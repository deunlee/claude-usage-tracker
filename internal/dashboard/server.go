package dashboard

import (
	"embed"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"claudeusage/internal/claude"
	"claudeusage/internal/poller"
	"claudeusage/internal/store"
	"claudeusage/internal/util"
)

const textType = "text/plain; charset=utf-8"
const jsonType = "application/json; charset=utf-8"

type staticFile struct {
	file        string
	contentType string
}

// URL이 파일 이름과 다르고(/ → index.html), Content-Type을 확장자 추측에 맡기지 않으려고 목록으로 정해 둔다.
var staticFiles = map[string]staticFile{
	"/":            {"index.html", "text/html; charset=utf-8"},
	"/style.css":   {"style.css", "text/css; charset=utf-8"},
	"/clawd.js":    {"clawd.js", "text/javascript; charset=utf-8"},
	"/app.js":      {"app.js", "text/javascript; charset=utf-8"},
	"/clawd":       {"clawd.html", "text/html; charset=utf-8"},
	"/gallery.js":  {"gallery.js", "text/javascript; charset=utf-8"},
	"/robots.txt":  {"robots.txt", "text/plain; charset=utf-8"},
	"/login":       {"login.html", "text/html; charset=utf-8"},
	"/login.js":    {"login.js", "text/javascript; charset=utf-8"},
	"/favicon.svg": {"favicon.svg", "image/svg+xml"},
}

// 새로고침 요청을 수집 고루틴이 받지 못하고 있으면(수집 중이면) 오래 붙잡지 않고 지금 상태로 답한다.
var refreshAckTimeout = 3 * time.Second

// 대시보드 파일을 실행 파일에 넣어 두어, exe 옆에는 config.json만 있으면 된다.
//
//go:embed static
var staticFS embed.FS

// 설정 디렉터리 경로는 보내지 않는다. 크리덴셜 파일이 늘 그 안의 .credentials.json이라 위치가 드러난다.
type accountEntry struct {
	Email *string `json:"email"`
	Alias *string `json:"alias"`
}

type latestPayload struct {
	FetchedAt *string           `json:"fetchedAt"`
	Readings  []claude.Reading  `json:"readings"`
	Breakdown *claude.Breakdown `json:"breakdown"`
}

type dashboardPayload struct {
	GeneratedAt string         `json:"generatedAt"`
	Accounts    []accountEntry `json:"accounts"`
	Account     int            `json:"account"`
	store.View
	Activity store.Activity `json:"activity"`
	Latest   latestPayload  `json:"latest"`
	Status   poller.Status  `json:"status"`
	// 화면이 저장된 비밀번호 없이 열렸을 때 로그인 창으로 보낼지 정하는 데 쓴다. 비밀번호가 비어 있어도 로그인 창은 거친다.
	IPAllowed bool `json:"ipAllowed"`
}

type Server struct {
	host    string
	port    int
	index   *store.Index
	pollers []*poller.Poller
	aliases claude.AccountAliases
	access  Access
	// nil이면 Host 헤더를 검사하지 않는다.
	allowedHosts map[string]bool
	server       *http.Server
}

func New(host string, port int, access Access, index *store.Index, pollers []*poller.Poller, aliases claude.AccountAliases) *Server {
	d := &Server{host: host, port: port, access: access, index: index, pollers: pollers, aliases: aliases}
	// DNS 리바인딩 차단: 다른 출처의 페이지가 자기 호스트명을 127.0.0.1로 돌려도 Host 헤더는 자기 이름을 보낸다.
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		p := strconv.Itoa(port)
		d.allowedHosts = map[string]bool{"127.0.0.1:" + p: true, "localhost:" + p: true, "[::1]:" + p: true}
	}
	d.server = &http.Server{
		Handler: d,
		// 헤더를 보내다 만 연결이 끝없이 남지 않게 한다. Node HTTP 서버의 기본값과 같은 60초다.
		ReadHeaderTimeout: time.Minute,
		// 응답이 가장 오래 걸리는 새로고침도 refreshAckTimeout(3초)만 기다리므로 넉넉히 잡아도 막힌 연결만 끊긴다.
		WriteTimeout: 30 * time.Second,
		// 요청을 마친 keep-alive 연결이 다음 요청 없이 끝없이 남지 않게 한다.
		IdleTimeout: 2 * time.Minute,
		ErrorLog:    log.New(logWriter{prefix: "대시보드 서버 오류: "}, "", 0),
	}
	return d
}

func (d *Server) URL() string {
	return "http://" + net.JoinHostPort(d.host, strconv.Itoa(d.port)) + "/"
}

// 대시보드만 멈춘 채 수집이 이어지면 알아채기 어려워, 돌려준 채널로 알려 호출한 쪽이 프로그램을 끝낼 수 있게 한다.
// 채널은 Close가 아닌 이유로 서버가 멈추면 그 오류를 받고, 서버가 멈추면 닫힌다.
func (d *Server) Listen() (<-chan error, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(d.host, strconv.Itoa(d.port)))
	if err != nil {
		return nil, err
	}
	return d.serve(listener), nil
}

func (d *Server) serve(listener net.Listener) <-chan error {
	stopped := make(chan error, 1)
	go func() {
		defer close(stopped)
		if err := d.server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			stopped <- err
		}
	}()
	return stopped
}

func (d *Server) Close() {
	d.server.Close()
}

func (d *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := d.handle(w, r); err != nil {
		util.Logf("대시보드 요청 처리 실패: %v", err)
		send(w, r, http.StatusInternalServerError, textType, []byte("internal error"))
	}
}

