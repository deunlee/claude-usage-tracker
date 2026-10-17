//go:build separatetest

package poller

import (
	"testing"
	"time"
)

func TestAdaptiveScheduleSlowsDownAndSnapsBack(t *testing.T) {
	var s adaptiveSchedule
	if _, ok := s.observe("a", "t0"); ok {
		t.Fatal("the first observation has nothing to compare with")
	}
	for i := 1; i <= 2; i++ {
		if _, ok := s.observe("a", "t"); ok {
			t.Fatalf("slowed down after %d unchanged polls", i)
		}
	}
	change, ok := s.observe("a", "t3")
	if !ok || change.from != 2*time.Minute || change.to != 5*time.Minute || change.changed {
		t.Fatalf("third unchanged poll: %+v %v", change, ok)
	}
	for range 5 {
		s.observe("a", "t")
	}
	if change, ok := s.observe("a", "t"); !ok || change.to != 10*time.Minute {
		t.Fatalf("sixth unchanged poll at 5 minutes: %+v %v", change, ok)
	}
	for range 20 {
		s.observe("a", "t")
	}
	if s.interval() != 10*time.Minute || s.status().UnchangedPollsToSlowDown != nil {
		t.Fatalf("the last step should stay: %+v", s.status())
	}
	change, ok = s.observe("b", "changed-at")
	if !ok || !change.changed || change.to != BaseInterval || *s.status().LastChangeAt != "changed-at" {
		t.Fatalf("a change should snap back: %+v %v %+v", change, ok, s.status())
	}
}

func TestAdaptiveScheduleResetToBase(t *testing.T) {
	var s adaptiveSchedule
	if _, ok := s.resetToBase(); ok {
		t.Fatal("already at the base interval")
	}
	s.step = 2
	if change, ok := s.resetToBase(); !ok || change.from != 10*time.Minute || change.to != BaseInterval {
		t.Fatalf("resetToBase: %+v %v", change, ok)
	}
}
