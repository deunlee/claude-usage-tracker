package store

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"claudeusage/internal/claude"
	"claudeusage/internal/util"
)

type sample struct {
	t      int64
	values summaryValues
}

type seriesInfo struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Slot  int    `json:"slot"`
}

type rangeInfo struct {
	Key      string `json:"key"`
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	BucketMs int64  `json:"bucketMs"`
}

// 대시보드는 [시각, 계열1, 계열2, ...] 배열로 받는다.
type chartPoint struct {
	t      int64
	values []*float64
}

func (p chartPoint) MarshalJSON() ([]byte, error) {
	row := make([]any, 0, len(p.values)+1)
	row = append(row, p.t)
	for _, value := range p.values {
		row = append(row, value)
	}
	return json.Marshal(row)
}

type View struct {
	Range   rangeInfo    `json:"range"`
	Catalog []seriesInfo `json:"catalog"`
	Points  []chartPoint `json:"points"`
}

const defaultRange = "12h"

var ranges = map[string]time.Duration{
	"1h":  time.Hour,
	"3h":  3 * time.Hour,
	"6h":  6 * time.Hour,
	"12h": 12 * time.Hour,
	"1d":  24 * time.Hour,
	"2d":  2 * 24 * time.Hour,
	"3d":  3 * 24 * time.Hour,
	"5d":  5 * 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"14d": 14 * 24 * time.Hour,
}

var longestRange = slices.Max(slices.Collect(maps.Values(ranges)))

// 기간마다 다른 달을 읽으면 계열 순서(곧 선 색)가 기간에 따라 바뀌므로, 가장 긴 기간이 걸치는 달을 늘 함께 읽는다.
// 가장 긴 기간이 14일이라 많아야 이번 달과 지난달 두 파일이다.
func monthFilesCovering(from, to time.Time) []string {
	start := util.Local(from)
	var names []string
	for cursor := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, util.Location()); !cursor.After(to); cursor = cursor.AddDate(0, 1, 0) {
		names = append(names, monthlyFileName(cursor))
	}
	return names
}

func firstIndexAtOrAfter(samples []sample, t int64) int {
	return sort.Search(len(samples), func(i int) bool { return samples[i].t >= t })
}

const maxChartPoints = 1500

var primaryFields = []string{"fiveHour", "sevenDay"}

// 대시보드 팔레트가 8색(--s1~--s8)뿐이라, 9번째 계열에 색을 만들어 붙이지 않는다.
const maxSeries = 8

func fieldLabel(field string) string {
	switch field {
	case "fiveHour":
		return claude.SeriesLabel("session", "", "")
	case "sevenDay":
		return claude.SeriesLabel("weekly_all", "", "")
	}
	return claude.SeriesLabel("weekly_scoped", strings.TrimPrefix(field, "sevenDay"), "")
}

type cachedMonth struct {
	stamp   fileStamp
	samples []sample
}

type Index struct {
	root string
	// 가장 촘촘한 수집 주기보다 잘게 나누면 기록이 없는 버킷만 늘어난다.
	minBucket time.Duration
	mu        sync.Mutex
	months    map[string]cachedMonth
	activity  activityCaches
}

func NewIndex(root string, minBucket time.Duration) *Index {
	return &Index{root: root, minBucket: minBucket, months: map[string]cachedMonth{}}
}

