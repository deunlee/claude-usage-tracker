package util

import (
	"bytes"
	"encoding/json"
	"time"
)

// 날짜 경계(잔디의 하루, 로그와 아카이브 파일 이름)와 사람이 읽는 시각을 적을 시간대다.
// 서버가 UTC로 도는 리눅스여도 쓰는 사람의 날짜로 묶이도록 머신 시간대와 떼어 두고 config.json의 timezone으로 정한다.
var location = time.Local

// 설정을 읽은 직후 한 번만 부른다. 수집기와 대시보드가 돌기 시작한 뒤에 바꾸면 그 고루틴들과 경쟁한다.
func SetLocation(loc *time.Location) {
	location = loc
}

func Location() *time.Location {
	return location
}

func Local(t time.Time) time.Time {
	return t.In(location)
}

func LocalDay(t time.Time) string {
	return Local(t).Format("2006-01-02")
}

func LocalMonth(t time.Time) string {
	return Local(t).Format("2006-01")
}

// 사람이 바로 읽을 수 있도록 로컬 시각에 시간대 오프셋을 붙인다. 예: 2026-10-03T18:25:20+09:00
func LocalISO(t time.Time) string {
	return Local(t).Format("2006-01-02T15:04:05-07:00")
}

func Clock(t time.Time) string {
	return Local(t).Format("15:04:05")
}

// JavaScript의 Date.toISOString과 같은 형식이다. 대시보드와 TS 시절 아카이브가 이 형식을 읽는다.
func ISOString(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// 응답의 시각은 resets_at처럼 마이크로초까지 붙은 RFC 3339 문자열이다.
func ParseTime(text string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, text)
	return t, err == nil
}

// 로그와 핑거프린트에 JavaScript가 숫자를 문자열로 바꿀 때와 같은 모양(45, 9.5, 1e-7)으로 쓴다.
// encoding/json이 실수를 ES6 규칙대로 적는다.
func FormatNumber(n float64) string {
	encoded, _ := json.Marshal(n)
	return string(encoded)
}

func Str(value any) *string {
	if s, ok := value.(string); ok {
		return &s
	}
	return nil
}

func Num(value any) *float64 {
	if n, ok := value.(float64); ok {
		return &n
	}
	return nil
}

func Optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// JSON.stringify와 같게 <, >, &를 이스케이프하지 않는다. 아카이브에 HTML 오류 본문이 그대로 읽히게 남는다.
func MarshalJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
