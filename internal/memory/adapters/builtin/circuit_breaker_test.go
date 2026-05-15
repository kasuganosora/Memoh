package builtin

import (
	"testing"
	"time"
)

func TestCircuitBreaker_NormalOperation(t *testing.T) {
	cb := NewCircuitBreaker(nil, CircuitBreakerConfig{
		FailureThreshold: 3,
		OpenDuration:     100 * time.Millisecond,
	})

	// Circuit should be closed initially
	if !cb.Allow() {
		t.Fatal("expected circuit to allow requests when closed")
	}
	if cb.State() != circuitClosed {
		t.Fatalf("expected state circuitClosed, got %d", cb.State())
	}

	// Record successes should keep it closed
	cb.RecordSuccess()
	cb.RecordSuccess()
	if !cb.Allow() {
		t.Fatal("expected circuit to allow requests after successes")
	}
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	cb := NewCircuitBreaker(nil, CircuitBreakerConfig{
		FailureThreshold: 3,
		OpenDuration:     100 * time.Millisecond,
	})

	// Record failures below threshold
	cb.RecordFailure()
	cb.RecordFailure()
	if !cb.Allow() {
		t.Fatal("expected circuit to allow requests below threshold")
	}
	if cb.State() != circuitClosed {
		t.Fatalf("expected state circuitClosed, got %d", cb.State())
	}

	// Third failure should open the circuit
	cb.RecordFailure()
	if cb.State() != circuitOpen {
		t.Fatalf("expected state circuitOpen after 3 failures, got %d", cb.State())
	}
	if cb.Allow() {
		t.Fatal("expected circuit to reject requests when open")
	}
}

func TestCircuitBreaker_Recovery(t *testing.T) {
	cb := NewCircuitBreaker(nil, CircuitBreakerConfig{
		FailureThreshold: 3,
		OpenDuration:     50 * time.Millisecond,
	})

	// Trip the circuit
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != circuitOpen {
		t.Fatal("expected circuit to be open")
	}

	// Wait for cooldown
	time.Sleep(60 * time.Millisecond)

	// Should transition to half-open and allow one request
	if !cb.Allow() {
		t.Fatal("expected circuit to allow request after cooldown (half-open)")
	}
	if cb.State() != circuitHalfOpen {
		t.Fatalf("expected state circuitHalfOpen, got %d", cb.State())
	}

	// Record success should close the circuit
	cb.RecordSuccess()
	if cb.State() != circuitClosed {
		t.Fatalf("expected state circuitClosed after recovery, got %d", cb.State())
	}
	if cb.ConsecutiveFailures() != 0 {
		t.Fatalf("expected 0 consecutive failures after recovery, got %d", cb.ConsecutiveFailures())
	}
}

func TestCircuitBreaker_HalfOpenFailure(t *testing.T) {
	cb := NewCircuitBreaker(nil, CircuitBreakerConfig{
		FailureThreshold: 3,
		OpenDuration:     50 * time.Millisecond,
	})

	// Trip the circuit
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordFailure()

	// Wait for cooldown
	time.Sleep(60 * time.Millisecond)

	// Allow transitions to half-open
	cb.Allow()
	if cb.State() != circuitHalfOpen {
		t.Fatalf("expected state circuitHalfOpen, got %d", cb.State())
	}

	// Failure in half-open should re-open the circuit
	cb.RecordFailure()
	if cb.State() != circuitOpen {
		t.Fatalf("expected state circuitOpen after half-open failure, got %d", cb.State())
	}
}

func TestCircuitBreaker_SuccessResetsFailureCount(t *testing.T) {
	cb := NewCircuitBreaker(nil, CircuitBreakerConfig{
		FailureThreshold: 3,
		OpenDuration:     100 * time.Millisecond,
	})

	// Two failures then a success
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordSuccess()

	// Failure count should be reset
	if cb.ConsecutiveFailures() != 0 {
		t.Fatalf("expected 0 consecutive failures after success, got %d", cb.ConsecutiveFailures())
	}

	// Two more failures should not trip the circuit
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != circuitClosed {
		t.Fatalf("expected state circuitClosed, got %d", cb.State())
	}
}
