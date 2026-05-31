package heartbeat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/robfig/cron/v3"

	"github.com/memohai/memoh/internal/auth"
	"github.com/memohai/memoh/internal/boot"
	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/db/sqlc"
)

const heartbeatTokenTTL = 10 * time.Minute

// heartbeatRunTimeout caps how long a single heartbeat execution may take.
// This prevents unbounded Generate() calls from hanging forever.
const heartbeatRunTimeout = 5 * time.Minute

// SessionCreator creates sessions for heartbeat runs.
type SessionCreator interface {
	CreateSession(ctx context.Context, botID, sessionType string) (string, error)
}

type Service struct {
	queries        *sqlc.Queries
	cron           *cron.Cron
	triggerer      Triggerer
	sessionCreator SessionCreator
	jwtSecret      string
	logger         *slog.Logger
	mu             sync.Mutex
	jobs           map[string]cron.EntryID
}

func NewService(log *slog.Logger, queries *sqlc.Queries, triggerer Triggerer, sessionCreator SessionCreator, runtimeConfig *boot.RuntimeConfig) *Service {
	c := cron.New()
	service := &Service{
		queries:        queries,
		cron:           c,
		triggerer:      triggerer,
		sessionCreator: sessionCreator,
		jwtSecret:      runtimeConfig.JwtSecret,
		logger:         log.With(slog.String("service", "heartbeat")),
		jobs:           map[string]cron.EntryID{},
	}
	c.Start()
	return service
}

func (s *Service) Bootstrap(ctx context.Context) error {
	if s.queries == nil {
		return errors.New("heartbeat queries not configured")
	}
	rows, err := s.queries.ListHeartbeatEnabledBots(ctx)
	if err != nil {
		// Heartbeat is non-critical — log and continue rather than blocking startup.
		s.logger.Error("heartbeat bootstrap: failed to list enabled bots", slog.Any("error", err))
		return nil
	}
	for _, row := range rows {
		botID := row.ID.String()
		ownerUserID := row.OwnerUserID.String()
		cfg := Config{
			BotID:       botID,
			OwnerUserID: ownerUserID,
			Interval:    int(row.HeartbeatInterval),
		}
		if err := s.scheduleJob(cfg); err != nil { //nolint:contextcheck // cron jobs use background context by design
			s.logger.Error("failed to schedule heartbeat", slog.String("bot_id", botID), slog.Any("error", err))
		}
	}
	s.logger.Info("heartbeat bootstrap complete", slog.Int("count", len(rows)))
	return nil
}

func (s *Service) Reschedule(ctx context.Context, botID string) error {
	s.mu.Lock()
	// Hold the lock across remove + delete to ensure the cron entry removal
	// is atomic. The subsequent scheduleJob call acquires the lock again
	// internally, so we release here to avoid holding it across DB queries.
	entryID, ok := s.jobs[botID]
	if ok {
		s.cron.Remove(entryID)
		delete(s.jobs, botID)
		s.logger.Info("heartbeat job removed for reschedule",
			slog.String("bot_id", botID),
			slog.Int("old_entry_id", int(entryID)))
	}
	s.mu.Unlock()

	pgID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	bot, err := s.queries.GetBotByID(ctx, pgID)
	if err != nil {
		return fmt.Errorf("get bot: %w", err)
	}
	if !bot.HeartbeatEnabled || bot.Status != "ready" {
		s.logger.Info("heartbeat not rescheduled: bot disabled or not ready",
			slog.String("bot_id", botID),
			slog.Bool("heartbeat_enabled", bot.HeartbeatEnabled),
			slog.String("bot_status", bot.Status))
		return nil
	}
	cfg := Config{
		BotID:       botID,
		OwnerUserID: bot.OwnerUserID.String(),
		Interval:    int(bot.HeartbeatInterval),
	}
	return s.scheduleJob(cfg) //nolint:contextcheck // cron jobs use background context by design
}

func (s *Service) Stop(botID string) {
	s.removeJob(botID)
	s.logger.Info("heartbeat stopped", slog.String("bot_id", botID))
}

// Shutdown stops the cron scheduler and waits for any running heartbeat jobs
// to complete. Should be called during graceful shutdown.
func (s *Service) Shutdown() context.Context {
	s.mu.Lock()
	activeCount := len(s.jobs)
	s.mu.Unlock()
	s.logger.Info("heartbeat shutting down",
		slog.Int("active_jobs", activeCount))
	return s.cron.Stop()
}

