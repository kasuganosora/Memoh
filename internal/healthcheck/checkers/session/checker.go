package sessionchecker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/memohai/memoh/internal/healthcheck"
	"github.com/memohai/memoh/internal/pipeline"
)

const (
	checkTypeSessionStuck = "session.stuck"
	titleKeySessionStuck  = "bots.checks.titles.sessionStuck"

	// stuckThreshold is the duration of inactivity after which a session
	// is considered potentially stuck. The watchdog timeout is 21min, so
	// 15min of inactivity is a strong signal that something is wrong.
	stuckThreshold = 15 * time.Minute

	// queueBacklogThreshold: if the RC channel has more than this many
	// pending items, the session goroutine is likely blocked and not
	// consuming messages.
	queueBacklogThreshold = 8
)

// SessionInspector provides diagnostic information about active discuss sessions.
type SessionInspector interface {
	SessionDiagnosticsForBot(botID string) []pipeline.SessionDiag
}

// Checker evaluates discuss session health — detects stuck sessions.
type Checker struct {
	logger    *slog.Logger
	inspector SessionInspector
}

// NewChecker creates a session health checker.
func NewChecker(log *slog.Logger, inspector SessionInspector) *Checker {
	if log == nil {
		log = slog.Default()
	}
	return &Checker{
		logger:    log.With(slog.String("checker", "healthcheck_session")),
		inspector: inspector,
	}
}

// ListChecks evaluates discuss session health for a bot.
func (c *Checker) ListChecks(ctx context.Context, botID string) []healthcheck.CheckResult {
	if err := ctx.Err(); err != nil {
		return []healthcheck.CheckResult{}
	}
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return []healthcheck.CheckResult{}
	}
	if c.inspector == nil {
		return []healthcheck.CheckResult{}
	}

	diags := c.inspector.SessionDiagnosticsForBot(botID)
	if len(diags) == 0 {
		// No active sessions — nothing to check.
		return []healthcheck.CheckResult{}
	}

	var results []healthcheck.CheckResult
	for _, diag := range diags {
		result := c.evaluateSession(diag)
		if result != nil {
			results = append(results, *result)
		}
	}
	return results
}

func (c *Checker) evaluateSession(diag pipeline.SessionDiag) *healthcheck.CheckResult {
	checkID := checkTypeSessionStuck + "." + diag.SessionID

	// Check 1: Session idle for too long — goroutine may be stuck.
	if diag.IdleDuration > stuckThreshold {
		c.logger.Warn("session possibly stuck: long idle",
			slog.String("session_id", diag.SessionID),
			slog.String("bot_id", diag.BotID),
			slog.Duration("idle_duration", diag.IdleDuration),
			slog.Duration("alive_duration", diag.AliveDuration))
		return &healthcheck.CheckResult{
			ID:       checkID,
			Type:     checkTypeSessionStuck,
			TitleKey: titleKeySessionStuck,
			Subtitle: fmt.Sprintf("Session %s", truncateID(diag.SessionID)),
			Status:   healthcheck.StatusError,
			Summary:  "Discuss session appears stuck (no activity for >15min).",
			Detail: fmt.Sprintf(
				"Session started %s ago, last activity %s ago. "+
					"The session goroutine may be blocked on a mutex, LLM call, or HTTP request.",
				formatDuration(diag.AliveDuration), formatDuration(diag.IdleDuration)),
			Metadata: map[string]any{
				"session_id":       diag.SessionID,
				"started_at":       diag.StartedAt.UTC().Format(time.RFC3339),
				"last_activity_at": diag.LastActivityAt.UTC().Format(time.RFC3339),
				"idle_duration_s":  int(diag.IdleDuration.Seconds()),
				"alive_duration_s": int(diag.AliveDuration.Seconds()),
				"queue_depth":      diag.QueueDepth,
			},
		}
	}

	// Check 2: Queue backlog — session goroutine is not consuming fast enough.
	if diag.QueueDepth >= queueBacklogThreshold {
		c.logger.Warn("session queue backlog detected",
			slog.String("session_id", diag.SessionID),
			slog.String("bot_id", diag.BotID),
			slog.Int("queue_depth", diag.QueueDepth),
			slog.Int("queue_capacity", diag.QueueCapacity))
		return &healthcheck.CheckResult{
			ID:       checkID,
			Type:     checkTypeSessionStuck,
			TitleKey: titleKeySessionStuck,
			Subtitle: fmt.Sprintf("Session %s", truncateID(diag.SessionID)),
			Status:   healthcheck.StatusWarn,
			Summary:  fmt.Sprintf("Discuss session has backlogged messages (%d/%d).", diag.QueueDepth, diag.QueueCapacity),
			Detail: fmt.Sprintf(
				"The RC channel has %d pending items (capacity %d). "+
					"The session goroutine may be blocked in an agent call or timing gate evaluation.",
				diag.QueueDepth, diag.QueueCapacity),
			Metadata: map[string]any{
				"session_id":      diag.SessionID,
				"queue_depth":     diag.QueueDepth,
				"queue_capacity":  diag.QueueCapacity,
				"idle_duration_s": int(diag.IdleDuration.Seconds()),
			},
		}
	}

	return nil // Session is healthy
}

func truncateID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}
