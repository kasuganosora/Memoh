package heartbeatchecker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/memohai/memoh/internal/healthcheck"
)

const (
	checkTypeHeartbeat = "heartbeat.schedule"
	titleKeyHeartbeat  = "bots.checks.titles.heartbeatSchedule"

	// overdueMultiplier: if last heartbeat was more than interval*2.5 ago,
	// the heartbeat is considered overdue (missed at least 1 run).
	overdueMultiplier = 2.5
)

// HeartbeatStatus holds heartbeat scheduling info for a bot.
type HeartbeatStatus struct {
	Enabled         bool
	IntervalMinutes int
	LastRunAt       time.Time // zero if never run
	LastStatus      string    // "completed", "error", or empty
	LastError       string
}

// HeartbeatInspector provides heartbeat scheduling info for a bot.
type HeartbeatInspector interface {
	HeartbeatStatusForBot(ctx context.Context, botID string) (HeartbeatStatus, error)
}

// Checker evaluates heartbeat schedule health.
type Checker struct {
	logger    *slog.Logger
	inspector HeartbeatInspector
}

// NewChecker creates a heartbeat health checker.
func NewChecker(log *slog.Logger, inspector HeartbeatInspector) *Checker {
	if log == nil {
		log = slog.Default()
	}
	return &Checker{
		logger:    log.With(slog.String("checker", "healthcheck_heartbeat")),
		inspector: inspector,
	}
}

// ListChecks evaluates heartbeat schedule health for a bot.
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

	status, err := c.inspector.HeartbeatStatusForBot(ctx, botID)
	if err != nil {
		c.logger.Warn("heartbeat healthcheck failed to get status",
			slog.String("bot_id", botID),
			slog.Any("error", err))
		return []healthcheck.CheckResult{}
	}
	if !status.Enabled {
		// Heartbeat disabled — nothing to check.
		return []healthcheck.CheckResult{}
	}

	checkID := checkTypeHeartbeat + "." + botID
	result := healthcheck.CheckResult{
		ID:       checkID,
		Type:     checkTypeHeartbeat,
		TitleKey: titleKeyHeartbeat,
		Subtitle: fmt.Sprintf("Every %dm", status.IntervalMinutes),
		Metadata: map[string]any{
			"interval_minutes": status.IntervalMinutes,
			"enabled":          true,
		},
	}

	// Never run yet — if bot is relatively new this is OK, otherwise warn.
	if status.LastRunAt.IsZero() {
		result.Status = healthcheck.StatusWarn
		result.Summary = "Heartbeat has never run yet."
		result.Detail = "The heartbeat schedule is enabled but no execution has been recorded."
		return []healthcheck.CheckResult{result}
	}

	result.Metadata["last_run_at"] = status.LastRunAt.UTC().Format(time.RFC3339)
	result.Metadata["last_status"] = status.LastStatus

	// Check if overdue.
	interval := time.Duration(status.IntervalMinutes) * time.Minute
	overdueThreshold := time.Duration(float64(interval) * overdueMultiplier)
	elapsed := time.Since(status.LastRunAt)

	if elapsed > overdueThreshold {
		result.Status = healthcheck.StatusError
		result.Summary = fmt.Sprintf("Heartbeat is overdue (last run %s ago, expected every %dm).",
			formatDuration(elapsed), status.IntervalMinutes)
		result.Detail = fmt.Sprintf(
			"Expected interval: %dm. Elapsed since last run: %s. "+
				"The cron job may have been lost (process restart without re-bootstrap) "+
				"or the heartbeat trigger may be stuck.",
			status.IntervalMinutes, formatDuration(elapsed))
		result.Metadata["overdue_by_s"] = int((elapsed - interval).Seconds())
		c.logger.Warn("heartbeat overdue",
			slog.String("bot_id", botID),
			slog.Duration("elapsed", elapsed),
			slog.Duration("expected_interval", interval))
		return []healthcheck.CheckResult{result}
	}

	// Last run error.
	if status.LastStatus == "error" {
		result.Status = healthcheck.StatusWarn
		result.Summary = "Last heartbeat run failed."
		result.Detail = status.LastError
		return []healthcheck.CheckResult{result}
	}

	// Healthy.
	result.Status = healthcheck.StatusOK
	result.Summary = fmt.Sprintf("Heartbeat is on schedule (last run %s ago).", formatDuration(elapsed))
	return []healthcheck.CheckResult{result}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}
