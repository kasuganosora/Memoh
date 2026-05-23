package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/db/sqlc"
)

// EventStore persists and loads CanonicalEvents from the database.
type EventStore struct {
	queries *sqlc.Queries
	logger  *slog.Logger
}

// NewEventStore creates an EventStore.
func NewEventStore(log *slog.Logger, queries *sqlc.Queries) *EventStore {
	if log == nil {
		log = slog.Default()
	}
	return &EventStore{
		queries: queries,
		logger:  log.With(slog.String("service", "pipeline_event_store")),
	}
}

// PersistEvent writes a CanonicalEvent to the bot_session_events table.
// Returns the UUID of the persisted event row, or empty string if the event
// was a duplicate (ON CONFLICT DO NOTHING).
func (s *EventStore) PersistEvent(ctx context.Context, botID, sessionID string, event CanonicalEvent) (string, error) {
	if event == nil {
		return "", errors.New("pipeline: cannot persist nil event")
	}

	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return "", fmt.Errorf("invalid bot id: %w", err)
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return "", fmt.Errorf("invalid session id: %w", err)
	}

	eventData, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshal event data: %w", err)
	}

	externalMessageID := extractExternalMessageID(event)
	senderID := extractSenderChannelIdentityID(event)

	pgExternalMsgID := pgtype.Text{}
	if externalMessageID != "" {
		pgExternalMsgID = pgtype.Text{String: externalMessageID, Valid: true}
	}

	pgSenderID := pgtype.UUID{}
	if senderID != "" {
		if parsed, parseErr := dbpkg.ParseUUID(senderID); parseErr == nil {
			pgSenderID = parsed
		}
	}

	pgID, err := s.queries.CreateSessionEvent(ctx, sqlc.CreateSessionEventParams{
		BotID:                   pgBotID,
		SessionID:               pgSessionID,
		EventKind:               string(event.Kind()),
		EventData:               eventData,
		ExternalMessageID:       pgExternalMsgID,
		SenderChannelIdentityID: pgSenderID,
		ReceivedAtMs:            event.GetReceivedAtMs(),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("persist session event: %w", err)
	}

	if pgID.Valid {
		return pgID.String(), nil
	}
	return "", nil
}

// LoadEvents loads all events for a session, ordered by received_at_ms.
func (s *EventStore) LoadEvents(ctx context.Context, sessionID string) ([]CanonicalEvent, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, fmt.Errorf("invalid session id: %w", err)
	}

	rows, err := s.queries.ListSessionEventsBySession(ctx, pgSessionID)
	if err != nil {
		return nil, fmt.Errorf("list session events: %w", err)
	}

	events := make([]CanonicalEvent, 0, len(rows))
	skipped := 0
	for _, row := range rows {
		event, parseErr := parseEventData(row.EventKind, row.EventData)
		if parseErr != nil {
			s.logger.Warn("skip unparseable event",
				slog.String("session_id", sessionID),
				slog.String("event_id", row.ID.String()),
				slog.Any("error", parseErr))
			skipped++
			continue
		}
		events = append(events, event)
	}

	if skipped > 0 {
		s.logger.Info("loaded session events",
			slog.String("session_id", sessionID),
			slog.Int("total", len(rows)),
			slog.Int("loaded", len(events)),
			slog.Int("skipped", skipped))
	}

	return events, nil
}

// LoadRecentEvents loads the most recent N events for a session, returned in
// chronological order (oldest first). This is more efficient than LoadEvents
// for cold-start recovery when sessions have accumulated many events, since
// the Pipeline's trimICNodes will discard excess nodes anyway.
func (s *EventStore) LoadRecentEvents(ctx context.Context, sessionID string, limit int) ([]CanonicalEvent, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return nil, fmt.Errorf("invalid session id: %w", err)
	}

	// Cap limit to prevent int32 overflow.
	if limit > int(^int32(0)) {
		limit = int(^int32(0))
	}

	rows, err := s.queries.ListRecentSessionEvents(ctx, sqlc.ListRecentSessionEventsParams{
		SessionID: pgSessionID,
		Limit:     int32(limit), //nolint:gosec // bounded above
	})
	if err != nil {
		return nil, fmt.Errorf("list recent session events: %w", err)
	}

	// Rows come in DESC order from the query; reverse to get chronological ASC.
	events := make([]CanonicalEvent, 0, len(rows))
	skipped := 0
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		event, parseErr := parseEventData(row.EventKind, row.EventData)
		if parseErr != nil {
			s.logger.Warn("skip unparseable event",
				slog.String("session_id", sessionID),
				slog.String("event_id", row.ID.String()),
				slog.Any("error", parseErr))
			skipped++
			continue
		}
		events = append(events, event)
	}

	if skipped > 0 || len(rows) == limit {
		s.logger.Info("loaded recent session events",
			slog.String("session_id", sessionID),
			slog.Int("limit", limit),
			slog.Int("total_rows", len(rows)),
			slog.Int("loaded", len(events)),
			slog.Int("skipped", skipped))
	}

	return events, nil
}

// HasEvents checks whether a session has any events persisted.
func (s *EventStore) HasEvents(ctx context.Context, sessionID string) (bool, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return false, fmt.Errorf("invalid session id: %w", err)
	}

	count, err := s.queries.CountSessionEvents(ctx, pgSessionID)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func parseEventData(kind string, data []byte) (CanonicalEvent, error) {
	switch EventKind(kind) {
	case EventMessage:
		var e MessageEvent
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		return e, nil
	case EventEdit:
		var e EditEvent
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		return e, nil
	case EventDelete:
		var e DeleteEvent
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		return e, nil
	case EventService:
		var e ServiceEvent
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		return e, nil
	default:
		return nil, fmt.Errorf("unknown event kind: %s", kind)
	}
}

func extractExternalMessageID(event CanonicalEvent) string {
	switch e := event.(type) {
	case MessageEvent:
		return strings.TrimSpace(e.MessageID)
	case EditEvent:
		return strings.TrimSpace(e.MessageID)
	default:
		return ""
	}
}

func extractSenderChannelIdentityID(event CanonicalEvent) string {
	switch e := event.(type) {
	case MessageEvent:
		if e.Sender != nil {
			return strings.TrimSpace(e.Sender.ID)
		}
	case EditEvent:
		if e.Sender != nil {
			return strings.TrimSpace(e.Sender.ID)
		}
	case ServiceEvent:
		if e.Actor != nil {
			return strings.TrimSpace(e.Actor.ID)
		}
	}
	return ""
}
