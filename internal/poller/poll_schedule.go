package poller

import (
	"time"

	"claudeusage/internal/util"
)

type scheduleStep struct {
	interval time.Duration
	// 이 단계에서 변화 없는 응답이 이만큼 이어지면 다음 단계로 늦춘다. 0이면 마지막 단계다.
	slowDownAfter int
}

const BaseInterval = 2 * time.Minute

// 사용량이 멈춰 있는 동안은 요청을 줄이고, 다시 움직이면 곧바로 촘촘하게 본다.
// 1분 간격으로 보내면 너덧 번째 요청마다 429가 돌아와(2026-10-03 관측) 가장 촘촘한 주기를 2분으로 둔다.
var scheduleSteps = []scheduleStep{
	{interval: BaseInterval, slowDownAfter: 3},
	{interval: 5 * time.Minute, slowDownAfter: 6},
	{interval: 10 * time.Minute},
}

type scheduleChange struct {
	from, to time.Duration
	changed  bool
}

type scheduleStatus struct {
	IntervalMs               int64   `json:"intervalMs"`
	BaseIntervalMs           int64   `json:"baseIntervalMs"`
	UnchangedPolls           int     `json:"unchangedPolls"`
	UnchangedPollsToSlowDown *int    `json:"unchangedPollsToSlowDown"`
	LastChangeAt             *string `json:"lastChangeAt"`
}

type adaptiveSchedule struct {
	step           int
	unchangedPolls int
	observed       bool
	fingerprint    string
	lastChangeAt   string
}

func (s *adaptiveSchedule) interval() time.Duration {
	return scheduleSteps[s.step].interval
}

// 초기화 직후에는 값이 크게 바뀌므로, 초기화 직전 기록부터 가장 촘촘한 주기로 되돌려 둔다.
func (s *adaptiveSchedule) resetToBase() (scheduleChange, bool) {
	before := s.interval()
	s.step = 0
	s.unchangedPolls = 0
	return scheduleChange{from: before, to: s.interval()}, before != s.interval()
}

func (s *adaptiveSchedule) observe(fingerprint, at string) (scheduleChange, bool) {
	previous, observed := s.fingerprint, s.observed
	s.fingerprint, s.observed = fingerprint, true
	if !observed {
		s.lastChangeAt = at
		return scheduleChange{}, false
	}
	before := s.interval()
	changed := fingerprint != previous
	if changed {
		s.step = 0
		s.unchangedPolls = 0
		s.lastChangeAt = at
	} else {
		s.unchangedPolls++
		if limit := scheduleSteps[s.step].slowDownAfter; limit > 0 && s.unchangedPolls >= limit {
			s.step++
			s.unchangedPolls = 0
		}
	}
	return scheduleChange{from: before, to: s.interval(), changed: changed}, before != s.interval()
}

func (s *adaptiveSchedule) status() scheduleStatus {
	status := scheduleStatus{
		IntervalMs:     s.interval().Milliseconds(),
		BaseIntervalMs: BaseInterval.Milliseconds(),
		UnchangedPolls: s.unchangedPolls,
		LastChangeAt:   util.Optional(s.lastChangeAt),
	}
	if limit := scheduleSteps[s.step].slowDownAfter; limit > 0 {
		status.UnchangedPollsToSlowDown = &limit
	}
	return status
}
