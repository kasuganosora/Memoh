package chattiming

import (
	"testing"
	"time"
)

func TestIdleCompensation_ComputeCredit(t *testing.T) {
	ic := NewIdleCompensator(IdleCompensationConfig{
		MinIdleBeforeCredit: 5 * time.Second,
	})

	tests := []struct {
		idle       time.Duration
		creditRate time.Duration
		expected   int
	}{
		{3 * time.Second, 10 * time.Second, 0},  // below min idle
		{10 * time.Second, 10 * time.Second, 1}, // exactly 1 credit
		{25 * time.Second, 10 * time.Second, 2}, // 2.5 → 2
		{60 * time.Second, 10 * time.Second, 6}, // 6 credits
		{30 * time.Second, 0, 0},                // zero credit rate
	}

	for _, tt := range tests {
		got := ic.ComputeCredit(tt.idle, tt.creditRate)
		if got != tt.expected {
			t.Errorf("idle=%v rate=%v: expected %d, got %d", tt.idle, tt.creditRate, tt.expected, got)
		}
	}
}

func TestIdleCompensation_ComputeCreditRateFromIntervals(t *testing.T) {
	intervals := []time.Duration{
		10 * time.Second,
		15 * time.Second,
		5 * time.Second,
	}
	rate := ComputeCreditRateFromIntervals(intervals)
	expected := 10 * time.Second // avg of 10, 15, 5 = 10
	if rate != expected {
		t.Fatalf("expected %v, got %v", expected, rate)
	}
}

func TestIdleCompensation_ComputeCreditRateEmpty(t *testing.T) {
	rate := ComputeCreditRateFromIntervals(nil)
	if rate != 30*time.Second {
		t.Fatalf("expected 30s default, got %v", rate)
	}
}

func TestIdleCompensation_ComputeCreditRateVeryFast(t *testing.T) {
	intervals := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}
	rate := ComputeCreditRateFromIntervals(intervals)
	// Should clamp to minimum 1s.
	if rate != time.Second {
		t.Fatalf("expected 1s minimum, got %v", rate)
	}
}
