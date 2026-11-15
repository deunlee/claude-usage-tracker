//go:build separatetest

package claude

import (
	"encoding/json"
	"testing"
	"time"

	"claudeusage/internal/util"
)

func snapshotOf(t *testing.T, body string) *Snapshot {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		t.Fatal(err)
	}
	return &Snapshot{data: data, Raw: json.RawMessage(body)}
}

func encode(t *testing.T, value any) string {
	t.Helper()
	encoded, err := util.MarshalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestReadingsPreferWindowFieldsAndTakeSeverityFromLimits(t *testing.T) {
	s := snapshotOf(t, `{
		"five_hour": {"utilization": 45, "resets_at": "2026-10-04T13:00:00.465302+00:00"},
		"seven_day": {"utilization": 9, "resets_at": "2026-10-10T05:00:00.465327+00:00"},
		"seven_day_opus": null,
		"extra_usage": {"is_enabled": false, "utilization": null},
		"limits": [
			{"kind": "session", "percent": 40, "severity": "normal", "resets_at": "2026-10-04T13:00:00+00:00", "scope": null},
			{"kind": "weekly_all", "percent": 9, "severity": "warning", "resets_at": "2026-10-10T05:00:00+00:00", "scope": null}
		]
	}`)
	want := `[{"key":"session","kind":"session","label":"5시간 세션","shortLabel":"5H","percent":45,"resetsAt":"2026-10-04T13:00:00.465302+00:00","severity":"normal","idle":false,"field":"fiveHour"},` +
		`{"key":"weekly_all","kind":"weekly_all","label":"이번 주 (모든 모델)","shortLabel":"7D","percent":9,"resetsAt":"2026-10-10T05:00:00.465327+00:00","severity":"warning","idle":false,"field":"sevenDay"}]`
	if got := encode(t, s.Readings()); got != want {
		t.Errorf("readings:\n got %s\nwant %s", got, want)
	}
	if got := s.Summary(); got != "5H: 45%, 7D: 9%" {
		t.Errorf("summary = %q", got)
	}
}

func TestReadingsScopedLimitsReplaceLegacyFields(t *testing.T) {
	s := snapshotOf(t, `{
		"five_hour": null,
		"seven_day_opus": {"utilization": 70, "resets_at": null},
		"limits": [
			{"kind": "weekly_scoped", "percent": 12.5, "resets_at": null, "scope": {"model": {"display_name": "Fable 5.1"}}},
			{"kind": "weekly_scoped", "percent": 3, "scope": {"model": {"display_name": "Fable"}, "surface": {"display_name": "Code"}}}
		]
	}`)
	readings := s.Readings()
	if len(readings) != 2 {
		t.Fatalf("readings = %s", encode(t, readings))
	}
	scoped := readings[0]
	if scoped.Key != "weekly_scoped:model=Fable 5.1" || scoped.Label != "이번 주 (Fable 5.1)" || scoped.ShortLabel != "Fable 5.1" ||
		scoped.Field == nil || *scoped.Field != "sevenDayFable51" || scoped.Idle {
		t.Errorf("scoped reading: %s", encode(t, scoped))
	}
	surface := readings[1]
	if surface.Key != "weekly_scoped:model=Fable:surface=Code" || surface.Label != "이번 주 (Fable · Code)" || surface.Field != nil {
		t.Errorf("surface reading: %s", encode(t, surface))
	}
}

func TestReadingsOmitFiveHourWhenPlanHasNone(t *testing.T) {
	s := snapshotOf(t, `{
		"five_hour": null,
		"seven_day": {"utilization": 91.0, "resets_at": "2026-11-18T19:00:00.546430+00:00"},
		"limits": [
			{"kind": "weekly_all", "group": "weekly", "percent": 91, "severity": "critical", "resets_at": "2026-11-18T19:00:00.546430+00:00", "scope": null, "is_active": false},
			{"kind": "weekly_scoped", "group": "weekly", "percent": 100, "severity": "critical", "resets_at": "2026-11-18T18:59:59.546539+00:00",
				"scope": {"model": {"id": null, "display_name": "Fable"}, "surface": null}, "is_active": true}
		]
	}`)
	got := ""
	for _, r := range s.Readings() {
		got += r.Key + "|"
	}
	if want := "weekly_all|weekly_scoped:model=Fable|"; got != want {
		t.Errorf("keys = %s, want %s", got, want)
	}
	if got := s.Summary(); got != "7D: 91%, Fable: 100%" {
		t.Errorf("summary = %q", got)
	}
}

func TestReadingsTreatNullFiveHourAsIdleWithoutLimits(t *testing.T) {
	readings := snapshotOf(t, `{"five_hour": null, "seven_day": {"utilization": 9, "resets_at": null}}`).Readings()
	if len(readings) != 2 {
		t.Fatalf("readings = %s", encode(t, readings))
	}
	if session := readings[0]; session.Kind != "session" || !session.Idle || session.Percent != 0 {
		t.Errorf("null five_hour should be an idle 0%% session: %s", encode(t, session))
	}
}

func TestReadingsFallBackToLegacyScopedAndExtraUsage(t *testing.T) {
	s := snapshotOf(t, `{
		"seven_day": {"utilization": 20, "resets_at": "2026-10-10T05:00:00Z"},
		"seven_day_sonnet": {"utilization": 0, "resets_at": null},
		"seven_day_opus": {"utilization": 30, "resets_at": "2026-10-10T05:00:00Z"},
		"extra_usage": {"is_enabled": true, "utilization": 15}
	}`)
	got := ""
	for _, r := range s.Readings() {
		got += r.Key + "|"
	}
	if want := "weekly_all|weekly_scoped:model=Sonnet|weekly_scoped:model=Opus|extra_usage|"; got != want {
		t.Errorf("keys = %s, want %s", got, want)
	}
	if got := s.Summary(); got != "7D: 20%, Sonnet: 0% (시작 전), Opus: 30%, Extra: 15%" {
		t.Errorf("summary = %q", got)
	}
}

func TestReachedLimitsIgnoresWindowsAlreadyReset(t *testing.T) {
	s := snapshotOf(t, `{
		"five_hour": {"utilization": 100, "resets_at": "2026-10-04T13:00:00Z"},
		"seven_day": {"utilization": 100, "resets_at": null}
	}`)
	before := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	after := time.Date(2026, 10, 4, 13, 0, 0, 0, time.UTC)
	if n := len(s.ReachedLimits(before)); n != 2 {
		t.Errorf("before reset: %d reached, want 2", n)
	}
	if n := len(s.ReachedLimits(after)); n != 1 {
		t.Errorf("at reset: %d reached, want 1", n)
	}
}

func TestFingerprintIgnoresResetMicroseconds(t *testing.T) {
	a := snapshotOf(t, `{"five_hour": {"utilization": 45, "resets_at": "2026-10-04T13:00:00.465302+00:00"}, "seven_day": {"utilization": 9.5}}`)
	b := snapshotOf(t, `{"five_hour": {"utilization": 45, "resets_at": "2026-10-04T12:59:59.936888+00:00"}, "seven_day": {"utilization": 9.5}}`)
	if a.Fingerprint() != b.Fingerprint() || a.Fingerprint() != "session=45|weekly_all=9.5" {
		t.Errorf("fingerprints %q, %q", a.Fingerprint(), b.Fingerprint())
	}
}

func TestBreakdown(t *testing.T) {
	s := snapshotOf(t, `{"seven_day_breakdown": {"as_of": "2026-10-04T08:40:47Z", "rows": [
		{"key": "claude_code", "display_name": "Claude Code", "percent": 100},
		{"key": "chat", "percent": 0},
		{"display_name": "Other", "percent": null},
		"junk"
	]}}`)
	want := `{"asOf":"2026-10-04T08:40:47Z","rows":[{"key":"claude_code","label":"Claude Code","percent":100},{"key":"chat","label":"chat","percent":0}]}`
	if got := encode(t, s.Breakdown()); got != want {
		t.Errorf("breakdown:\n got %s\nwant %s", got, want)
	}
}

func TestIsUsageBody(t *testing.T) {
	for body, want := range map[string]bool{
		`{"limits": null}`:                        true,
		`{"error": {"type": "rate_limit_error"}}`: false,
		`[]`:          false,
		`"five_hour"`: false,
	} {
		var value any
		if err := json.Unmarshal([]byte(body), &value); err != nil {
			t.Fatal(err)
		}
		if got := isUsageBody(value); got != want {
			t.Errorf("isUsageBody(%s) = %v", body, got)
		}
	}
}
