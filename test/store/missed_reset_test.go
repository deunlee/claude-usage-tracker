//go:build separatetest

package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"claudeusage/internal/util"
)

const missedEmail = "someone@example.com"

// fetchedAt에 받은 응답: 5시간 40%(fiveHourReset 뒤 초기화), 주간 9%(sevenDayReset 뒤 초기화), Fable 주간 3%(fableReset 뒤 초기화).
// 실제 응답처럼 초기화 시각을 경계 앞뒤로 1초 안쪽에서 흔들어 적는다.
func writeRecentAndMonthly(t *testing.T, root string, fetchedAt time.Time, fiveHourReset, sevenDayReset, fableReset time.Duration) {
	t.Helper()
	dir := filepath.Join(root, missedEmail)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	reset := func(d time.Duration) string { return fetchedAt.Add(d).UTC().Format("2006-01-02T15:04:05.999999-07:00") }
	data := `{"five_hour":{"utilization":40,"resets_at":"` + reset(fiveHourReset-681*time.Microsecond) + `"},` +
		`"seven_day":{"utilization":9,"resets_at":"` + reset(sevenDayReset+72468*time.Microsecond) + `"},` +
		`"limits":[{"kind":"weekly_scoped","percent":3,"resets_at":"` + reset(fableReset+495*time.Millisecond) + `","scope":{"model":{"display_name":"Fable"}}}]}`
	recent := `[
{"fetchedAt":"` + util.ISOString(fetchedAt) + `","ok":true,"data":` + data + `},
{"fetchedAt":"` + util.ISOString(fetchedAt.Add(time.Minute)) + `","ok":false,"error":"HTTP 500"}
]`
	if err := os.WriteFile(filepath.Join(dir, "recent.json"), []byte(recent), 0o666); err != nil {
		t.Fatal(err)
	}
	monthly := `[
{"time":"` + util.LocalISO(fetchedAt) + `","fiveHour":40,"sevenDay":9,"sevenDayFable":3}
]`
	if err := os.WriteFile(filepath.Join(dir, monthlyFileName(fetchedAt)), []byte(monthly), 0o666); err != nil {
		t.Fatal(err)
	}
}

