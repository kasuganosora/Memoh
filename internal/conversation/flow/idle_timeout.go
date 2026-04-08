package flow

import (
	"context"
	"sync"
	"time"
)

// idleCancel wraps a resettable idle timer. If Reset() is not called before
// the timer fires, the underlying context is cancelled.
type idleCancel struct {
	cancel context.CancelFunc
	timer  *time.Timer
	mu     sync.Mutex
	fired  bool
}

func (ic *idleCancel) Reset() {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if !ic.fired {
		ic.timer.Stop()
		ic.timer.Reset(idleTimeout)
	}
}

func (ic *idleCancel) Stop() {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	ic.timer.Stop()
}

func (ic *idleCancel) DidFire() bool {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	return ic.fired
}

const idleTimeout = 90 * time.Second

// withIdleTimeout returns a context that is cancelled if no Reset() call is
// made within idleTimeout. The returned idleCancel must have Reset() called
// for each meaningful event (e.g. each agent stream event) to prevent the
// timeout from firing.
func withIdleTimeout(parent context.Context) (context.Context, *idleCancel) {
	ctx, cancel := context.WithCancel(parent)
	ic := &idleCancel{
		cancel: cancel,
	}
	ic.timer = time.AfterFunc(idleTimeout, func() {
		ic.mu.Lock()
		ic.fired = true
		ic.mu.Unlock()
		cancel()
	})
	return ctx, ic
}