func (d *Server) handle(w http.ResponseWriter, r *http.Request) error {
	if d.allowedHosts != nil && !d.allowedHosts[r.Host] {
		send(w, r, http.StatusMisdirectedRequest, textType, []byte("misdirected request"))
		return nil
	}
	// 페이지와 스크립트에는 수집한 값이 없어 그대로 보내고, 값을 돌려주거나 상태를 바꾸는 API만 막는다.
	if strings.HasPrefix(r.URL.Path, "/api/") && !d.authorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		send(w, r, http.StatusUnauthorized, textType, []byte("unauthorized"))
		return nil
	}
	if r.URL.Path == "/api/login" {
		return d.login(w, r)
	}
	if r.URL.Path == "/api/refresh" {
		return d.refresh(w, r)
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		send(w, r, http.StatusMethodNotAllowed, textType, []byte("method not allowed"))
		return nil
	}
	if static, ok := staticFiles[r.URL.Path]; ok {
		body, err := staticFS.ReadFile("static/" + static.file)
		if err != nil {
			return err
		}
		send(w, r, http.StatusOK, static.contentType, body)
		return nil
	}
	if r.URL.Path == "/api/dashboard" {
		body, err := d.dashboard(r)
		if err != nil {
			return err
		}
		send(w, r, http.StatusOK, jsonType, body)
		return nil
	}
	send(w, r, http.StatusNotFound, textType, []byte("not found"))
	return nil
}

// 비밀번호 검사는 handle에서 마쳤으므로 여기까지 오면 맞는 비밀번호다.
func (d *Server) login(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		send(w, r, http.StatusMethodNotAllowed, textType, []byte("method not allowed"))
		return nil
	}
	send(w, r, http.StatusOK, textType, []byte("ok"))
	return nil
}

// 상태를 바꾸는 요청이라 POST만 받고, 다른 출처의 페이지가 브라우저를 시켜 보내는 요청은 Origin으로 막는다.
// Origin이 없는 요청(curl 등)은 브라우저 밖에서 보낸 것이라 받는다.
func (d *Server) refresh(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		send(w, r, http.StatusMethodNotAllowed, textType, []byte("method not allowed"))
		return nil
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
		send(w, r, http.StatusForbidden, textType, []byte("forbidden"))
		return nil
	}
	handled := make([]<-chan struct{}, len(d.pollers))
	for i, p := range d.pollers {
		handled[i] = p.RequestRefresh()
	}
	timer := time.NewTimer(refreshAckTimeout)
	defer timer.Stop()
wait:
	for _, done := range handled {
		select {
		case <-done:
		case <-timer.C:
			break wait
		case <-r.Context().Done():
			break wait
		}
	}
	// 화면에는 고른 계정만 보이므로, 받은 값을 다시 불러올 시각을 정하는 데 필요한 그 계정의 상태만 돌려준다.
	p := d.pollers[accountIndex(r.URL.Query().Get("account"), len(d.pollers))]
	body, err := util.MarshalJSON(p.Status())
	if err != nil {
		return err
	}
	send(w, r, http.StatusOK, jsonType, body)
	return nil
}

func (d *Server) dashboard(r *http.Request) ([]byte, error) {
	query := r.URL.Query()
	statuses := make([]poller.Status, len(d.pollers))
	accounts := make([]accountEntry, len(d.pollers))
	for i, p := range d.pollers {
		statuses[i] = p.Status()
		accounts[i] = d.accountEntry(statuses[i].Email)
	}
	account := accountIndex(query.Get("account"), len(d.pollers))
	status := statuses[account]
	latest := d.pollers[account].LastSnapshot()

	// 서버를 막 켜 아직 수집에 성공하지 못했으면 5시간·주간을 0%로 보낸다.
	payloadLatest := latestPayload{Readings: claude.PlaceholderReadings()}
	if latest != nil {
		payloadLatest = latestPayload{FetchedAt: &latest.FetchedAt, Readings: latest.Snapshot.Readings(), Breakdown: latest.Snapshot.Breakdown()}
	}
	labels := map[string]string{}
	for _, entry := range payloadLatest.Readings {
		if entry.Field != nil {
			labels[*entry.Field] = entry.Label
		}
	}
	email := ""
	if status.Email != nil {
		email = *status.Email
	}
	now := time.Now()
	view, err := d.index.View(now, email, query.Get("range"), labels)
	if err != nil {
		return nil, err
	}
	activity, err := d.index.Activity(now, email)
	if err != nil {
		return nil, err
	}
	return util.MarshalJSON(dashboardPayload{
		GeneratedAt: util.ISOString(now),
		Accounts:    accounts,
		Account:     account,
		View:        view,
		Activity:    activity,
		Latest:      payloadLatest,
		Status:      status,
		IPAllowed:   d.ipAllowed(r),
	})
}

func (d *Server) accountEntry(email *string) accountEntry {
	entry := accountEntry{Email: email}
	if email != nil {
		if alias, ok := d.aliases.Lookup(*email); ok {
			entry.Alias = &alias
		}
	}
	return entry
}

// 계정은 config.json에 적은 순서(~/.claude가 0번)로 고른다. 범위를 벗어나면 첫 계정을 보여 준다.
func accountIndex(raw string, count int) int {
	index, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || index < 0 || index >= count {
		return 0
	}
	return index
}

func send(w http.ResponseWriter, r *http.Request, status int, contentType string, body []byte) {
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Content-Length", strconv.Itoa(len(body)))
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		w.Write(body)
	}
}

type logWriter struct {
	prefix string
}

func (w logWriter) Write(b []byte) (int, error) {
	util.Logf("%s%s", w.prefix, strings.TrimRight(string(b), "\n"))
	return len(b), nil
}
