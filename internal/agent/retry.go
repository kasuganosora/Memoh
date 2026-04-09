package agent

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strings"
	"time"
)

// RetryConfig controls retry behavior for stream failures.
type RetryConfig struct {
	MaxAttempts  int           // total retry attempts
	FastAttempts int           // first N attempts with no delay
	BaseDelay    time.Duration // backoff base for non-fast attempts
	MaxDelay     time.Duration // backoff cap
}

// serverErrPattern matches "api error 5XX" where XX is any two digits.
var serverErrPattern = regexp.MustCompile(`api error 5\d{2}`)

// DefaultRetryConfig returns the default retry strategy: 10 attempts total,
// first 5 fast (no delay), last 5 with exponential backoff.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:  10,
		FastAttempts: 5,
		BaseDelay:    1 * time.Second,
		MaxDelay:     30 * time.Second,
	}
}

// isRetryableStreamError returns true for errors worth retrying.
func isRetryableStreamError(err error) bool {
	if err == nil {
		return false
	}
	// Context cancelled/expired — do NOT retry (check first since
	// context.DeadlineExceeded also satisfies net.Error)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// Network-level errors (connection refused, timeout, DNS)
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// HTTP status errors: retry on 429 and 5xx
	errStr := err.Error()
	if strings.Contains(errStr, "429") {
		return true
	}
	if strings.Contains(errStr, "rate limit") || strings.Contains(errStr, "rate_limit") {
		return true
	}
	if serverErrPattern.MatchString(errStr) {
		return true
	}
	// Connection reset / EOF
	if strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "EOF") ||
		strings.Contains(errStr, "connection refused") {
		return true
	}
	return false
}

// retryDelay returns the delay before the next retry attempt.
// For fast attempts (0-indexed < FastAttempts): no delay.
// For backoff attempts: exponential delay with jitter, capped at MaxDelay.
func retryDelay(attempt int, cfg RetryConfig) time.Duration {
	if attempt < cfg.FastAttempts {
		return 0
	}
	// Exponential backoff: base * 2^(attempt - fastAttempts)
	backoffIdx := attempt - cfg.FastAttempts
	delay := cfg.BaseDelay * time.Duration(1<<uint(backoffIdx))
	return min(delay, cfg.MaxDelay)
}
