package sessionchecker

import (
	"context"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/pipeline"
)

type mockInspector struct {
	diags []pipeline.SessionDiag
}

func (m *mockInspector) SessionDiagnosticsForBot(_ string) []pipeline.SessionDiag {
	return m.diags
}

func TestListChecks_NoSessions(t *testing.T) {
	c := NewChecker(nil, &mockInspector{})
	results := c.ListChecks(context.Background(), "bot-1")
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestListChecks_HealthySession(t *testing.T) {
	now := time.Now()
	c := NewChecker(nil, &mockInspector{
		diags: []pipeline.SessionDiag{
			{
				SessionID:      "sess-1",
				BotID:          "bot-1",
				StartedAt:      now.Add(-5 * time.Minute),
				LastActivityAt: now.Add(-30 * time.Second),
				AliveDuration:  5 * time.Minute,
				IdleDuration:   30 * time.Second,
				QueueDepth:     0,
				QueueCapacity:  16,
			},
		},
	})
	results := c.ListChecks(context.Background(), "bot-1")
	if len(results) != 0 {
		t.Fatalf("expected 0 results for healthy session, got %d", len(results))
	}
}

func TestListChecks_StuckSession(t *testing.T) {
	now := time.Now()
	c := NewChecker(nil, &mockInspector{
		diags: []pipeline.SessionDiag{
			{
				SessionID:      "sess-stuck",
				BotID:          "bot-1",
				StartedAt:      now.Add(-20 * time.Minute),
				LastActivityAt: now.Add(-16 * time.Minute),
				AliveDuration:  20 * time.Minute,
				IdleDuration:   16 * time.Minute,
				QueueDepth:     3,
				QueueCapacity:  16,
			},
		},
	})
	results := c.ListChecks(context.Background(), "bot-1")
	if len(results) != 1 {
		t.Fatalf("expected 1 result for stuck session, got %d", len(results))
	}
	if results[0].Status != "error" {
		t.Errorf("expected status=error, got %q", results[0].Status)
	}
	if results[0].ID != "session.stuck.sess-stuck" {
		t.Errorf("unexpected check ID: %q", results[0].ID)
	}
}

func TestListChecks_QueueBacklog(t *testing.T) {
	now := time.Now()
	c := NewChecker(nil, &mockInspector{
		diags: []pipeline.SessionDiag{
			{
				SessionID:      "sess-backlog",
				BotID:          "bot-1",
				StartedAt:      now.Add(-3 * time.Minute),
				LastActivityAt: now.Add(-2 * time.Minute),
				AliveDuration:  3 * time.Minute,
				IdleDuration:   2 * time.Minute,
				QueueDepth:     10,
				QueueCapacity:  16,
			},
		},
	})
	results := c.ListChecks(context.Background(), "bot-1")
	if len(results) != 1 {
		t.Fatalf("expected 1 result for backlogged session, got %d", len(results))
	}
	if results[0].Status != "warn" {
		t.Errorf("expected status=warn, got %q", results[0].Status)
	}
}

func TestListChecks_NilInspector(t *testing.T) {
	c := NewChecker(nil, nil)
	results := c.ListChecks(context.Background(), "bot-1")
	if len(results) != 0 {
		t.Fatalf("expected 0 results with nil inspector, got %d", len(results))
	}
}

func TestListChecks_EmptyBotID(t *testing.T) {
	c := NewChecker(nil, &mockInspector{
		diags: []pipeline.SessionDiag{{SessionID: "x", BotID: "bot-1"}},
	})
	results := c.ListChecks(context.Background(), "")
	if len(results) != 0 {
		t.Fatalf("expected 0 results with empty botID, got %d", len(results))
	}
}
