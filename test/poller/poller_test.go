//go:build separatetest

package poller

import (
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"claudeusage/internal/claude"
	"claudeusage/internal/store"
	"claudeusage/internal/util"
)

const testEmail = "someone@example.com"

func pollerAgainst(t *testing.T, handler http.HandlerFunc) (*Poller, string) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	configDir := t.TempDir()
	files := map[string]string{
		".credentials.json": `{"claudeAiOauth":{"accessToken":"sk-test","expiresAt":4102444800000,"scopes":["user:profile","user:inference"],"subscriptionType":"max"}}`,
		".claude.json":      `{"oauthAccount":{"emailAddress":"` + testEmail + `"}}`,
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte(text), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	client := claude.NewClient()
	client.URL = server.URL + "/api/oauth/usage"
	client.RootCAs = server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	root := t.TempDir()
	archive := store.NewArchive(filepath.Join(root, "archive"), filepath.Join(root, "logs"))
	return New(claude.ExplicitConfigDir(configDir, t.TempDir()), client, archive, &claude.TokenRefresher{}, nil), root
}

// 대시보드 요청이 수집 도중에 상태를 읽는 상황을 -race로 잡으려고, 수집하는 동안 계속 읽는다.
func runOnePoll(t *testing.T, poller *Poller) Status {
	t.Helper()
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				poller.Status()
				poller.LastSnapshot()
			}
		}
	}()
	defer close(done)
	poller.Start()
	deadline := time.Now().Add(5 * time.Second)
	for poller.Status().NextPollAt == nil {
		if time.Now().After(deadline) {
			t.Fatal("the first poll did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	poller.Stop()
	return poller.Status()
}

func TestPollerRecordsSuccessfulPoll(t *testing.T) {
	poller, root := pollerAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		writer.Write([]byte(`{"five_hour":{"utilization":45,"resets_at":"2099-01-01T00:00:00Z"},"seven_day":{"utilization":9,"resets_at":null},` +
			`"seven_day_breakdown":{"as_of":null,"rows":[{"key":"chat","display_name":"Chats","percent":100}]}}`))
		writer.Close()
	})
	status := runOnePoll(t, poller)

	if *status.LastOutcome != claude.OutcomeOK || *status.Email != testEmail || status.LastSuccessAt == nil || *status.Token.SubscriptionType != "max" {
		t.Errorf("status %s", encode(t, status))
	}
	if latest := poller.LastSnapshot(); latest == nil || latest.Snapshot.Summary() != "5H: 45%, 7D: 9%" || len(latest.Snapshot.Breakdown().Rows) != 1 {
		t.Errorf("latest %+v", latest)
	}
	at, _ := time.Parse(time.RFC3339, *status.LastSuccessAt)
	daily, err := readRecords(filepath.Join(root, "logs", logFileName(at)))
	if err != nil || len(daily) != 1 {
		t.Fatalf("daily %v %s", err, daily)
	}
	var record map[string]any
	if err := json.Unmarshal(daily[0], &record); err != nil {
		t.Fatal(err)
	}
	if record["ok"] != true || record["httpStatus"] != 200.0 || record["nextPollInMs"] != 120000.0 || record["data"].(map[string]any)["five_hour"] == nil {
		t.Errorf("daily record %s", daily[0])
	}
	recent, err := readRecords(filepath.Join(root, "archive", testEmail, "recent.json"))
	if err != nil || len(recent) != 1 || string(recent[0]) != string(daily[0]) {
		t.Errorf("recent %v %s", err, recent)
	}
	monthly, err := readRecords(filepath.Join(root, "archive", testEmail, monthlyFileName(at)))
	if err != nil || len(monthly) != 1 {
		t.Fatalf("monthly %v %s", err, monthly)
	}
	if want := `{"time":"` + util.LocalISO(at) + `","fiveHour":45,"sevenDay":9}`; string(monthly[0]) != want {
		t.Errorf("monthly %s, want %s", monthly[0], want)
	}
}

