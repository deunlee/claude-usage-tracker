package store

import (
	"maps"
	"path/filepath"
	"sync"
	"time"

	"claudeusage/internal/util"
)

const activityMonths = 3

// 대시보드 잔디: 날짜(YYYY-MM-DD, 로컬)마다 그날 늘어난 주간(모든 모델) 사용률.
// 기록이 하나도 없는 날은 Days에 없다.
type Activity struct {
	From string             `json:"from"`
	To   string             `json:"to"`
	Days map[string]float64 `json:"days"`
}

// 지난 날의 기록은 더 바뀌지 않으므로 어제까지는 한 번 집계해 두고, 요청마다 오늘만 다시 센다.
type activityCache struct {
	// 이 캐시를 만든 날. 날이 바뀌면 그날이 '어제까지'에 들어와야 하므로 다시 만든다.
	today string
	past  map[string]float64
	// 어제까지의 마지막 기록. 그 기록에서 오늘 첫 기록까지 늘어난 양은 그 기록의 날에 들어간다.
	last *weeklyReading
}

type weeklyReading struct {
	day   string
	value float64
}

type activityCaches struct {
	mu       sync.Mutex
	accounts map[string]activityCache
}

// 프로그램을 시작할 때 계정마다 어제까지를 미리 집계해, 첫 대시보드 요청이 몇 달 치를 읽느라 늦지 않게 한다.
func (x *Index) WarmActivity(now time.Time, emails []string) {
	for _, email := range emails {
		if _, err := x.Activity(now, email); err != nil {
			util.Logf("%s의 일별 사용량을 집계하지 못했습니다: %v", email, err)
		}
	}
}

func (x *Index) Activity(now time.Time, email string) (Activity, error) {
	today := util.LocalDay(now)
	from := util.LocalDay(now.AddDate(0, -activityMonths, 0))
	activity := Activity{From: from, To: today, Days: map[string]float64{}}
	if email == "" {
		return activity, nil
	}
	dir, err := accountArchiveDir(x.root, email)
	if err != nil {
		return Activity{}, err
	}

	x.activity.mu.Lock()
	defer x.activity.mu.Unlock()
	if x.activity.accounts == nil {
		x.activity.accounts = map[string]activityCache{}
	}
	cache, ok := x.activity.accounts[email]
	if !ok || cache.today != today {
		var samples []sample
		for _, name := range monthFilesCovering(now.AddDate(0, -activityMonths, 0), now) {
			samples = append(samples, x.cached(filepath.Join(dir, name))...)
		}
		cache = activityCache{today: today, past: map[string]float64{}}
		cache.last = addIncreases(cache.past, samples, nil, from, util.LocalDay(now.AddDate(0, 0, -1)))
		x.activity.accounts[email] = cache
	}
	maps.Copy(activity.Days, cache.past)
	month := x.cached(filepath.Join(dir, monthlyFileName(now)))
	local := util.Local(now)
	startOfToday := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, util.Location())
	addIncreases(activity.Days, month[firstIndexAtOrAfter(month, startOfToday.UnixMilli()):], cache.last, from, today)
	return activity, nil
}

// 기록을 차례로 보며 주간 사용률이 늘어난 양을 앞 기록의 날에 더하고, 마지막 기록을 돌려준다.
// 그래서 하루는 그날 첫 기록부터 다음 날 첫 기록까지이고, 프로그램이 자정을 넘겨 꺼져 있던 동안 쓴 양은 꺼지기 전 날에 들어간다.
// 주간 창은 초기화될 때만 값이 내려가므로, 내려가면 초기화 뒤에 새로 쓴 양이 그 값이라 0부터 센다.
// samples는 시각 순이어야 하고, through 뒤의 기록은 보지 않는다. from 앞의 날에 들어갈 양은 버린다.
func addIncreases(increases map[string]float64, samples []sample, previous *weeklyReading, from, through string) *weeklyReading {
	for _, s := range samples {
		// 창 필드가 없던 기록(null)은 0으로 보면 그 앞뒤가 초기화처럼 읽혀 사용량이 부풀므로 건너뛴다.
		value := s.values.get("sevenDay")
		if value == nil {
			continue
		}
		day := util.LocalDay(time.UnixMilli(s.t))
		if day > through {
			break
		}
		if day >= from {
			increases[day] += 0
		}
		if previous != nil && previous.day >= from {
			increase := *value - previous.value
			if increase < 0 {
				increase = *value
			}
			increases[previous.day] += increase
		}
		previous = &weeklyReading{day: day, value: *value}
	}
	return previous
}
