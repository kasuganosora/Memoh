package chattiming

import (
	"context"
	"sync"
)

// InterruptController manages mid-LLM-call interrupts for a discuss session.
// When new messages arrive while the bot is generating a response, the controller
// cancels the current LLM call so it can be restarted with updated context.
//
// Four-layer infinite-loop prevention:
//  1. Single-trigger: only one cancel per Bind() cycle
//  2. Consecutive counter: blocks interrupts after maxConsecutive reached
//  3. Counter resets only on natural completion (not interrupt)
//  4. Max rounds budget per trigger cycle
type InterruptController struct {
	mu sync.Mutex

	// State for the current agent invocation.
	cancelFn           context.CancelFunc // Cancels the current agent stream
	active             bool               // Is an agent stream running?
	interruptRequested bool               // Has an interrupt been requested this cycle?

	// Cumulative state across retries.
	consecutiveCount int // Consecutive interrupts so far
	currentRound     int // Current round in the retry loop

	// Configuration.
	maxConsecutive int // Maximum allowed consecutive interrupts (default: 2)
	maxRounds      int // Maximum retry rounds per trigger (default: 6)
}

// InterruptConfig controls planner interrupt behavior.
type InterruptConfig struct {
	Enabled        bool `json:"enabled,omitempty"`
	MaxConsecutive int  `json:"max_consecutive,omitempty"` // default: 2
	MaxRounds      int  `json:"max_rounds,omitempty"`      // default: 6
}

const (
	defaultMaxConsecutive = 2
	defaultMaxRounds      = 6
)

// NewInterruptController creates a controller with the given limits.
func NewInterruptController(cfg InterruptConfig) *InterruptController {
	mc := cfg.MaxConsecutive
	if mc <= 0 {
		mc = defaultMaxConsecutive
	}
	mr := cfg.MaxRounds
	if mr <= 0 {
		mr = defaultMaxRounds
	}
	return &InterruptController{
		maxConsecutive: mc,
		maxRounds:      mr,
	}
}

// Bind attaches a cancellable sub-context to the controller.
// Call this at the start of each agent invocation.
// Returns the derived context and a cancel function.
//
// Usage:
//
//	agentCtx, agentCancel := ctrl.Bind(parentCtx)
//	defer agentCancel()
//	// ... run agent with agentCtx ...
//	ctrl.Unbind(wasCompletedNaturally)
func (ic *InterruptController) Bind(parent context.Context) (context.Context, context.CancelFunc) {
	ic.mu.Lock()
	defer ic.mu.Unlock()

	ctx, cancel := context.WithCancel(parent)
	ic.cancelFn = cancel
	ic.active = true
	ic.interruptRequested = false
	ic.currentRound++

	return ctx, cancel
}

// Unbind detaches the context. Set completed=true if the agent finished
// naturally (this resets the consecutive interrupt counter).
// Set completed=false if the agent was interrupted.
func (ic *InterruptController) Unbind(completed bool) {
	ic.mu.Lock()
	defer ic.mu.Unlock()

	ic.active = false
	ic.cancelFn = nil
	ic.interruptRequested = false

	if completed {
		ic.consecutiveCount = 0
		ic.currentRound = 0
	}
}

// RequestInterrupt attempts to cancel the current agent stream.
// Returns true if the interrupt was triggered, false if blocked by guards.
//
// Guards:
//  1. Not active (no agent running)
//  2. Already requested this cycle (single-trigger)
//  3. Consecutive count exceeded
func (ic *InterruptController) RequestInterrupt() bool {
	ic.mu.Lock()
	defer ic.mu.Unlock()

	if !ic.active {
		return false
	}
	if ic.interruptRequested {
		return false
	}
	if ic.consecutiveCount >= ic.maxConsecutive {
		return false
	}

	ic.interruptRequested = true
	ic.consecutiveCount++
	ic.cancelFn()
	return true
}

// CanRetry returns true if the retry loop has budget remaining.
func (ic *InterruptController) CanRetry() bool {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	return ic.currentRound < ic.maxRounds
}

// ResetRounds resets the round counter. Call at the start of a new trigger
// (i.e., when a new batch of messages starts processing).
func (ic *InterruptController) ResetRounds() {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	ic.currentRound = 0
}

// IsActive returns whether an agent stream is currently running.
func (ic *InterruptController) IsActive() bool {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	return ic.active
}

// ConsecutiveCount returns the current consecutive interrupt count (for logging).
func (ic *InterruptController) ConsecutiveCount() int {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	return ic.consecutiveCount
}
