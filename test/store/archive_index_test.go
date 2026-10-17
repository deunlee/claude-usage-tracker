//go:build separatetest

package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"claudeusage/internal/util"
)

const testMinBucket = 2 * time.Minute

func TestViewBucketsByMaxAndKeepsSeriesOrder(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "someone@example.com")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	// 버킷은 에포크 기준 2분 단위라, 시간대 오프셋이 :30·:45인 곳에서도 경계가 같도록 UTC 정각에서 잰다.
	now := time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC).Local()
	at := func(minutesAgo int) string { return util.LocalISO(now.Add(-time.Duration(minutesAgo) * time.Minute)) }
	// 지난달 기록이 계열 순서를 먼저 정한다. sevenDayOpus는 범위 안에 기록이 없어 빠지지만, 그 slot을 sevenDayFable이 당겨 쓰지 않는다.
	last := `[
{"time":"` + util.LocalISO(now.AddDate(0, 0, -10)) + `","sevenDayOpus":5,"fiveHour":1,"sevenDay":1}
]`
	current := `[
{"time":"` + at(1) + `","fiveHour":30,"sevenDay":12,"sevenDayFable":2},
{"time":"` + at(3) + `","fiveHour":5,"sevenDay":null},
{"time":"` + at(2) + `","fiveHour":10,"sevenDay":10},
{"time":"not a time","fiveHour":99}
]`
	writeMonth(t, dir, now.AddDate(0, 0, -10), last)
	writeMonth(t, dir, now, current)

	view, err := NewIndex(root, testMinBucket).View(now, "someone@example.com", "1h", map[string]string{"sevenDayFable": "이번 주 (Fable)"})
	if err != nil {
		t.Fatal(err)
	}
	wantCatalog := `[{"key":"fiveHour","label":"5시간 세션","slot":1},{"key":"sevenDay","label":"이번 주 (모든 모델)","slot":2},` +
		`{"key":"sevenDayFable","label":"이번 주 (Fable)","slot":4}]`
	if got := encode(t, view.Catalog); got != wantCatalog {
		t.Errorf("catalog:\n got %s\nwant %s", got, wantCatalog)
	}
	if view.Range.Key != "1h" || view.Range.To != now.UnixMilli() || view.Range.BucketMs != testMinBucket.Milliseconds() {
		t.Errorf("range %+v", view.Range)
	}
	// 1·2분 전 기록은 같은 2분 버킷이라 최댓값으로 합쳐지고, 시각은 마지막 기록을 따른다.
	// 맨 앞은 범위 직전 기록(지난달)이다.
	t10d, t3, t1 := now.AddDate(0, 0, -10).UnixMilli(), now.Add(-3*time.Minute).UnixMilli(), now.Add(-1*time.Minute).UnixMilli()
	got := encode(t, view.Points)
	want := encode(t, []any{[]any{t10d, 1, 1, nil}, []any{t3, 5, 0, nil}, []any{t1, 30, 12, 2}})
	if got != want {
		t.Errorf("points:\n got %s\nwant %s", got, want)
	}
}

// 3일 기간은 버킷이 3분이라 초기화 1초 전 기록과 초기화 기록이 같은 버킷에 든다. 둘을 합치면 0%가 사라진다.
func TestViewKeepsResetDropInsideBucket(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "someone@example.com")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	// 버킷은 에포크 기준 3분 단위라, 초기화(02:01 UTC)가 02:00~02:03 버킷 한가운데 오게 한다.
	now := time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC).Local()
	reset := time.Date(2026, 10, 4, 2, 1, 0, 0, time.UTC).Local()
	at := func(t time.Time) string { return util.LocalISO(t) }
	writeMonth(t, dir, now, `[
{"time":"`+at(reset.Add(-90*time.Second))+`","fiveHour":70,"sevenDay":20},
{"time":"`+at(reset.Add(-time.Second))+`","fiveHour":80,"sevenDay":20},
{"time":"`+at(reset)+`","fiveHour":0,"sevenDay":20},
{"time":"`+at(reset.Add(time.Minute))+`","fiveHour":1,"sevenDay":20},
{"time":"`+at(reset.Add(4*time.Minute))+`","fiveHour":2,"sevenDay":20},
{"time":"`+at(reset.Add(4*time.Minute+30*time.Second))+`","fiveHour":3,"sevenDay":21}
]`)
	view, err := NewIndex(root, testMinBucket).View(now, "someone@example.com", "3d", nil)
	if err != nil {
		t.Fatal(err)
	}
	if view.Range.BucketMs != 3*time.Minute.Milliseconds() {
		t.Fatalf("bucket %d ms", view.Range.BucketMs)
	}
	// 초기화 기록은 홀로 남고, 올라가기만 하는 같은 버킷의 기록(02:05, 02:05:30)은 여전히 최댓값으로 합친다.
	ms := func(d time.Duration) int64 { return reset.Add(d).UnixMilli() }
	want := encode(t, []any{
		[]any{ms(-90 * time.Second), 70, 20},
		[]any{ms(-time.Second), 80, 20},
		[]any{ms(0), 0, 20},
		[]any{ms(time.Minute), 1, 20},
		[]any{ms(4*time.Minute + 30*time.Second), 3, 21},
	})
	if got := encode(t, view.Points); got != want {
		t.Errorf("points:\n got %s\nwant %s", got, want)
	}
}

