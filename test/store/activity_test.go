//go:build separatetest

package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"claudeusage/internal/util"
)

func TestAddIncreasesCountsFromZeroAfterResetAndUntilNextDay(t *testing.T) {
	day := time.Date(2026, 10, 4, 0, 0, 0, 0, time.Local)
	at := func(h, m int) int64 {
		return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute).UnixMilli()
	}
	value := func(v float64) summaryValues { return summaryValues{{name: "sevenDay", value: &v}} }
	samples := []sample{
		// 3일은 범위 밖이라, 3일 마지막 기록에서 4일 첫 기록까지 늘어난 양은 버린다.
		{t: day.Add(-time.Minute).UnixMilli(), values: value(5)},
		// 4일: 30 → 40, 초기화 1초 전 40 → 초기화 0 → 3 → (null 건너뜀) → 7, 다음 날 첫 기록 8까지. 10 + 0 + 3 + 4 + 1 = 18.
		{t: at(1, 0), values: value(30)},
		{t: at(2, 0), values: value(40)},
		{t: at(3, 0) - 1000, values: value(40)},
		{t: at(3, 0), values: value(0)},
		{t: at(4, 0), values: value(3)},
		{t: at(5, 0), values: summaryValues{{name: "sevenDay"}}},
		{t: at(6, 0), values: value(7)},
		// 5일: 8 → 9, 다음 날 첫 기록 9까지. 1.
		{t: at(24, 30), values: value(8)},
		{t: at(25, 0), values: value(9)},
		// 6일: 기록은 하나뿐이지만 프로그램이 꺼져 있다 9일에 다시 기록했으므로, 그 사이에 늘어난 11이 6일에 들어간다.
		{t: at(48, 0), values: value(9)},
		// 9일: 마지막 날이라 다음 기록이 없다.
		{t: at(120, 0), values: value(20)},
		// through 뒤의 기록은 보지 않는다.
		{t: at(144, 0), values: value(50)},
	}
	got := map[string]float64{}
	last := addIncreases(got, samples, nil, "2026-10-04", "2026-10-09")
	want := map[string]float64{"2026-10-04": 18, "2026-10-05": 1, "2026-10-06": 11, "2026-10-09": 0}
	if encode(t, got) != encode(t, want) {
		t.Errorf("increases %s, want %s", encode(t, got), encode(t, want))
	}
	if last == nil || last.day != "2026-10-09" || last.value != 20 {
		t.Errorf("last %+v", last)
	}
}

func TestActivityCachesPastDaysAndRecountsToday(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "someone@example.com")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.Local)
	at := func(d time.Duration) string { return util.LocalISO(now.Add(-d)) }
	// 3개월 하루 전(7월 5일) 기록은 범위 밖이라 세지 않는다.
	writeMonth(t, dir, time.Date(2026, 7, 5, 12, 0, 0, 0, time.Local), `[
{"time":"`+util.LocalISO(time.Date(2026, 7, 5, 10, 0, 0, 0, time.Local))+`","fiveHour":0,"sevenDay":1},
{"time":"`+util.LocalISO(time.Date(2026, 7, 5, 11, 0, 0, 0, time.Local))+`","fiveHour":0,"sevenDay":9}
]`)
	// 5일 11시 뒤로 프로그램이 꺼져 있다가 6일 10시에 다시 기록했다. 그 사이에 늘어난 5는 5일에 들어간다.
	writeMonth(t, dir, now, `[
{"time":"`+at(26*time.Hour)+`","fiveHour":0,"sevenDay":10},
{"time":"`+at(25*time.Hour)+`","fiveHour":0,"sevenDay":15},
{"time":"`+at(2*time.Hour)+`","fiveHour":0,"sevenDay":20},
{"time":"`+at(time.Hour)+`","fiveHour":0,"sevenDay":23}
]`)
	index := NewIndex(root, testMinBucket)
	index.WarmActivity(now, []string{"someone@example.com"})

	// 어제까지는 캐시에서, 오늘은 요청 때 파일을 다시 읽어 센다. 캐시를 썼는지 보려고 어제 첫 기록을 바꿔 둔다.
	writeMonth(t, dir, now, `[
{"time":"`+at(26*time.Hour)+`","fiveHour":0,"sevenDay":5},
{"time":"`+at(25*time.Hour)+`","fiveHour":0,"sevenDay":15},
{"time":"`+at(2*time.Hour)+`","fiveHour":0,"sevenDay":20},
{"time":"`+at(time.Hour)+`","fiveHour":0,"sevenDay":23},
{"time":"`+at(time.Minute)+`","fiveHour":0,"sevenDay":25}
]`)
	activity, err := index.Activity(now, "someone@example.com")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"from":"2026-07-06","to":"2026-10-06","days":{"2026-10-05":10,"2026-10-06":5}}`
	if got := encode(t, activity); got != want {
		t.Errorf("activity %s, want %s", got, want)
	}

	// 날이 바뀌면 어제까지를 다시 센다.
	activity, err = index.Activity(now.Add(24*time.Hour), "someone@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got := encode(t, activity.Days); got != `{"2026-10-05":15,"2026-10-06":5}` {
		t.Errorf("next day %s", got)
	}
}

func TestActivityWithoutEmailIsEmpty(t *testing.T) {
	activity, err := NewIndex(t.TempDir(), testMinBucket).Activity(time.Date(2026, 10, 6, 12, 0, 0, 0, time.Local), "")
	if err != nil || encode(t, activity) != `{"from":"2026-07-06","to":"2026-10-06","days":{}}` {
		t.Errorf("activity %s (%v)", encode(t, activity), err)
	}
}
