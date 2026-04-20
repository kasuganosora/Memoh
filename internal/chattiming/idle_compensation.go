package chattiming

import (
	"time"
)

// IdleCompensationConfig controls time-based message credit.
// When the chat goes quiet but not enough messages have accumulated to meet
// the talk_value threshold, elapsed silence time is converted into equivalent
// message credit to trigger processing anyway.
type IdleCompensationConfig struct {
	// Enabled turns on idle compensation.
	Enabled bool `json:"enabled,omitempty"`

	// WindowSize is the lookback window for computing average reply delay.
	// Default: 10 minutes.
	WindowSize time.Duration `json:"window_size,omitempty"`

	// MinIdleBeforeCredit is the minimum idle time before any credit is granted.
	// Default: 5 seconds.
	MinIdleBeforeCredit time.Duration `json:"min_idle_before_credit,omitempty"`
}

const (
	defaultIdleWindow          = 10 * time.Minute
	defaultMinIdleBeforeCredit = 5 * time.Second
)

// NormalizeDefaults fills zero-valued fields with defaults.
func (c *IdleCompensationConfig) NormalizeDefaults() {
	if c.WindowSize <= 0 {
		c.WindowSize = defaultIdleWindow
	}
	if c.MinIdleBeforeCredit <= 0 {
		c.MinIdleBeforeCredit = defaultMinIdleBeforeCredit
	}
}

// IdleCompensator converts idle time into message credit.
type IdleCompensator struct {
	config IdleCompensationConfig
}

// NewIdleCompensator creates a compensator with the given config.
func NewIdleCompensator(cfg IdleCompensationConfig) *IdleCompensator {
	cfg.NormalizeDefaults()
	return &IdleCompensator{config: cfg}
}

// ComputeCredit returns the number of equivalent messages earned by idle time.
// creditRate is the average delay between messages (e.g., 10s means 10s silence ≈ 1 message).
// Returns 0 if idle duration is below MinIdleBeforeCredit.
func (ic *IdleCompensator) ComputeCredit(idleDuration time.Duration, creditRate time.Duration) int {
	if idleDuration < ic.config.MinIdleBeforeCredit {
		return 0
	}
	if creditRate <= 0 {
		return 0
	}
	return int(idleDuration / creditRate)
}

// ComputeCreditRateFromIntervals computes an appropriate credit rate from a set
// of message inter-arrival intervals. Returns the average interval.
// If no intervals are provided, returns a default of 30s.
func ComputeCreditRateFromIntervals(intervals []time.Duration) time.Duration {
	if len(intervals) == 0 {
		return 30 * time.Second
	}
	var total time.Duration
	for _, d := range intervals {
		total += d
	}
	avg := total / time.Duration(len(intervals))
	if avg < time.Second {
		return time.Second
	}
	return avg
}
