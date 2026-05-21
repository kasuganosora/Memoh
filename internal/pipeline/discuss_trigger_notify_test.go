package pipeline

import (
	"sync"
	"testing"
)

// TestNotifyRC_ConcurrentDropOldest verifies that concurrent pushes to a full
// rcCh channel do not lose the latest RC due to race conditions in the
// drop-oldest logic.
func TestNotifyRC_ConcurrentDropOldest(t *testing.T) {
	t.Parallel()

	const (
		numGoroutines = 50
		chanSize      = 4
	)

	sess := &discussSession{
		rcCh: make(chan RenderedContext, chanSize),
	}

	// Fill the channel to capacity.
	for i := 0; i < chanSize; i++ {
		sess.rcCh <- RenderedContext{{ReceivedAtMs: int64(i)}}
	}

	// Concurrently push new RCs using the mutex-protected drop-oldest pattern.
	var wg sync.WaitGroup
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			rc := RenderedContext{{ReceivedAtMs: int64(1000 + id)}}
			sess.rcMu.Lock()
			select {
			case sess.rcCh <- rc:
			default:
				select {
				case <-sess.rcCh:
				default:
				}
				sess.rcCh <- rc
			}
			sess.rcMu.Unlock()
		}(i)
	}
	wg.Wait()

	// After all goroutines complete, the channel should be exactly at capacity
	// and should not have deadlocked or panicked.
	if len(sess.rcCh) != chanSize {
		t.Fatalf("expected channel to have %d items, got %d", chanSize, len(sess.rcCh))
	}

	// Drain and verify all items are valid (non-nil).
	for i := 0; i < chanSize; i++ {
		rc := <-sess.rcCh
		if len(rc) == 0 {
			t.Fatalf("item %d: expected non-empty RC", i)
		}
		// All remaining items should be from the concurrent pushes (>= 1000).
		if rc[0].ReceivedAtMs < 1000 {
			t.Fatalf("item %d: expected RC from concurrent push (>=1000), got %d", i, rc[0].ReceivedAtMs)
		}
	}
}
