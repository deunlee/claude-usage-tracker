package claude

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"claudeusage/internal/util"
)

type Reading struct {
	Key        string  `json:"key"`
	Kind       string  `json:"kind"`
	Label      string  `json:"label"`
	ShortLabel string  `json:"shortLabel"`
	Percent    float64 `json:"percent"`
	ResetsAt   *string `json:"resetsAt"`
	Severity   *string `json:"severity"`
	// 창이 아직 시작되지 않음: 첫 메시지를 보내기 전이라 사용량 자체가 없다.
	Idle bool `json:"idle"`
	// 월별 요약 파일에서 이 값이 들어가는 키. 요약에 넣지 않는 항목은 nil.
	Field *string `json:"field"`
}

type BreakdownRow struct {
	Key     *string `json:"key"`
	Label   string  `json:"label"`
	Percent float64 `json:"percent"`
}

type Breakdown struct {
	AsOf *string        `json:"asOf"`
	Rows []BreakdownRow `json:"rows"`
}

// Claude Code는 2xx 응답이라도 이 필드가 하나도 없으면 본문을 에러로 취급한다.
var usageFields = []string{"five_hour", "seven_day", "seven_day_oauth_apps", "seven_day_opus", "seven_day_sonnet", "cinder_cove", "extra_usage", "limits"}

// Claude Code의 /usage 화면은 세션·주간 줄을 limits가 아니라 이 두 필드로 그린다.
var primaryWindows = []struct{ kind, field string }{
	{"session", "five_hour"},
	{"weekly_all", "seven_day"},
}

// limits에 모델별 주간 창이 없던 응답 형식에서만 쓴다.
var legacyScopedWindows = []struct{ field, model string }{
	{"seven_day_sonnet", "Sonnet"},
	{"seven_day_opus", "Opus"},
}

func isPrimaryKind(kind any) bool {
	for _, window := range primaryWindows {
		if kind == window.kind {
			return true
		}
	}
	return false
}

func scopeName(scope any, part string) string {
	object, _ := scope.(map[string]any)
	value, _ := object[part].(map[string]any)
	name, _ := value["display_name"].(string)
	return name
}

func seriesKey(kind, model, surface string) string {
	key := kind
	if model != "" {
		key += ":model=" + model
	}
	if surface != "" {
		key += ":surface=" + surface
	}
	return key
}

func joinScope(model, surface string) string {
	parts := make([]string, 0, 2)
	for _, part := range []string{model, surface} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " · ")
}

func SeriesLabel(kind, model, surface string) string {
	scope := joinScope(model, surface)
	switch kind {
	case "session":
		return "5시간 세션"
	case "weekly_all":
		return "이번 주 (모든 모델)"
	case "weekly_scoped":
		if scope == "" {
			scope = "범위 지정"
		}
		return "이번 주 (" + scope + ")"
	default:
		if scope == "" {
			return kind
		}
		return kind + " (" + scope + ")"
	}
}

// 로그 한 줄에 모든 창을 몰아 쓰므로 짧게 줄인다.
func shortLabel(kind, model, surface string) string {
	switch kind {
	case "session":
		return "5H"
	case "weekly_all":
		return "7D"
	}
	if scope := joinScope(model, surface); kind == "weekly_scoped" && scope != "" {
		return scope
	}
	return SeriesLabel(kind, model, surface)
}

// 요약 파일 키: fiveHour, sevenDay, 모델별 주간은 sevenDay + 모델명의 영숫자(예: Fable → sevenDayFable, Fable 5.1 → sevenDayFable51).
// 요약에 넣지 않는 항목은 빈 문자열이다.
func summaryField(kind, model, surface string) string {
	switch {
	case surface != "":
		return ""
	case kind == "session" && model == "":
		return "fiveHour"
	case kind == "weekly_all" && model == "":
		return "sevenDay"
	case kind != "weekly_scoped" || model == "":
		return ""
	}
	words := strings.FieldsFunc(model, func(r rune) bool {
		return !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9')
	})
	if len(words) == 0 {
		return ""
	}
	var suffix strings.Builder
	for _, word := range words {
		suffix.WriteString(strings.ToUpper(word[:1]) + word[1:])
	}
	return "sevenDay" + suffix.String()
}

func newReading(kind, model, surface string, percent *float64, resetsAt, severity *string) Reading {
	r := Reading{
		Key:        seriesKey(kind, model, surface),
		Kind:       kind,
		Label:      SeriesLabel(kind, model, surface),
		ShortLabel: shortLabel(kind, model, surface),
		ResetsAt:   resetsAt,
		Severity:   severity,
		Idle:       percent == nil || (*percent == 0 && resetsAt == nil),
		Field:      util.Optional(summaryField(kind, model, surface)),
	}
	if percent != nil {
		r.Percent = *percent
	}
	return r
}

type Snapshot struct {
	data map[string]any
	// 아카이브에는 파싱한 값이 아니라 받은 그대로(공백만 뺀) 본문을 남긴다.
	Raw json.RawMessage
}

// 아직 응답을 한 번도 받지 못했을 때 미터가 비지 않도록 5시간·주간을 0%로 채운다.
func PlaceholderReadings() []Reading {
	readings := []Reading{}
	zero := 0.0
	for _, window := range primaryWindows {
		r := newReading(window.kind, "", "", &zero, nil, nil)
		r.Idle = false
		readings = append(readings, r)
	}
	return readings
}

func isUsageBody(body any) bool {
	object, ok := body.(map[string]any)
	if !ok {
		return false
	}
	for _, field := range usageFields {
		if _, present := object[field]; present {
			return true
		}
	}
	return false
}

