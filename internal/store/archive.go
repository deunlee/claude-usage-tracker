package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"claudeusage/internal/claude"
	"claudeusage/internal/util"
)

// 원본: 응답 본문을 손대지 않고 logs/의 일별 파일에 쌓는다. 성공한 응답은 recent.json에도 최근 것만 남긴다.
type Record struct {
	FetchedAt    string          `json:"fetchedAt"`
	OK           bool            `json:"ok"`
	HTTPStatus   *int            `json:"httpStatus"`
	DurationMs   *int64          `json:"durationMs"`
	CLIVersion   string          `json:"cliVersion"`
	NextPollInMs int64           `json:"nextPollInMs"`
	Data         json.RawMessage `json:"data,omitempty"`
	Error        string          `json:"error,omitempty"`
	// rate limit 기록에만 붙는다. 응답에 retry-after가 없었으면 null이다.
	RetryAfter   json.RawMessage `json:"retryAfter,omitempty"`
	ResponseBody json.RawMessage `json:"responseBody,omitempty"`
}

type summaryValue struct {
	name  string
	value *float64
}

// 키 순서가 대시보드의 계열 순서(곧 선 색)를 정하므로 맵이 아니라 순서 있는 목록으로 둔다.
type summaryValues []summaryValue

func (v *summaryValues) set(name string, value *float64) {
	for i := range *v {
		if (*v)[i].name == name {
			(*v)[i].value = value
			return
		}
	}
	*v = append(*v, summaryValue{name: name, value: value})
}

func (v summaryValues) get(name string) *float64 {
	for _, field := range v {
		if field.name == name {
			return field.value
		}
	}
	return nil
}

// 요약: 성공한 수집만, 그래프에 쓰는 값만 월별 파일에 쌓는다.
// 모델별 주간 한도는 sevenDay<모델명> 키로 붙는다(예: sevenDayFable).
type Summary struct {
	time   string
	values summaryValues
}

func (r Summary) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`{"time":`)
	if err := writeJSON(&b, r.time); err != nil {
		return nil, err
	}
	for _, field := range r.values {
		b.WriteByte(',')
		if err := writeJSON(&b, field.name); err != nil {
			return nil, err
		}
		b.WriteByte(':')
		if err := writeJSON(&b, field.value); err != nil {
			return nil, err
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func writeJSON(b *bytes.Buffer, value any) error {
	encoded, err := util.MarshalJSON(value)
	b.Write(encoded)
	return err
}

// 이메일을 그대로 폴더 이름으로 쓰므로, archive 밖을 가리키거나 Windows가 거부하는 이름은 받지 않는다.
var unsafeDirName = regexp.MustCompile(`[<>:"/\\|?*\p{Cc}]|^\.\.?$|[. ]$`)

func accountArchiveDir(root, email string) (string, error) {
	if email == "" || unsafeDirName.MatchString(email) {
		quoted, _ := util.MarshalJSON(email)
		return "", fmt.Errorf("폴더 이름으로 쓸 수 없는 이메일입니다: %s", quoted)
	}
	return filepath.Join(root, email), nil
}

// 실행 로그(YYYY-MM-DD.log)와 같은 폴더에 쌓이므로 날짜를 앞에 두어 함께 정렬되게 한다.
// 이메일은 accountArchiveDir를 통과한 값이라 파일 이름에 써도 된다.
func logFileName(t time.Time, email string) string {
	return util.LocalDay(t) + "-" + email + ".json"
}

const recentFileName = "recent.json"

// 다시 시작할 때 마지막 응답을 찾는 데만 쓰므로 많이 남길 필요가 없다. 전체 기록은 logs/에 있다.
const recentLimit = 50

func monthlyFileName(t time.Time) string {
	return util.LocalMonth(t) + ".json"
}

func SummaryOf(snapshot *claude.Snapshot, fetchedAt time.Time) Summary {
	// fiveHour·sevenDay는 응답에 창이 없어도 키를 남겨 모든 기록의 앞부분 모양을 같게 한다.
	record := Summary{time: util.LocalISO(fetchedAt), values: summaryValues{{name: "fiveHour"}, {name: "sevenDay"}}}
	for _, r := range snapshot.Readings() {
		if r.Field != nil {
			record.values.set(*r.Field, &r.Percent)
		}
	}
	return record
}

// 파일이 없으면 빈 목록이다.
func readJSONArray(file string) ([]json.RawMessage, error) {
	text, err := util.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []json.RawMessage
	err = json.Unmarshal(text, &records)
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) || (err == nil && records == nil) {
		return nil, fmt.Errorf("%s이 JSON 배열이 아닙니다", file)
	}
	if err != nil {
		return nil, err
	}
	return records, nil
}

// 기록을 덧붙일 때마다 파일 전체를 다시 쓰므로, 들여쓰기 없이 한 줄에 한 건씩 둔다.
func oneRecordPerLine(records []json.RawMessage) []byte {
	var b bytes.Buffer
	b.WriteString("[\n")
	for i, record := range records {
		if i > 0 {
			b.WriteString(",\n")
		}
		b.Write(record)
	}
	b.WriteString("\n]\n")
	return b.Bytes()
}

func replaceFile(file string, data []byte) error {
	// 임시 파일에 쓰고 바꿔치기해야, 쓰는 도중 꺼져도 파일 전체가 깨지지 않는다.
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o666); err != nil {
		return err
	}
	for attempt := 1; ; attempt++ {
		err := os.Rename(tmp, file)
		// Windows는 다른 프로세스(인덱서, 백신, 열어둔 뷰어)가 잡고 있는 파일을 덮어쓰지 못한다. 보통 금방 풀린다.
		if err == nil || attempt >= 5 || !util.IsFileBusy(err) {
			return err
		}
		time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
	}
}

