package builtin

import (
	"log/slog"
	"sync"
	"time"
)

// circuitState represents the current state of the circuit breaker.
type circuitState int

const (
	circuitClosed   circuitState = iota // Normal operation
	circuitOpen                         // Tripped, rejecting requests
	circuitHalfOpen                     // Testing if service recovered
)

const (
	// defaultFailureThreshold is the number of consecutive failures before opening the circuit.
	defaultFailureThreshold = 3
	// defaultOpenDuration is how long the circuit stays open before transitioning to half-open.
	defaultOpenDuration = 30 * time.Second
)

// CircuitBreaker implements a simple circuit breaker pattern for memory search operations.
// After consecutive failures (timeouts), it automatically skips requests for a cooldown period.
type CircuitBreaker struct {
	mu               sync.Mutex
	state            circuitState
	consecutiveFails int
	lastFailTime     time.Time
	failureThreshold int
	openDuration     time.Duration
	logger           *slog.Logger
}

// CircuitBreakerConfig holds configuration for the circuit breaker.
type CircuitBreakerConfig struct {
	FailureThreshold int
	OpenDuration     time.Duration
}

// NewCircuitBreaker creates a new circuit breaker with the given configuration.
func NewCircuitBreaker(logger *slog.Logger, cfg CircuitBreakerConfig) *CircuitBreaker {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = defaultFailureThreshold
	}
	if cfg.OpenDuration <= 0 {
		cfg.OpenDuration = defaultOpenDuration
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &CircuitBreaker{
		state:            circuitClosed,
		failureThreshold: cfg.FailureThreshold,
		openDuration:     cfg.OpenDuration,
		logger:           logger,
	}
}

// Allow checks whether a request should be allowed through.
// Returns true if the circuit is closed or half-open (testing recovery).
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case circuitClosed:
		return true
	case circuitOpen:
		// Check if cooldown has elapsed
		if time.Since(cb.lastFailTime) >= cb.openDuration {
			cb.state = circuitHalfOpen
			cb.logger.Info("circuit breaker: transitioning to half-open",
				slog.Duration("open_duration", cb.openDuration),
			)
			return true
		}
		return false
	case circuitHalfOpen:
		// Allow one request through to test recovery
		return true
	}
	return true
}

// RecordSuccess records a successful operation, resetting the failure counter.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == circuitHalfOpen {
		cb.logger.Info("circuit breaker: recovered, closing circuit")
	}
	cb.state = circuitClosed
	cb.consecutiveFails = 0
}

// RecordFailure records a failed operation. If consecutive failures reach the
// threshold, the circuit opens.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.consecutiveFails++
	cb.lastFailTime = time.Now()

	if cb.consecutiveFails >= cb.failureThreshold {
		if cb.state != circuitOpen {
			cb.logger.Warn("circuit breaker: opening circuit",
				slog.Int("consecutive_failures", cb.consecutiveFails),
				slog.Duration("open_duration", cb.openDuration),
			)
		}
		cb.state = circuitOpen
	}
}

// State returns the current circuit state (for observability/testing).
func (cb *CircuitBreaker) State() circuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// ConsecutiveFailures returns the current consecutive failure count (for testing).
func (cb *CircuitBreaker) ConsecutiveFailures() int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.consecutiveFails
}