// email: 아직 계정 정보를 읽지 못했으면 빈 문자열이고, 그때는 기록 없이 빈 그래프를 준다.
// labels: 최신 응답에서 얻은 키별 표시 이름. 요약 키만으로는 모델명의 원래 표기(공백·점)를 되살릴 수 없다.
func (x *Index) View(now time.Time, email, requestedRange string, labels map[string]string) (View, error) {
	rangeKey := requestedRange
	if _, ok := ranges[rangeKey]; !ok {
		rangeKey = defaultRange
	}
	to := now.UnixMilli()
	from := to - ranges[rangeKey].Milliseconds()
	var samples []sample
	if email != "" {
		dir, err := accountArchiveDir(x.root, email)
		if err != nil {
			return View{}, err
		}
		// 달마다 시각 순으로 정렬해 캐시해 두었으니, 달 순서대로 이으면 전체도 시각 순이다.
		for _, name := range monthFilesCovering(now.Add(-longestRange), now) {
			samples = append(samples, x.cached(filepath.Join(dir, name))...)
		}
	}
	catalog := buildCatalog(samples, labels)
	start := firstIndexAtOrAfter(samples, from)
	minute := time.Minute.Milliseconds()
	bucketMs := max(x.minBucket.Milliseconds(), int64(math.Ceil(float64(to-from)/maxChartPoints/float64(minute)))*minute)
	points := bucketize(samples[start:firstIndexAtOrAfter(samples, to+1)], catalog, bucketMs)
	// 범위 직전 기록은 축 밖이라, 그 기록에만 있는 계열은 보이는 선이 없다. 붙이기 전에 고른다.
	shown := seriesWithRecords(catalog, points)
	// 범위 첫 기록까지 선이 비어 왼쪽 끝이 잘려 보이지 않도록, 범위 직전 기록을 하나 앞에 붙인다.
	// 그 기록에서 첫 기록까지의 선분을 그대로 그리고 축 시작(from)에서 잘라야 기울기가 실제와 같다.
	// 첫 버킷과 합치면 시각이 범위 안으로 당겨져 기울기가 틀어지므로 따로 둔다.
	if start > 0 && len(points) > 0 {
		points = append(bucketize(samples[start-1:start], catalog, bucketMs), points...)
	}
	catalog, points = keepSeries(catalog, points, shown)
	return View{
		Range:   rangeInfo{Key: rangeKey, From: from, To: to, BucketMs: bucketMs},
		Catalog: catalog,
		Points:  points,
	}, nil
}

// 모델별 주간 한도는 플랜을 내리면 응답에서 빠진다(Max → Pro의 Fable). 기간 안에 기록이 없는 계열은 그래프에 보내지 않는다.
// 5시간·주간은 기록이 없어도 0으로 그리므로 늘 남긴다.
func seriesWithRecords(catalog []seriesInfo, points []chartPoint) []int {
	var shown []int
	for i, series := range catalog {
		if slices.Contains(primaryFields, series.Key) || slices.ContainsFunc(points, func(p chartPoint) bool { return p.values[i] != nil }) {
			shown = append(shown, i)
		}
	}
	return shown
}

// 남긴 계열의 slot은 그대로 두어, 기간을 바꿔도 같은 계열은 같은 색으로 그린다.
func keepSeries(catalog []seriesInfo, points []chartPoint, shown []int) ([]seriesInfo, []chartPoint) {
	if len(shown) == len(catalog) {
		return catalog, points
	}
	kept := make([]seriesInfo, len(shown))
	for j, i := range shown {
		kept[j] = catalog[i]
	}
	for p := range points {
		values := make([]*float64, len(shown))
		for j, i := range shown {
			values[j] = points[p].values[i]
		}
		points[p].values = values
	}
	return kept, points
}

func (x *Index) cached(file string) []sample {
	x.mu.Lock()
	defer x.mu.Unlock()
	samples, err := x.load(file)
	// 지난달 파일은 처음 쓰는 달에는 없는 게 정상이라, 요청마다 로그를 남기지 않는다.
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		util.Logf("%s을 읽지 못했습니다: %v", file, err)
	}
	return samples
}

func (x *Index) load(file string) ([]sample, error) {
	info, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if hit, ok := x.months[file]; ok && hit.stamp == stampOf(info) {
		return hit.samples, nil
	}
	records, err := readJSONArray(file)
	if err != nil {
		return nil, err
	}
	samples := parseSummaries(records)
	x.months[file] = cachedMonth{stamp: stampOf(info), samples: samples}
	return samples, nil
}

