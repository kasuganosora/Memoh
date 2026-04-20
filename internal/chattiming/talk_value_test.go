package chattiming

import (
	"testing"
	"time"
)

func TestTalkValue_DefaultValue(t *testing.T) {
	cfg := TalkValueConfig{} // zero value
	v := cfg.EffectiveValue(time.Now())
	if v != defaultTalkValue {
		t.Fatalf("expected default %f, got %f", defaultTalkValue, v)
	}
}

func TestTalkValue_Clamping(t *testing.T) {
	cfg := TalkValueConfig{Value: 0.001}
	v := cfg.EffectiveValue(time.Now())
	if v != minTalkValue {
		t.Fatalf("expected %f, got %f", minTalkValue, v)
	}

	cfg = TalkValueConfig{Value: 5.0}
	v = cfg.EffectiveValue(time.Now())
	if v != maxTalkValue {
		t.Fatalf("expected %f, got %f", maxTalkValue, v)
	}
}

func TestTalkValue_TriggerThreshold(t *testing.T) {
	tests := []struct {
		value     float64
		threshold int
	}{
		{1.0, 1},
		{0.5, 2},
		{0.34, 3},
		{0.25, 4},
		{0.2, 5},
		{0.1, 10},
	}
	for _, tt := range tests {
		cfg := TalkValueConfig{Value: tt.value}
		got := cfg.TriggerThreshold(time.Now())
		if got != tt.threshold {
			t.Errorf("value=%.2f: expected threshold %d, got %d", tt.value, tt.threshold, got)
		}
	}
}

func TestTalkValue_TimeRuleNormalRange(t *testing.T) {
	cfg := TalkValueConfig{
		Value: 0.5,
		TimeRules: []TalkValueTimeRule{
			{StartHour: 0, EndHour: 8, Value: 0.3},
			{StartHour: 9, EndHour: 18, Value: 1.0},
		},
	}

	// 3am → rule matches (0–8), value=0.3
	t3am := time.Date(2026, 4, 20, 3, 0, 0, 0, time.UTC)
	v := cfg.EffectiveValue(t3am)
	if v != 0.3 {
		t.Fatalf("3am: expected 0.3, got %f", v)
	}

	// 12pm → rule matches (9–18), value=1.0
	t12pm := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	v = cfg.EffectiveValue(t12pm)
	if v != 1.0 {
		t.Fatalf("12pm: expected 1.0, got %f", v)
	}

	// 8am → no rule matches (0–8 is exclusive at end, 9–18 starts at 9), fallback to base 0.5
	t8am := time.Date(2026, 4, 20, 8, 0, 0, 0, time.UTC)
	v = cfg.EffectiveValue(t8am)
	if v != 0.5 {
		t.Fatalf("8am: expected 0.5, got %f", v)
	}
}

func TestTalkValue_TimeRuleOvernight(t *testing.T) {
	cfg := TalkValueConfig{
		Value: 0.8,
		TimeRules: []TalkValueTimeRule{
			{StartHour: 22, EndHour: 6, Value: 0.2},
		},
	}

	// 23:00 → matches (hour >= 22)
	t23 := time.Date(2026, 4, 20, 23, 0, 0, 0, time.UTC)
	v := cfg.EffectiveValue(t23)
	if v != 0.2 {
		t.Fatalf("23:00: expected 0.2, got %f", v)
	}

	// 03:00 → matches (hour < 6)
	t3 := time.Date(2026, 4, 20, 3, 0, 0, 0, time.UTC)
	v = cfg.EffectiveValue(t3)
	if v != 0.2 {
		t.Fatalf("03:00: expected 0.2, got %f", v)
	}

	// 12:00 → no match, fallback to base
	t12 := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	v = cfg.EffectiveValue(t12)
	if v != 0.8 {
		t.Fatalf("12:00: expected 0.8, got %f", v)
	}
}

func TestTalkValue_TimeRuleWithDays(t *testing.T) {
	cfg := TalkValueConfig{
		Value: 0.5,
		TimeRules: []TalkValueTimeRule{
			{StartHour: 9, EndHour: 18, Value: 1.0, Days: []int{1, 2, 3, 4, 5}}, // weekdays only
		},
	}

	// Monday 12:00 → matches
	monday := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC) // Monday
	v := cfg.EffectiveValue(monday)
	if v != 1.0 {
		t.Fatalf("Monday 12pm: expected 1.0, got %f", v)
	}

	// Saturday 12:00 → no match (day filter)
	saturday := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC) // Saturday
	v = cfg.EffectiveValue(saturday)
	if v != 0.5 {
		t.Fatalf("Saturday 12pm: expected 0.5, got %f", v)
	}
}

func TestTalkValue_TimeRuleEmptyWindow(t *testing.T) {
	// StartHour == EndHour should never match (empty window).
	cfg := TalkValueConfig{
		Value:     0.5,
		TimeRules: []TalkValueTimeRule{{StartHour: 14, EndHour: 14, Value: 0.1}},
	}

	for hour := 0; hour < 24; hour++ {
		tm := time.Date(2026, 4, 20, hour, 0, 0, 0, time.UTC)
		v := cfg.EffectiveValue(tm)
		if v != 0.5 {
			t.Fatalf("hour %d: expected base 0.5 for empty window, got %f", hour, v)
		}
	}
}
