package heartbeatchecker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/healthcheck"
)

type mockInspector struct {
	status HeartbeatStatus
	err    error
}

func (m *mockInspector) HeartbeatStatusForBot(_ context.Context, _ string) (HeartbeatStatus, error) {
	return m.status, m.err
}

func TestListChecks_Disabled(t *testing.T) {
	c := NewChecker(nil, &mockInspector{status: HeartbeatStatus{Enabled: false}})
	results := c.ListChecks(context.Background(), "bot-1")
	if len(results) != 0 {
		t.Fatalf("expected 0 results for disabled heartbeat, got %d", len(results))
	}
}

func TestListChecks_NeverRun(t *testing.T) {
	c := NewChecker(nil, &mockInspector{status: HeartbeatStatus{
		Enabled:         true,
		IntervalMinutes: 30,
	}})
	results := c.ListChecks(context.Background(), "bot-1")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != healthcheck.StatusWarn {
		t.Errorf("expected status=warn, got %q", results[0].Status)
	}
}

func TestListChecks_Healthy(t *testing.T) {
	c := NewChecker(nil, &mockInspector{status: HeartbeatStatus{
		Enabled:         true,
		IntervalMinutes: 30,
		LastRunAt:       time.Now().Add(-10 * time.Minute),
		LastStatus:      "completed",
	}})
	results := c.ListChecks(context.Background(), "bot-1")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != healthcheck.StatusOK {
		t.Errorf("expected status=ok, got %q", results[0].Status)
	}
}

func TestListChecks_Overdue(t *testing.T) {
	c := NewChecker(nil, &mockInspector{status: HeartbeatStatus{
		Enabled:         true,
		IntervalMinutes: 30,
		LastRunAt:       time.Now().Add(-80 * time.Minute), // 80min > 30min * 2.5 = 75min
		LastStatus:      "completed",
	}})
	results := c.ListChecks(context.Background(), "bot-1")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != healthcheck.StatusError {
		t.Errorf("expected status=error, got %q", results[0].Status)
	}
}

func TestListChecks_LastRunError(t *testing.T) {
	c := NewChecker(nil, &mockInspector{status: HeartbeatStatus{
		Enabled:         true,
		IntervalMinutes: 30,
		LastRunAt:       time.Now().Add(-5 * time.Minute),
		LastStatus:      "error",
		LastError:       "trigger timeout",
	}})
	results := c.ListChecks(context.Background(), "bot-1")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != healthcheck.StatusWarn {
		t.Errorf("expected status=warn, got %q", results[0].Status)
	}
}

func TestListChecks_InspectorError(t *testing.T) {
	c := NewChecker(nil, &mockInspector{err: errors.New("db error")})
	results := c.ListChecks(context.Background(), "bot-1")
	if len(results) != 0 {
		t.Fatalf("expected 0 results on error, got %d", len(results))
	}
}