func TestPollerPausesOnRateLimit(t *testing.T) {
	poller, root := pollerAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"type":"rate_limit_error"}}`))
	})
	status := runOnePoll(t, poller)

	if *status.LastOutcome != claude.OutcomeRateLimited || *status.PauseReason != pauseRateLimited || status.PausedUntil == nil {
		t.Errorf("status %s", encode(t, status))
	}
	next, _ := time.Parse(time.RFC3339, *status.NextPollAt)
	if until, _ := time.Parse(time.RFC3339, *status.PausedUntil); next.Before(until) {
		t.Errorf("next poll %v comes before the pause ends %v", next, until)
	}
	daily, _ := readRecords(filepath.Join(root, "logs", logFileName(time.Now())))
	var record map[string]any
	if len(daily) != 1 || json.Unmarshal(daily[0], &record) != nil || record["retryAfter"] != "120" || record["error"] != "HTTP 429 rate_limit_error" {
		t.Errorf("daily %s", daily)
	}
	for _, name := range []string{"recent.json", monthlyFileName(time.Now())} {
		if _, err := os.Stat(filepath.Join(root, "archive", testEmail, name)); !os.IsNotExist(err) {
			t.Errorf("a failed poll should not add to %s: %v", name, err)
		}
	}
}

func TestPollerWaitsLongerAtLimit(t *testing.T) {
	poller, _ := pollerAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"five_hour":{"utilization":100,"resets_at":"2099-01-01T00:00:00Z"},"seven_day":{"utilization":40,"resets_at":"2099-01-07T00:00:00Z"}}`))
	})
	status := runOnePoll(t, poller)

	attempt, _ := time.Parse(time.RFC3339, *status.LastAttemptAt)
	next, _ := time.Parse(time.RFC3339, *status.NextPollAt)
	if !status.LimitReached || status.IntervalMs != (30*time.Minute).Milliseconds() {
		t.Errorf("status %s", encode(t, status))
	}
	if gap := next.Sub(attempt); gap < 30*time.Minute-time.Second || gap > 30*time.Minute+time.Second {
		t.Errorf("next poll %v after the last attempt, want about 30m", gap)
	}
}

func TestRefreshRequestBringsNextPollForward(t *testing.T) {
	poller, _ := pollerAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"five_hour":{"utilization":45,"resets_at":"2099-01-01T00:00:00Z"},"seven_day":{"utilization":9,"resets_at":null}}`))
	})
	poller.schedule.step = len(scheduleSteps) - 1
	poller.Start()
	defer poller.Stop()
	deadline := time.Now().Add(5 * time.Second)
	for poller.Status().NextPollAt == nil {
		if time.Now().After(deadline) {
			t.Fatal("the first poll did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-poller.RequestRefresh():
	case <-time.After(5 * time.Second):
		t.Fatal("the refresh request was not handled")
	}
	status := poller.Status()
	attempt, _ := time.Parse(time.RFC3339, *status.LastAttemptAt)
	next, _ := time.Parse(time.RFC3339, *status.NextPollAt)
	if gap := next.Sub(attempt); gap < BaseInterval-time.Second || gap > BaseInterval+time.Second {
		t.Errorf("next poll %v after the last attempt, want about %v", gap, BaseInterval)
	}
	if status.Schedule.IntervalMs != BaseInterval.Milliseconds() {
		t.Errorf("schedule %+v", status.Schedule)
	}
}

func TestPollerShowsLastSavedResponseWhenFirstPollFails(t *testing.T) {
	poller, root := pollerAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"type":"rate_limit_error"}}`))
	})
	dir := filepath.Join(root, "archive", testEmail)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	recent := `[
{"fetchedAt":"2026-10-05T14:30:16.183Z","ok":true,"data":{"five_hour":{"utilization":61,"resets_at":"2099-01-01T00:00:00Z"},"seven_day":{"utilization":33,"resets_at":"2099-01-07T00:00:00Z"}}}
]`
	if err := os.WriteFile(filepath.Join(dir, "recent.json"), []byte(recent), 0o666); err != nil {
		t.Fatal(err)
	}
	status := runOnePoll(t, poller)

	latest := poller.LastSnapshot()
	if *status.LastOutcome != claude.OutcomeRateLimited || latest == nil ||
		latest.FetchedAt != "2026-10-05T14:30:16.183Z" || latest.Snapshot.Summary() != "5H: 61%, 7D: 33%" {
		t.Errorf("status %s, latest %+v", encode(t, status), latest)
	}
	if status.LastSuccessAt != nil {
		t.Errorf("a restored response is not a success of this run: %s", *status.LastSuccessAt)
	}
}

// 아카이브 파일 이름과 형식은 대시보드·사람과의 약속이라, store의 구현을 빌리지 않고 여기 그대로 적어 확인한다.
func logFileName(at time.Time) string {
	return util.LocalDay(at) + "-" + testEmail + ".json"
}

func monthlyFileName(at time.Time) string {
	return util.LocalMonth(at) + ".json"
}

func readRecords(file string) ([]json.RawMessage, error) {
	text, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var records []json.RawMessage
	err = json.Unmarshal(text, &records)
	return records, err
}

func encode(t *testing.T, value any) string {
	t.Helper()
	encoded, err := util.MarshalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestLogNamePrefersAlias(t *testing.T) {
	configDir := claude.ExplicitConfigDir(t.TempDir(), t.TempDir())
	p := New(configDir, claude.NewClient(), store.NewArchive(t.TempDir(), t.TempDir()), &claude.TokenRefresher{}, claude.AccountAliases{"me@example.com": "계정1"})

	if got := p.logName(); got != configDir.Dir {
		t.Errorf("before email: %q, want %q", got, configDir.Dir)
	}
	p.email = "Me@Example.com"
	if got := p.logName(); got != "계정1" {
		t.Errorf("aliased: %q", got)
	}
	p.email = "other@example.com"
	if got := p.logName(); got != "other@example.com" {
		t.Errorf("unaliased: %q", got)
	}
}
