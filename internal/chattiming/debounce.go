package chattiming

import (
	"context"
	"sync"
	"time"
)

// DebounceConfig controls the quiet-period wait before processing messages.
type DebounceConfig struct {
	// QuietPeriod is how long to wait after the last message before processing.
	// Default: 2s.
	QuietPeriod time.Duration `json:"quiet_period,omitempty"`

	// MaxWait is the upper bound on total debounce time. Prevents waiting forever.
	// Default: 15s.
	MaxWait time.Duration `json:"max_wait,omitempty"`
}

// Defaults.
const (
	DefaultDebounceQuietPeriod = 2 * time.Second
	DefaultDebounceMaxWait     = 15 * time.Second
)

// NormalizeDefaults fills zero-valued fields with defaults.
func (c *DebounceConfig) NormalizeDefaults() {
	if c.QuietPeriod <= 0 {
		c.QuietPeriod = DefaultDebounceQuietPeriod
	}
	if c.MaxWait <= 0 {
		c.MaxWait = DefaultDebounceMaxWait
	}
	if c.MaxWait < c.QuietPeriod {
		c.MaxWait = c.QuietPeriod
	}
}

// Debouncer waits for a quiet period after the last input before allowing
// processing to proceed. Call Reset() on each new input, call Wait() to block
// until the quiet period elapses.
//
// The debounce extends as long as new inputs keep arriving (via Reset()),
// but will never wait longer than MaxWait total from the first input.
type Debouncer struct {
	config  DebounceConfig
	mu      sync.Mutex
	started time.Time
	timer   *time.Timer
	done    chan struct{}
	stopped bool
}

// NewDebouncer creates a Debouncer with the given configuration.
func NewDebouncer(cfg DebounceConfig) *Debouncer {
	cfg.NormalizeDefaults()
	return &Debouncer{
		config: cfg,
		done:   make(chan struct{}, 1),
	}
}

// Reset records a new input event, extending the quiet period.
// The first call in a cycle records the start time for MaxWait enforcement.
func (d *Debouncer) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	if d.started.IsZero() {
		d.started = now
	}

	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(d.config.QuietPeriod, func() {
		select {
		case d.done <- struct{}{}:
		default:
		}
	})
}

// Wait blocks until either:
//   - The quiet period elapses (no Reset() calls for QuietPeriod duration)
//   - The MaxWait upper bound is reached
//   - The context is cancelled
//   - Stop() is called
//
// Returns ctx.Err() if the context was cancelled.
func (d *Debouncer) Wait(ctx context.Context) error {
	d.mu.Lock()
	started := d.started
	maxWait := d.config.MaxWait
	d.mu.Unlock()

	if started.IsZero() {
		return nil
	}

	// Use a separate timer for MaxWait enforcement.
	maxWaitTimer := time.NewTimer(maxWait)
	defer maxWaitTimer.Stop()

	for {
		d.mu.Lock()
		stopped := d.stopped
		d.mu.Unlock()
		if stopped {
			return nil
		}

		select {
		case <-d.done:
			// Quiet period elapsed — clear started for the next cycle.
			d.mu.Lock()
			d.started = time.Time{}
			d.mu.Unlock()
			return nil
		case <-maxWaitTimer.C:
			// MaxWait reached — clear started for the next cycle.
			d.mu.Lock()
			d.started = time.Time{}
			d.mu.Unlock()
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Stop terminates the debouncer, releasing any waiting goroutines.
func (d *Debouncer) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.stopped = true
	d.started = time.Time{}
	if d.timer != nil {
		d.timer.Stop()
	}
	select {
	case d.done <- struct{}{}:
	default:
	}
}

// Elapsed returns the duration since the first Reset() call.
// Returns zero if Reset() was never called.
func (d *Debouncer) Elapsed() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.started.IsZero() {
		return 0
	}
	return time.Since(d.started)
}
