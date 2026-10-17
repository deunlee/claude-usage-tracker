//go:build separatetest

package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claudeusage/internal/claude"
	"claudeusage/internal/util"
)

func TestSummaryOfKeepsKeyOrder(t *testing.T) {
	s := snapshotOf(t, `{
		"seven_day": {"utilization": 9},
		"limits": [{"kind": "weekly_scoped", "percent": 1.5, "scope": {"model": {"display_name": "Fable"}}}]
	}`)
	at := time.Date(2026, 10, 4, 17, 40, 47, 0, time.Local)
	got := encode(t, SummaryOf(s, at))
	want := `{"time":"` + util.LocalISO(at) + `","fiveHour":null,"sevenDay":9,"sevenDayFable":1.5}`
	if got != want {
		t.Errorf("summary:\n got %s\nwant %s", got, want)
	}
}

func TestArchiveRecordJSON(t *testing.T) {
	status := 429
	record := Record{
		FetchedAt: "2026-10-04T08:40:47.039Z", HTTPStatus: &status, CLIVersion: "2.1.288", NextPollInMs: 120000,
		Error: "HTTP 429", RetryAfter: json.RawMessage("null"), ResponseBody: json.RawMessage(`"<html>"`),
	}
	want := `{"fetchedAt":"2026-10-04T08:40:47.039Z","ok":false,"httpStatus":429,"durationMs":null,"cliVersion":"2.1.288","nextPollInMs":120000,"error":"HTTP 429","retryAfter":null,"responseBody":"<html>"}`
	if got := encode(t, record); got != want {
		t.Errorf("record:\n got %s\nwant %s", got, want)
	}
}

func TestAccountArchiveDirRejectsUnsafeNames(t *testing.T) {
	for _, email := range []string{"", ".", "..", "a/b", `a\b`, "a:b", "trailing.", "trailing ", "tab\there"} {
		if _, err := accountArchiveDir("root", email); err == nil {
			t.Errorf("accepted %q", email)
		}
	}
	if dir, err := accountArchiveDir("root", "someone@example.com"); err != nil || dir != filepath.Join("root", "someone@example.com") {
		t.Errorf("rejected a normal email: %v", err)
	}
}

func TestAppenderWritesOneRecordPerLineAndRereadsExternalEdits(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.json")
	a := jsonArrayAppender{dir: dir}
	if err := a.append("a.json", json.RawMessage(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := a.append("a.json", json.RawMessage(`{"n":2}`)); err != nil {
		t.Fatal(err)
	}
	assertFile(t, file, "[\n{\"n\":1},\n{\"n\":2}\n]\n")

	if err := os.WriteFile(file, []byte("[\n  {\"n\": 0,  \"keep\": \"order\"}\n]"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := a.append("a.json", json.RawMessage(`{"n":3}`)); err != nil {
		t.Fatal(err)
	}
	assertFile(t, file, "[\n{\"n\":0,\"keep\":\"order\"},\n{\"n\":3}\n]\n")
}

func TestAppenderMovesCorruptFileAside(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.json")
	if err := os.WriteFile(file, []byte(`{"not": "an array"}`), 0o666); err != nil {
		t.Fatal(err)
	}
	a := jsonArrayAppender{dir: dir}
	if err := a.append("a.json", json.RawMessage(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	assertFile(t, file, "[\n{\"n\":1}\n]\n")
	entries, _ := os.ReadDir(dir)
	corrupt := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "a.json.corrupt-") {
			corrupt++
		}
	}
	if corrupt != 1 {
		t.Errorf("found %d corrupt copies in %v", corrupt, entries)
	}
}

func TestUsageArchiveAppendsLogRecentAndMonthly(t *testing.T) {
	root, logDir := t.TempDir(), t.TempDir()
	archive := NewArchive(root, logDir)
	at := time.Date(2026, 10, 4, 17, 40, 47, 0, time.Local)
	summary := Summary{time: util.LocalISO(at), values: summaryValues{{name: "fiveHour"}, {name: "sevenDay"}}}
	if err := archive.Append("someone@example.com", at, Record{FetchedAt: util.ISOString(at), OK: true}, &summary); err != nil {
		t.Fatal(err)
	}
	if err := archive.Append("someone@example.com", at, Record{FetchedAt: util.ISOString(at)}, nil); err != nil {
		t.Fatal(err)
	}
	logged, _ := readJSONArray(filepath.Join(logDir, "2026-10-04-someone@example.com.json"))
	recent, _ := readJSONArray(filepath.Join(root, "someone@example.com", "recent.json"))
	monthly, _ := readJSONArray(filepath.Join(root, "someone@example.com", "2026-10.json"))
	if len(logged) != 2 || len(recent) != 1 || len(monthly) != 1 {
		t.Errorf("log %d records, recent %d records, monthly %d records", len(logged), len(recent), len(monthly))
	}
}

func TestAppenderKeepsOnlyLastLimitRecords(t *testing.T) {
	dir := t.TempDir()
	a := jsonArrayAppender{dir: dir, limit: 2}
	for n := 1; n <= 3; n++ {
		if err := a.append("a.json", json.RawMessage(fmt.Sprintf(`{"n":%d}`, n))); err != nil {
			t.Fatal(err)
		}
	}
	assertFile(t, filepath.Join(dir, "a.json"), "[\n{\"n\":2},\n{\"n\":3}\n]\n")
}

func assertFile(t *testing.T, file, want string) {
	t.Helper()
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("%s:\n got %q\nwant %q", file, got, want)
	}
}

func snapshotOf(t *testing.T, body string) *claude.Snapshot {
	t.Helper()
	var data any
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		t.Fatal(err)
	}
	return claude.Classify(&claude.Response{Status: 200, Body: data}).Snapshot
}

func encode(t *testing.T, value any) string {
	t.Helper()
	encoded, err := util.MarshalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
