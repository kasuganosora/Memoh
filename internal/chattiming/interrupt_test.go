package chattiming

import (
	"context"
	"testing"
	"time"
)

func TestInterrupt_BindUnbind(t *testing.T) {
	ic := NewInterruptController(InterruptConfig{MaxConsecutive: 2, MaxRounds: 6})

	_, cancel := ic.Bind(context.Background())
	defer cancel()

	if !ic.IsActive() {
		t.Fatal("expected active after bind")
	}

	ic.Unbind(true)

	if ic.IsActive() {
		t.Fatal("expected inactive after unbind")
	}
	if ic.ConsecutiveCount() != 0 {
		t.Fatalf("expected consecutive=0 after natural completion, got %d", ic.ConsecutiveCount())
	}
}

func TestInterrupt_RequestInterrupt(t *testing.T) {
	ic := NewInterruptController(InterruptConfig{MaxConsecutive: 2, MaxRounds: 6})

	ctx, cancel := ic.Bind(context.Background())
	defer cancel()

	if !ic.RequestInterrupt() {
		t.Fatal("expected interrupt to succeed")
	}

	// Context should be cancelled.
	select {
	case <-ctx.Done():
		// Good.
	case <-time.After(100 * time.Millisecond):
		t.Fatal("context was not cancelled after interrupt")
	}

	// Second interrupt on same cycle should be blocked.
	if ic.RequestInterrupt() {
		t.Fatal("expected second interrupt to be blocked (single-trigger)")
	}
}

func TestInterrupt_ConsecutiveLimit(t *testing.T) {
	ic := NewInterruptController(InterruptConfig{MaxConsecutive: 2, MaxRounds: 10})

	// Simulate two consecutive interrupt cycles.
	for i := 0; i < 2; i++ {
		_, cancel := ic.Bind(context.Background())
		ic.RequestInterrupt()
		ic.Unbind(false) // interrupted, not completed
		cancel()
	}

	// Third cycle — consecutive count is at limit.
	_, cancel := ic.Bind(context.Background())
	defer cancel()

	if ic.RequestInterrupt() {
		t.Fatal("expected interrupt to be blocked by consecutive limit")
	}
}

func TestInterrupt_ConsecutiveResetOnCompletion(t *testing.T) {
	ic := NewInterruptController(InterruptConfig{MaxConsecutive: 2, MaxRounds: 10})

	// First cycle: interrupted.
	_, cancel1 := ic.Bind(context.Background())
	ic.RequestInterrupt()
	ic.Unbind(false)
	cancel1()

	// Second cycle: completed naturally.
	_, cancel2 := ic.Bind(context.Background())
	ic.Unbind(true) // natural completion resets count
	cancel2()

	// Third cycle: should be allowed again (count was reset).
	_, cancel3 := ic.Bind(context.Background())
	defer cancel3()

	if !ic.RequestInterrupt() {
		t.Fatal("expected interrupt to be allowed after natural completion reset")
	}
}

func TestInterrupt_CanRetry(t *testing.T) {
	ic := NewInterruptController(InterruptConfig{MaxConsecutive: 2, MaxRounds: 3})
	ic.ResetRounds()

	for i := 0; i < 3; i++ {
		if !ic.CanRetry() {
			t.Fatalf("round %d: expected can retry", i+1)
		}
		_, cancel := ic.Bind(context.Background())
		ic.Unbind(false)
		cancel()
	}

	if ic.CanRetry() {
		t.Fatal("expected no more retries after max rounds")
	}
}

func TestInterrupt_ResetRounds(t *testing.T) {
	ic := NewInterruptController(InterruptConfig{MaxConsecutive: 2, MaxRounds: 2})
	ic.ResetRounds()

	// Exhaust rounds.
	for i := 0; i < 2; i++ {
		_, cancel := ic.Bind(context.Background())
		ic.Unbind(false)
		cancel()
	}

	if ic.CanRetry() {
		t.Fatal("expected no retries")
	}

	// Reset should allow new round.
	ic.ResetRounds()
	if !ic.CanRetry() {
		t.Fatal("expected retries after reset")
	}
}

func TestInterrupt_NotActive(t *testing.T) {
	ic := NewInterruptController(InterruptConfig{MaxConsecutive: 2, MaxRounds: 6})

	if ic.RequestInterrupt() {
		t.Fatal("expected interrupt to fail when not active")
	}
}