// 덧붙이기만 하므로 보통 이미 시각 순이지만, 사람이 고친 파일도 있을 수 있어 읽을 때 한 번 정렬해 캐시한다.
func parseSummaries(records []json.RawMessage) []sample {
	var samples []sample
	for _, record := range records {
		if s, ok := parseSummary(record); ok {
			samples = append(samples, s)
		}
	}
	slices.SortStableFunc(samples, func(a, b sample) int { return cmp.Compare(a.t, b.t) })
	return samples
}

// 키 순서를 지키려고 맵으로 풀지 않고 토큰 단위로 읽는다.
func parseSummary(record json.RawMessage) (sample, bool) {
	decoder := json.NewDecoder(bytes.NewReader(record))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return sample{}, false
	}
	var s sample
	var timeText string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return sample{}, false
		}
		key, _ := token.(string)
		var value any
		if err := decoder.Decode(&value); err != nil {
			return sample{}, false
		}
		if key == "time" {
			timeText, _ = value.(string)
			continue
		}
		s.values.set(key, util.Num(value))
	}
	t, ok := util.ParseTime(timeText)
	if !ok {
		return sample{}, false
	}
	s.t = t.UnixMilli()
	return s, true
}

func buildCatalog(samples []sample, labels map[string]string) []seriesInfo {
	// 색이 계열을 따라가도록, 순서는 기간 필터와 무관하게 읽어 온 달 전체에서 처음 나타난 순서로 고정한다.
	fields := slices.Clone(primaryFields)
	for _, s := range samples {
		for _, value := range s.values {
			if !slices.Contains(fields, value.name) {
				fields = append(fields, value.name)
			}
		}
	}
	fields = fields[:min(len(fields), maxSeries)]
	catalog := make([]seriesInfo, len(fields))
	for i, key := range fields {
		label, ok := labels[key]
		if !ok {
			label = fieldLabel(key)
		}
		catalog[i] = seriesInfo{Key: key, Label: label, Slot: i + 1}
	}
	return catalog
}

// 긴 기간은 버킷별 최댓값으로 줄인다. 한도에 얼마나 다가갔는지가 중요해서 평균 대신 최댓값을 쓴다.
func bucketize(samples []sample, catalog []seriesInfo, bucketMs int64) []chartPoint {
	points := []chartPoint{}
	var bucket int64
	pinned := false
	for _, s := range samples {
		values := make([]*float64, len(catalog))
		for i, series := range catalog {
			values[i] = valueOf(s, series.Key)
		}
		index := s.t / bucketMs
		// 초기화로 값이 내려가면 같은 버킷이어도 새 점을 시작하고, 그 점에는 뒤 기록을 합치지 않는다.
		// 합치면 초기화 직전·직후 기록(…:59 100%, …:00 0%)이 최댓값 한 점이 되거나 점의 시각이 뒤로 밀려 사선으로 내려온다.
		drop := len(points) > 0 && dropsBelow(points[len(points)-1].values, values)
		if len(points) == 0 || index != bucket || drop || pinned {
			bucket = index
			points = append(points, chartPoint{t: s.t, values: values})
			pinned = drop
			continue
		}
		row := &points[len(points)-1]
		row.t = s.t
		for i, value := range values {
			if value != nil && (row.values[i] == nil || *value > *row.values[i]) {
				row.values[i] = value
			}
		}
	}
	return points
}

func dropsBelow(row, values []*float64) bool {
	for i, value := range values {
		if value != nil && row[i] != nil && *value < *row[i] {
			return true
		}
	}
	return false
}

func valueOf(s sample, field string) *float64 {
	value := s.values.get(field)
	// 5시간·주간은 응답에 창 필드가 없던 기록(null)도 그래프가 끊기지 않게 0으로 그린다.
	// 모델별 주간은 키가 없으면 그 한도가 아직 없던 때라 그리지 않는다.
	if value == nil && slices.Contains(primaryFields, field) {
		zero := 0.0
		return &zero
	}
	return value
}