type fileStamp struct {
	modTime time.Time
	size    int64
}

func stampOf(info fs.FileInfo) fileStamp {
	return fileStamp{modTime: info.ModTime(), size: info.Size()}
}

type jsonArrayAppender struct {
	dir  string
	name string
	// 0보다 크면 마지막 limit건만 남긴다.
	limit   int
	records []json.RawMessage
	written *fileStamp
}

func (a *jsonArrayAppender) append(name string, record json.RawMessage) error {
	file := filepath.Join(a.dir, name)
	reload := name != a.name
	if !reload {
		changed, err := a.changedSinceWrite(file)
		if err != nil {
			return err
		}
		reload = changed
	}
	if reload {
		records, err := loadRecords(file)
		if err != nil {
			return err
		}
		a.records, a.name = records, name
	}
	a.records = append(a.records, record)
	if a.limit > 0 && len(a.records) > a.limit {
		a.records = slices.Clone(a.records[len(a.records)-a.limit:])
	}
	if err := replaceFile(file, oneRecordPerLine(a.records)); err != nil {
		return err
	}
	info, err := os.Stat(file)
	if err != nil {
		return err
	}
	stamp := stampOf(info)
	a.written = &stamp
	return nil
}

// 사람이나 다른 프로세스가 파일을 고쳤다면, 메모리의 목록으로 덮어쓰지 않도록 다시 읽는다.
func (a *jsonArrayAppender) changedSinceWrite(file string) (bool, error) {
	info, err := os.Stat(file)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return a.written == nil || *a.written != stampOf(info), nil
}

func loadRecords(file string) ([]json.RawMessage, error) {
	records, err := readJSONArray(file)
	if err == nil {
		// 사람이 들여쓰기해 둔 파일이어도 다시 쓸 때는 한 줄에 한 건이 되게 한다.
		for i, record := range records {
			var compact bytes.Buffer
			if err := json.Compact(&compact, record); err != nil {
				return nil, err
			}
			records[i] = compact.Bytes()
		}
		return records, nil
	}
	aside := fmt.Sprintf("%s.corrupt-%d", file, time.Now().UnixMilli())
	if renameErr := os.Rename(file, aside); renameErr != nil {
		return nil, renameErr
	}
	util.Logf("%s을 읽을 수 없어 %s로 옮기고 새로 시작합니다 (%v)", file, aside, err)
	return nil, nil
}

type accountFiles struct {
	dir     string
	log     jsonArrayAppender
	recent  jsonArrayAppender
	monthly jsonArrayAppender
}

type Archive struct {
	root   string
	logDir string
	// 두 설정 디렉터리가 같은 계정에 로그인돼 있으면 같은 파일을 동시에 고쳐 쓰게 되므로 한 번에 하나씩 쓴다.
	mu       sync.Mutex
	accounts map[string]*accountFiles
}

// root: 대시보드가 읽는 계정별 폴더(recent.json, YYYY-MM.json)를 둔다.
// logDir: 모든 원본 기록을 계정·날짜별 파일로 쌓기만 하는 곳이다. 프로그램은 다시 읽지 않는다.
func NewArchive(root, logDir string) *Archive {
	return &Archive{root: root, logDir: logDir, accounts: map[string]*accountFiles{}}
}

func (a *Archive) Init() error {
	return errors.Join(os.MkdirAll(a.root, 0o777), os.MkdirAll(a.logDir, 0o777))
}

func (a *Archive) Append(email string, fetchedAt time.Time, record Record, summary *Summary) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	files, err := a.filesOf(email)
	if err != nil {
		return err
	}
	encoded, err := util.MarshalJSON(record)
	if err != nil {
		return err
	}
	// 쌓기만 하는 로그에 쓰지 못했다고 대시보드가 읽는 파일까지 건너뛰지 않는다.
	logErr := files.log.append(logFileName(fetchedAt, email), encoded)
	if summary == nil {
		return logErr
	}
	recentErr := files.recent.append(recentFileName, encoded)
	encodedSummary, err := util.MarshalJSON(summary)
	if err != nil {
		return errors.Join(logErr, recentErr, err)
	}
	return errors.Join(logErr, recentErr, files.monthly.append(monthlyFileName(fetchedAt), encodedSummary))
}

func (a *Archive) filesOf(email string) (*accountFiles, error) {
	if files, ok := a.accounts[email]; ok {
		return files, nil
	}
	dir, err := accountArchiveDir(a.root, email)
	if err != nil {
		return nil, err
	}
	if err := errors.Join(os.MkdirAll(dir, 0o777), os.MkdirAll(a.logDir, 0o777)); err != nil {
		return nil, err
	}
	files := &accountFiles{
		dir:     dir,
		log:     jsonArrayAppender{dir: a.logDir},
		recent:  jsonArrayAppender{dir: dir, limit: recentLimit},
		monthly: jsonArrayAppender{dir: dir},
	}
	a.accounts[email] = files
	return files, nil
}