func TestRecordMissedResetsWritesEveryResetOnce(t *testing.T) {
	root, logDir := t.TempDir(), t.TempDir()
	fetchedAt := time.Date(2026, 10, 4, 10, 0, 0, 0, time.Local)
	writeRecentAndMonthly(t, root, fetchedAt, time.Hour, 2*time.Hour, 3*time.Hour)
	recentBefore, _ := os.ReadFile(filepath.Join(root, missedEmail, "recent.json"))
	archive := NewArchive(root, logDir)

	resets, err := archive.RecordMissedResets(missedEmail, fetchedAt.Add(4*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(resets) != 3 || resets[0].Field != "fiveHour" || !resets[0].At.Equal(fetchedAt.Add(time.Hour)) ||
		resets[1].Field != "sevenDay" || !resets[1].At.Equal(fetchedAt.Add(2*time.Hour)) ||
		resets[2].Field != "sevenDayFable" || !resets[2].At.Equal(fetchedAt.Add(3*time.Hour)) {
		t.Fatalf("resets %+v", resets)
	}
	monthly, _ := readJSONArray(filepath.Join(root, missedEmail, monthlyFileName(fetchedAt)))
	want := []string{
		`{"time":"` + util.LocalISO(fetchedAt) + `","fiveHour":40,"sevenDay":9,"sevenDayFable":3}`,
		`{"time":"` + util.LocalISO(fetchedAt.Add(time.Hour-time.Second)) + `","fiveHour":40,"sevenDay":9,"sevenDayFable":3}`,
		`{"time":"` + util.LocalISO(fetchedAt.Add(time.Hour)) + `","fiveHour":0,"sevenDay":9,"sevenDayFable":3}`,
		`{"time":"` + util.LocalISO(fetchedAt.Add(2*time.Hour-time.Second)) + `","fiveHour":0,"sevenDay":9,"sevenDayFable":3}`,
		`{"time":"` + util.LocalISO(fetchedAt.Add(2*time.Hour)) + `","fiveHour":0,"sevenDay":0,"sevenDayFable":3}`,
		`{"time":"` + util.LocalISO(fetchedAt.Add(3*time.Hour-time.Second)) + `","fiveHour":0,"sevenDay":0,"sevenDayFable":3}`,
		`{"time":"` + util.LocalISO(fetchedAt.Add(3*time.Hour)) + `","fiveHour":0,"sevenDay":0,"sevenDayFable":0}`,
	}
	if got := encode(t, monthly); got != encode(t, rawList(want)) {
		t.Errorf("monthly:\n got %s\nwant %s", got, encode(t, rawList(want)))
	}
	if recentAfter, _ := os.ReadFile(filepath.Join(root, missedEmail, "recent.json")); string(recentAfter) != string(recentBefore) {
		t.Errorf("recent.json changed: %s", recentAfter)
	}
	if entries, _ := os.ReadDir(logDir); len(entries) != 0 {
		t.Errorf("wrote to the log directory: %v", entries)
	}

	// 수집에 성공하기 전에 다시 시작해도 같은 기록을 겹쳐 넣지 않는다.
	if again, err := NewArchive(root, logDir).RecordMissedResets(missedEmail, fetchedAt.Add(5*time.Hour)); err != nil || len(again) != 0 {
		t.Errorf("second start recorded %+v (%v)", again, err)
	}
}

func TestRecordMissedResetsSkipsResetsStillAhead(t *testing.T) {
	root := t.TempDir()
	fetchedAt := time.Date(2026, 10, 4, 10, 0, 0, 0, time.Local)
	writeRecentAndMonthly(t, root, fetchedAt, time.Hour, 2*time.Hour, 3*time.Hour)

	resets, err := NewArchive(root, t.TempDir()).RecordMissedResets(missedEmail, fetchedAt.Add(90*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(resets) != 1 || resets[0].Field != "fiveHour" {
		t.Fatalf("resets %+v", resets)
	}
	monthly, _ := readJSONArray(filepath.Join(root, missedEmail, monthlyFileName(fetchedAt)))
	want := []string{
		`{"time":"` + util.LocalISO(fetchedAt) + `","fiveHour":40,"sevenDay":9,"sevenDayFable":3}`,
		`{"time":"` + util.LocalISO(fetchedAt.Add(time.Hour-time.Second)) + `","fiveHour":40,"sevenDay":9,"sevenDayFable":3}`,
		`{"time":"` + util.LocalISO(fetchedAt.Add(time.Hour)) + `","fiveHour":0,"sevenDay":9,"sevenDayFable":3}`,
	}
	if got := encode(t, monthly); got != encode(t, rawList(want)) {
		t.Errorf("monthly:\n got %s\nwant %s", got, encode(t, rawList(want)))
	}
}

func TestRecordMissedResetsDropsResetsAtTheSameTimeTogether(t *testing.T) {
	root := t.TempDir()
	fetchedAt := time.Date(2026, 10, 4, 10, 0, 0, 0, time.Local)
	writeRecentAndMonthly(t, root, fetchedAt, time.Hour, time.Hour, time.Hour)

	resets, err := NewArchive(root, t.TempDir()).RecordMissedResets(missedEmail, fetchedAt.Add(2*time.Hour))
	if err != nil || len(resets) != 3 {
		t.Fatalf("resets %+v (%v)", resets, err)
	}
	monthly, _ := readJSONArray(filepath.Join(root, missedEmail, monthlyFileName(fetchedAt)))
	want := []string{
		`{"time":"` + util.LocalISO(fetchedAt) + `","fiveHour":40,"sevenDay":9,"sevenDayFable":3}`,
		`{"time":"` + util.LocalISO(fetchedAt.Add(time.Hour-time.Second)) + `","fiveHour":40,"sevenDay":9,"sevenDayFable":3}`,
		`{"time":"` + util.LocalISO(fetchedAt.Add(time.Hour)) + `","fiveHour":0,"sevenDay":0,"sevenDayFable":0}`,
	}
	if got := encode(t, monthly); got != encode(t, rawList(want)) {
		t.Errorf("monthly:\n got %s\nwant %s", got, encode(t, rawList(want)))
	}
}

func TestRecordMissedResetsWithoutRecent(t *testing.T) {
	resets, err := NewArchive(t.TempDir(), t.TempDir()).RecordMissedResets(missedEmail, time.Now())
	if err != nil || len(resets) != 0 {
		t.Errorf("resets %+v (%v)", resets, err)
	}
}

func rawList(texts []string) []any {
	list := make([]any, len(texts))
	for i, text := range texts {
		list[i] = rawJSON(text)
	}
	return list
}

type rawJSON string

func (r rawJSON) MarshalJSON() ([]byte, error) { return []byte(r), nil }
