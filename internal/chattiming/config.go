package chattiming

import (
	"time"
)

// Config holds all smart chat timing configuration for a bot.
// Stored as JSONB in the bots table.
type Config struct {
	// Enabled turns on all smart timing features for discuss mode.
	Enabled bool `json:"enabled,omitempty"`

	// Debounce is the quiet period configuration.
	Debounce DebounceConfig `json:"debounce,omitempty"`

	// TimingGate enables the LLM-based should-I-speak check.
	TimingGate bool `json:"timing_gate,omitempty"`

	// TalkValue controls chattiness.
	TalkValue TalkValueConfig `json:"talk_value,omitempty"`

	// Interrupt enables planner interrupts (cancel mid-LLM, re-run).
	Interrupt InterruptConfig `json:"interrupt,omitempty"`

	// IdleCompensation enables time-based message credit.
	IdleCompensation IdleCompensationConfig `json:"idle_compensation,omitempty"`
}

// DefaultConfig returns the recommended defaults.
func DefaultConfig() Config {
	return Config{
		Enabled: false,
		Debounce: DebounceConfig{
			QuietPeriod: DefaultDebounceQuietPeriod,
			MaxWait:     DefaultDebounceMaxWait,
		},
		TimingGate: true,
		TalkValue: TalkValueConfig{
			Value: defaultTalkValue,
		},
		Interrupt: InterruptConfig{
			Enabled:        true,
			MaxConsecutive: defaultMaxConsecutive,
			MaxRounds:      defaultMaxRounds,
		},
		IdleCompensation: IdleCompensationConfig{
			Enabled:             true,
			WindowSize:          defaultIdleWindow,
			MinIdleBeforeCredit: defaultMinIdleBeforeCredit,
		},
	}
}

// EffectiveTalkValue returns the talk_value for the current time.
func (c *Config) EffectiveTalkValue() float64 {
	return c.TalkValue.EffectiveValue(time.Now())
}

// TriggerThreshold returns how many messages must accumulate for the current time.
func (c *Config) TriggerThreshold() int {
	return c.TalkValue.TriggerThreshold(time.Now())
}