func (s *Service) runHeartbeat(ctx context.Context, cfg Config) {
	start := time.Now()
	log := s.logger.With(
		slog.String("bot_id", cfg.BotID),
		slog.Int("interval_minutes", cfg.Interval),
	)
	log.Info("heartbeat run started")

	if s.triggerer == nil {
		log.Error("heartbeat triggerer not configured")
		return
	}

	pgBotID, err := db.ParseUUID(cfg.BotID)
	if err != nil {
		log.Error("invalid bot id", slog.Any("error", err))
		return
	}

	var sessionID string
	var pgSessionID pgtype.UUID
	if s.sessionCreator != nil {
		sid, err := s.sessionCreator.CreateSession(ctx, cfg.BotID, "heartbeat")
		if err != nil {
			log.Error("create heartbeat session failed", slog.Any("error", err))
		} else {
			sessionID = sid
			pgSessionID = db.ParseUUIDOrEmpty(sid)
			log.Debug("heartbeat session created", slog.String("session_id", sid))
		}
	}

	var lastHeartbeatAt string
	if prevLogs, listErr := s.queries.ListHeartbeatLogsByBot(ctx, sqlc.ListHeartbeatLogsByBotParams{
		BotID: pgBotID,
		Limit: 1,
	}); listErr == nil && len(prevLogs) > 0 {
		lastHeartbeatAt = prevLogs[0].StartedAt.Time.UTC().Format("2006-01-02T15:04:05Z")
	}

	logRow, err := s.queries.CreateHeartbeatLog(ctx, sqlc.CreateHeartbeatLogParams{
		BotID:     pgBotID,
		SessionID: pgSessionID,
	})
	if err != nil {
		log.Error("create heartbeat log failed", slog.Any("error", err))
		return
	}

	token, err := s.generateTriggerToken(cfg.OwnerUserID)
	if err != nil {
		s.completeLog(ctx, logRow.ID, "error", "", err.Error(), nil, pgtype.UUID{})
		log.Error("generate trigger token failed", slog.Any("error", err))
		return
	}

	log.Debug("triggering heartbeat",
		slog.String("session_id", sessionID),
		slog.String("last_heartbeat_at", lastHeartbeatAt))

	result, err := s.triggerer.TriggerHeartbeat(ctx, cfg.BotID, TriggerPayload{
		BotID:           cfg.BotID,
		Interval:        cfg.Interval,
		OwnerUserID:     cfg.OwnerUserID,
		SessionID:       sessionID,
		LastHeartbeatAt: lastHeartbeatAt,
	}, token)
	if err != nil {
		s.completeLog(ctx, logRow.ID, "error", "", err.Error(), nil, pgtype.UUID{})
		log.Error("heartbeat trigger failed",
			slog.Any("error", err),
			slog.Duration("duration", time.Since(start)))
		return
	}

	modelID := db.ParseUUIDOrEmpty(result.ModelID)
	s.completeLog(ctx, logRow.ID, result.Status, result.Text, "", result.UsageBytes, modelID)
	log.Info("heartbeat run completed",
		slog.String("status", result.Status),
		slog.String("session_id", sessionID),
		slog.String("model_id", result.ModelID),
		slog.Int("result_len", len(result.Text)),
		slog.Duration("duration", time.Since(start)))
}

func (s *Service) completeLog(ctx context.Context, logID pgtype.UUID, status, resultText, errorMessage string, usageBytes []byte, modelID pgtype.UUID) {
	_, err := s.queries.CompleteHeartbeatLog(ctx, sqlc.CompleteHeartbeatLogParams{
		ID:           logID,
		Status:       status,
		ResultText:   resultText,
		ErrorMessage: errorMessage,
		Usage:        usageBytes,
		ModelID:      modelID,
	})
	if err != nil {
		s.logger.Error("complete heartbeat log failed",
			slog.String("log_id", logID.String()),
			slog.String("status", status),
			slog.Any("error", err))
	}
}

func (s *Service) ListLogs(ctx context.Context, botID string, limit, offset int) ([]Log, int64, error) {
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	total, err := s.queries.CountHeartbeatLogsByBot(ctx, pgBotID)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.queries.ListHeartbeatLogsByBot(ctx, sqlc.ListHeartbeatLogsByBotParams{
		BotID:  pgBotID,
		Limit:  int32(limit),  //nolint:gosec // capped to 100 above
		Offset: int32(offset), //nolint:gosec // validated above
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]Log, 0, len(rows))
	for _, row := range rows {
		items = append(items, toLog(row))
	}
	return items, total, nil
}

