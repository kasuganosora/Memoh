package chattiming

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestDebouncer_QuietPeriod(t *testing.T) {
	d := NewDebouncer(DebounceConfig{
		QuietPeriod: 50 * time.Millisecond,
		MaxWait:     500 * time.Millisecond,
	})
	defer d.Stop()

	d.Reset()

	start := time.Now()
	err := d.Wait(context.Background())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed < 40*time.Millisecond {
		t.Fatalf("wait returned too fast: %v", elapsed)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("wait returned too slow: %v", elapsed)
	}
}

func TestDebouncer_ResetExtends(t *testing.T) {
	d := NewDebouncer(DebounceConfig{
		QuietPeriod: 50 * time.Millisecond,
		MaxWait:     2 * time.Second,
	})
	defer d.Stop()

	d.Reset()

	// After 30ms, reset again — should extend the quiet period.
	time.Sleep(30 * time.Millisecond)
	d.Reset()

	start := time.Now()
	err := d.Wait(context.Background())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have waited at least another ~50ms from the second Reset().
	if elapsed < 40*time.Millisecond {
		t.Fatalf("wait returned too fast after reset: %v", elapsed)
	}
}

func TestDebouncer_MaxWait(t *testing.T) {
	// Use a quiet period longer than we expect to wait, but still within
	// what NormalizeDefaults allows. We set MaxWait > QuietPeriod so
	// NormalizeDefaults doesn't adjust it.
	d := NewDebouncer(DebounceConfig{
		QuietPeriod: 100 * time.Millisecond,
		MaxWait:     200 * time.Millisecond,
	})
	defer d.Stop()

	// Keep resetting to extend the quiet period beyond MaxWait.
	d.Reset()
	time.Sleep(50 * time.Millisecond)
	d.Reset() // Extends quiet period to +100ms from now, total ~150ms > MaxWait

	start := time.Now()
	err := d.Wait(context.Background())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should be bounded by MaxWait (~200ms from first Reset), not QuietPeriod.
	if elapsed > 400*time.Millisecond {
		t.Fatalf("max wait overshot: %v", elapsed)
	}
}

func TestDebouncer_ContextCancel(t *testing.T) {
	d := NewDebouncer(DebounceConfig{
		QuietPeriod: 10 * time.Second,
		MaxWait:     10 * time.Second,
	})
	defer d.Stop()

	d.Reset()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := d.Wait(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("context cancel too slow: %v", elapsed)
	}
}

func TestDebouncer_Stop(t *testing.T) {
	d := NewDebouncer(DebounceConfig{
		QuietPeriod: 10 * time.Second,
		MaxWait:     10 * time.Second,
	})

	d.Reset()
	d.Stop()

	err := d.Wait(context.Background())
	if err != nil {
		t.Fatalf("unexpected error after stop: %v", err)
	}
}

func TestDebouncer_NoReset(t *testing.T) {
	d := NewDebouncer(DebounceConfig{
		QuietPeriod: 50 * time.Millisecond,
		MaxWait:     100 * time.Millisecond,
	})
	defer d.Stop()

	// Wait without ever calling Reset — should return immediately.
	err := d.Wait(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDebouncer_ConcurrentReset(t *testing.T) {
	d := NewDebouncer(DebounceConfig{
		QuietPeriod: 10 * time.Millisecond,
		MaxWait:     500 * time.Millisecond,
	})
	defer d.Stop()

	var resets atomic.Int32
	done := make(chan struct{})

	// Multiple goroutines calling Reset concurrently.
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				d.Reset()
				resets.Add(1)
			}
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	err := d.Wait(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resets.Load() != 1000 {
		t.Fatalf("expected 1000 resets, got %d", resets.Load())
	}
}

func TestDebouncer_MultipleCycles(t *testing.T) {
	// Verify the debouncer works correctly across multiple Reset/Wait cycles.
	// The started field must be cleared after Wait returns so the next cycle
	// gets a fresh MaxWait budget.
	d := NewDebouncer(DebounceConfig{
		QuietPeriod: 50 * time.Millisecond,
		MaxWait:     200 * time.Millisecond,
	})
	defer d.Stop()

	// Cycle 1.
	d.Reset()
	if err := d.Wait(context.Background()); err != nil {
		t.Fatalf("cycle 1: unexpected error: %v", err)
	}

	// Cycle 2: should also get a full quiet period, not bypass it.
	d.Reset()
	start := time.Now()
	if err := d.Wait(context.Background()); err != nil {
		t.Fatalf("cycle 2: unexpected error: %v", err)
	}
	elapsed := time.Since(start)

	// Should have waited at least ~50ms (quiet period), not returned immediately.
	if elapsed < 40*time.Millisecond {
		t.Fatalf("cycle 2 bypassed quiet period: %v", elapsed)
	}
}