func (s *Snapshot) Readings() []Reading {
	var limits []map[string]any
	if list, ok := s.data["limits"].([]any); ok {
		for _, item := range list {
			if limit, ok := item.(map[string]any); ok {
				limits = append(limits, limit)
			}
		}
	}
	readings := []Reading{}
	for _, window := range primaryWindows {
		var limit map[string]any
		for _, candidate := range limits {
			if candidate["kind"] == window.kind {
				limit = candidate
				break
			}
		}
		if r, ok := s.primaryReading(window.kind, window.field, limit); ok {
			readings = append(readings, r)
		}
	}
	hasScoped := false
	for _, limit := range limits {
		if isPrimaryKind(limit["kind"]) {
			continue
		}
		if limit["kind"] == "weekly_scoped" {
			hasScoped = true
		}
		if r, ok := limitReading(limit); ok {
			readings = append(readings, r)
		}
	}
	if !hasScoped {
		readings = append(readings, s.legacyScopedReadings()...)
	}

	if extra, ok := s.data["extra_usage"].(map[string]any); ok && extra["is_enabled"] == true {
		if percent := util.Num(extra["utilization"]); percent != nil {
			readings = append(readings, Reading{
				Key: "extra_usage", Kind: "extra_usage", Label: "추가 사용량 (월)", ShortLabel: "Extra", Percent: *percent,
			})
		}
	}
	return readings
}

func (s *Snapshot) Breakdown() *Breakdown {
	object, ok := s.data["seven_day_breakdown"].(map[string]any)
	if !ok {
		return nil
	}
	list, ok := object["rows"].([]any)
	if !ok {
		return nil
	}
	rows := []BreakdownRow{}
	for _, item := range list {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		percent := util.Num(row["percent"])
		if percent == nil {
			continue
		}
		key := util.Str(row["key"])
		label := "?"
		if name := util.Str(row["display_name"]); name != nil {
			label = *name
		} else if key != nil {
			label = *key
		}
		rows = append(rows, BreakdownRow{Key: key, Label: label, Percent: *percent})
	}
	return &Breakdown{AsOf: util.Str(object["as_of"]), Rows: rows}
}

// 수집 주기는 5시간·주간 사용률만 보고 정한다.
// 원본 응답은 resets_at의 마이크로초처럼 매번 달라지는 값이 있어, 그대로 비교하면 늘 '변화'로 잡힌다.
func (s *Snapshot) Fingerprint() string {
	var parts []string
	for _, r := range s.Readings() {
		if isPrimaryKind(r.Kind) {
			parts = append(parts, r.Key+"="+util.FormatNumber(r.Percent))
		}
	}
	slices.Sort(parts)
	return strings.Join(parts, "|")
}

// 한도에 닿은 5시간·주간(모든 모델) 창. 초기화 시각이 지난 창은 이미 풀렸으므로 뺀다.
func (s *Snapshot) ReachedLimits(at time.Time) []Reading {
	var reached []Reading
	for _, r := range s.Readings() {
		if !isPrimaryKind(r.Kind) || r.Percent < 100 {
			continue
		}
		if r.ResetsAt != nil {
			if resetAt, ok := util.ParseTime(*r.ResetsAt); ok && !resetAt.After(at) {
				continue
			}
		}
		reached = append(reached, r)
	}
	return reached
}

func (s *Snapshot) PrimaryResetTimes() []time.Time {
	var times []time.Time
	for _, r := range s.Readings() {
		if !isPrimaryKind(r.Kind) || r.ResetsAt == nil {
			continue
		}
		if t, ok := util.ParseTime(*r.ResetsAt); ok {
			times = append(times, t)
		}
	}
	return times
}

func (s *Snapshot) Summary() string {
	readings := s.Readings()
	if len(readings) == 0 {
		return "(사용량 항목 없음)"
	}
	parts := make([]string, len(readings))
	for i, r := range readings {
		parts[i] = r.ShortLabel + ": " + util.FormatNumber(r.Percent) + "%"
		if r.Idle {
			parts[i] += " (시작 전)"
		}
	}
	return strings.Join(parts, ", ")
}

func (s *Snapshot) primaryReading(kind, field string, limit map[string]any) (Reading, bool) {
	window, isWindow := s.data[field].(map[string]any)
	var percent *float64
	var resetsAt *string
	if isWindow {
		percent = util.Num(window["utilization"])
		resetsAt = util.Str(window["resets_at"])
	}
	if percent == nil && limit != nil {
		percent = util.Num(limit["percent"])
		resetsAt = util.Str(limit["resets_at"])
	}
	// 필드도 limits 항목도 없으면 이 플랜에는 그 창이 없다. 필드가 null이면 창이 아직 시작되지 않은 것이다.
	if _, present := s.data[field]; percent == nil && !present && limit == nil {
		return Reading{}, false
	}
	var severity *string
	if limit != nil {
		severity = util.Str(limit["severity"])
	}
	return newReading(kind, "", "", percent, resetsAt, severity), true
}

func limitReading(limit map[string]any) (Reading, bool) {
	kind := util.Str(limit["kind"])
	percent := util.Num(limit["percent"])
	if kind == nil || percent == nil {
		return Reading{}, false
	}
	model := scopeName(limit["scope"], "model")
	surface := scopeName(limit["scope"], "surface")
	return newReading(*kind, model, surface, percent, util.Str(limit["resets_at"]), util.Str(limit["severity"])), true
}

func (s *Snapshot) legacyScopedReadings() []Reading {
	var readings []Reading
	for _, legacy := range legacyScopedWindows {
		window, ok := s.data[legacy.field].(map[string]any)
		if !ok {
			continue
		}
		percent := util.Num(window["utilization"])
		if percent == nil {
			continue
		}
		readings = append(readings, newReading("weekly_scoped", legacy.model, "", percent, util.Str(window["resets_at"]), nil))
	}
	return readings
}