func TestViewRangeKeys(t *testing.T) {
	now := time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)
	for key, want := range map[string]time.Duration{
		"1d":  24 * time.Hour,
		"2d":  2 * 24 * time.Hour,
		"3d":  3 * 24 * time.Hour,
		"5d":  5 * 24 * time.Hour,
		"24h": ranges[defaultRange],
	} {
		view, err := NewIndex(t.TempDir(), testMinBucket).View(now, "", key, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := time.Duration(view.Range.To-view.Range.From) * time.Millisecond; got != want {
			t.Errorf("range %q spans %v, want %v", key, got, want)
		}
	}
}

func TestViewKeepsSampleBeforeRangeApart(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "someone@example.com")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC).Local()
	at := func(d time.Duration) string { return util.LocalISO(now.Add(-d)) }
	// 61분 전과 59분 전은 같은 2분 버킷이지만, 범위 직전 기록은 첫 기록과 합치지 않는다. 그보다 앞선 기록은 쓰지 않는다.
	writeMonth(t, dir, now, `[
{"time":"`+at(70*time.Minute)+`","fiveHour":1,"sevenDay":1},
{"time":"`+at(61*time.Minute)+`","fiveHour":10,"sevenDay":5},
{"time":"`+at(59*time.Minute)+`","fiveHour":12,"sevenDay":5}
]`)
	index := NewIndex(root, testMinBucket)
	view, err := index.View(now, "someone@example.com", "1h", nil)
	if err != nil {
		t.Fatal(err)
	}
	t61, t59 := now.Add(-61*time.Minute).UnixMilli(), now.Add(-59*time.Minute).UnixMilli()
	if got, want := encode(t, view.Points), encode(t, []any{[]any{t61, 10, 5}, []any{t59, 12, 5}}); got != want {
		t.Errorf("points:\n got %s\nwant %s", got, want)
	}

	// 범위 안에 기록이 없으면 범위 밖 기록만으로는 그릴 선이 없다.
	view, err = index.View(now.Add(time.Hour), "someone@example.com", "1h", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := encode(t, view.Points); got != "[]" {
		t.Errorf("points without samples in range: %s", got)
	}
}

// Max에서 Pro로 내리면 응답에서 Fable 한도가 빠진다. Fable 기록이 없는 기간에는 그 계열을 보내지 않는다.
func TestViewHidesModelSeriesWithoutRecordsInRange(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "someone@example.com")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC).Local()
	at := func(d time.Duration) string { return util.LocalISO(now.Add(-d)) }
	writeMonth(t, dir, now, `[
{"time":"`+at(3*time.Hour)+`","fiveHour":10,"sevenDay":5,"sevenDayFable":7},
{"time":"`+at(30*time.Minute)+`","fiveHour":0,"sevenDay":5}
]`)
	index := NewIndex(root, testMinBucket)
	for _, c := range []struct {
		rangeKey, catalog string
	}{
		{"1h", `["fiveHour","sevenDay"]`},
		{"6h", `["fiveHour","sevenDay","sevenDayFable"]`},
	} {
		view, err := index.View(now, "someone@example.com", c.rangeKey, nil)
		if err != nil {
			t.Fatal(err)
		}
		keys := []string{}
		for _, series := range view.Catalog {
			keys = append(keys, series.Key)
		}
		if got := encode(t, keys); got != c.catalog {
			t.Errorf("%s catalog %s, want %s", c.rangeKey, got, c.catalog)
		}
		for _, point := range view.Points {
			if len(point.values) != len(view.Catalog) {
				t.Errorf("%s point %s has %d values for %d series", c.rangeKey, encode(t, point), len(point.values), len(view.Catalog))
			}
		}
	}
}

func TestViewWithoutEmailIsEmpty(t *testing.T) {
	view, err := NewIndex(t.TempDir(), testMinBucket).View(time.Now(), "", "nope", nil)
	if err != nil {
		t.Fatal(err)
	}
	if view.Range.Key != defaultRange || encode(t, view.Points) != "[]" || len(view.Catalog) != 2 {
		t.Errorf("view %s", encode(t, view))
	}
}

func TestMonthFilesCovering(t *testing.T) {
	to := time.Date(2026, 10, 4, 12, 0, 0, 0, time.Local)
	got := monthFilesCovering(to.Add(-longestRange), to)
	if len(got) != 2 || got[0] != "2026-09.json" || got[1] != "2026-10.json" {
		t.Errorf("months %v", got)
	}
}

func writeMonth(t *testing.T, dir string, at time.Time, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, monthlyFileName(at)), []byte(text), 0o666); err != nil {
		t.Fatal(err)
	}
}
