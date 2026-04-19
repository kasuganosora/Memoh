package chattiming

import (
	"math"
	"time"
)

// TalkValueConfig controls how chatty the bot is in group discussions.
// Value ranges from 0.0 (silent) to 1.0 (respond to every message batch).
type TalkValueConfig struct {
	// Value is the base chattiness (0.01–1.0). Default: 0.5.
	Value float64 `json:"value,omitempty"`

	// TimeRules are time-of-day overrides for the talk_value.
	TimeRules []TalkValueTimeRule `json:"time_rules,omitempty"`
}

// TalkValueTimeRule overrides talk_value during a specific time window.
type TalkValueTimeRule struct {
	// StartHour is the start of the time window (0–23, inclusive).
	StartHour int `json:"start_hour,omitempty"`
	// EndHour is the end of the time window (0–23, exclusive).
	EndHour int `json:"end_hour,omitempty"`
	// Value is the talk_value override during this window.
	Value float64 `json:"value"`
	// Days restricts the rule to specific weekdays. Empty = every day.
	// Uses time.Weekday values: 0=Sunday, 1=Monday, ..., 6=Saturday.
	Days []int `json:"days,omitempty"`
}

const (
	defaultTalkValue = 0.5
	minTalkValue     = 0.01
	maxTalkValue     = 1.0
)

// EffectiveValue returns the talk_value for the given time, considering
// time-of-day rules. If no rule matches, the base Value is used.
func (c *TalkValueConfig) EffectiveValue(t time.Time) float64 {
	v := c.Value
	if v <= 0 {
		v = defaultTalkValue
	}

	// Check time rules in order; last matching rule wins.
	for _, rule := range c.TimeRules {
		if rule.matches(t) {
			v = rule.Value
		}
	}

	return clampTalkValue(v)
}

// TriggerThreshold returns how many messages must accumulate before the bot
// considers responding. Computed as ceil(1.0 / effectiveValue).
//
//	value=1.0 → threshold=1 (every message)
//	value=0.5 → threshold=2
//	value=0.2 → threshold=5
//	value=0.1 → threshold=10
func (c *TalkValueConfig) TriggerThreshold(t time.Time) int {
	ev := c.EffectiveValue(t)
	return int(math.Ceil(1.0 / ev))
}

func (r *TalkValueTimeRule) matches(t time.Time) bool {
	h := t.Hour()

	// Check day-of-week filter.
	if len(r.Days) > 0 {
		w := int(t.Weekday())
		found := false
		for _, d := range r.Days {
			if d == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Handle overnight ranges (e.g., 22:00–06:00 means 22:00–24:00 OR 00:00–06:00).
	if r.StartHour >= r.EndHour {
		// Overnight: matches if hour >= start OR hour < end.
		return h >= r.StartHour || h < r.EndHour
	}
	// Normal range: matches if start <= hour < end.
	return h >= r.StartHour && h < r.EndHour
}

func clampTalkValue(v float64) float64 {
	if v < minTalkValue {
		return minTalkValue
	}
	if v > maxTalkValue {
		return maxTalkValue
	}
	return v
}