func (s *Service) DeleteLogs(ctx context.Context, botID string) error {
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	err = s.queries.DeleteHeartbeatLogsByBot(ctx, pgBotID)
	if err != nil {
		s.logger.Error("delete heartbeat logs failed",
			slog.String("bot_id", botID),
			slog.Any("error", err))
	} else {
		s.logger.Info("heartbeat logs deleted", slog.String("bot_id", botID))
	}
	return err
}

// HeartbeatStatus holds heartbeat scheduling info for health check.
type HeartbeatStatus struct {
	Enabled         bool
	IntervalMinutes int
	LastRunAt       time.Time
	LastStatus      string
	LastError       string
}

// HeartbeatStatusForBot returns the heartbeat scheduling status for a bot.
// Used by the healthcheck subsystem to detect overdue/stuck heartbeats.
func (s *Service) HeartbeatStatusForBot(ctx context.Context, botID string) (HeartbeatStatus, error) {
	pgID, err := db.ParseUUID(botID)
	if err != nil {
		return HeartbeatStatus{}, err
	}
	bot, err := s.queries.GetBotByID(ctx, pgID)
	if err != nil {
		return HeartbeatStatus{}, fmt.Errorf("get bot: %w", err)
	}
	status := HeartbeatStatus{
		Enabled:         bot.HeartbeatEnabled,
		IntervalMinutes: int(bot.HeartbeatInterval),
	}
	if !bot.HeartbeatEnabled {
		return status, nil
	}

	// Get latest log entry.
	logs, err := s.queries.ListHeartbeatLogsByBot(ctx, sqlc.ListHeartbeatLogsByBotParams{
		BotID: pgID,
		Limit: 1,
	})
	if err != nil {
		return status, nil // non-critical — return what we have
	}
	if len(logs) > 0 {
		if logs[0].StartedAt.Valid {
			status.LastRunAt = logs[0].StartedAt.Time
		}
		status.LastStatus = logs[0].Status
		status.LastError = logs[0].ErrorMessage
	}
	return status, nil
}

func (s *Service) generateTriggerToken(userID string) (string, error) {
	if strings.TrimSpace(s.jwtSecret) == "" {
		return "", errors.New("jwt secret not configured")
	}
	signed, _, err := auth.GenerateToken(userID, s.jwtSecret, heartbeatTokenTTL)
	if err != nil {
		return "", err
	}
	return "Bearer " + signed, nil
}

func (s *Service) scheduleJob(cfg Config) error {
	if cfg.Interval <= 0 {
		cfg.Interval = 30
	}
	spec := fmt.Sprintf("@every %dm", cfg.Interval)
	job := func() {
		runCtx, runCancel := context.WithTimeout(context.Background(), heartbeatRunTimeout)
		defer runCancel()
		s.runHeartbeat(runCtx, cfg)
	}
	entryID, err := s.cron.AddFunc(spec, job)
	if err != nil {
		return fmt.Errorf("add heartbeat cron job: %w", err)
	}
	s.mu.Lock()
	s.jobs[cfg.BotID] = entryID
	s.mu.Unlock()
	s.logger.Info("heartbeat scheduled",
		slog.String("bot_id", cfg.BotID),
		slog.Int("interval_minutes", cfg.Interval),
		slog.String("cron_spec", spec),
		slog.Int("entry_id", int(entryID)))
	return nil
}

func (s *Service) removeJob(botID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entryID, ok := s.jobs[botID]
	if ok {
		s.cron.Remove(entryID)
		delete(s.jobs, botID)
		s.logger.Debug("heartbeat cron entry removed",
			slog.String("bot_id", botID),
			slog.Int("entry_id", int(entryID)))
	}
}

func toLog(row sqlc.ListHeartbeatLogsByBotRow) Log {
	l := Log{
		ID:           row.ID.String(),
		BotID:        row.BotID.String(),
		Status:       row.Status,
		ResultText:   row.ResultText,
		ErrorMessage: row.ErrorMessage,
	}
	if row.SessionID.Valid {
		l.SessionID = row.SessionID.String()
	}
	if row.StartedAt.Valid {
		l.StartedAt = row.StartedAt.Time
	}
	if row.CompletedAt.Valid {
		t := row.CompletedAt.Time
		l.CompletedAt = &t
	}
	if row.Usage != nil {
		var usage any
		if err := json.Unmarshal(row.Usage, &usage); err == nil {
			l.Usage = usage
		}
	}
	return l
}
