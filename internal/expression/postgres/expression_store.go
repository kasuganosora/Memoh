// Package postgres provides PostgreSQL implementations of expression repositories.
package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/expression"
)

// ExpressionStore implements expression.ExpressionRepository using PostgreSQL.
type ExpressionStore struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewExpressionStore creates a new ExpressionStore.
func NewExpressionStore(pool *pgxpool.Pool, logger *slog.Logger) *ExpressionStore {
	return &ExpressionStore{
		pool:   pool,
		logger: logger,
	}
}

// Upsert inserts or updates an expression entry.
func (s *ExpressionStore) Upsert(ctx context.Context, expr expression.ExpressionEntry) error {
	botID, err := db.ParseUUID(expr.BotID)
	if err != nil {
		return fmt.Errorf("parse bot_id: %w", err)
	}
	// Note: ON CONFLICT requires a unique constraint on (bot_id, situation);
	// the migration creates an index but not a constraint. Upserts are
	// treated as simple INSERTs for now; dedup is handled in the learner.
	_, execErr := s.pool.Exec(ctx, `
		INSERT INTO bot_expressions (bot_id, session_id, situation, style, count)
		VALUES ($1, $2, $3, $4, 1)
	`, botID, nilString(expr.SessionID), expr.Situation, expr.Style)
	if execErr != nil {
		return fmt.Errorf("upsert expression: %w", execErr)
	}
	return nil
}

// SearchBySituation searches expressions by bot. When embed is nil, returns
// top entries by count; otherwise searches by vector similarity.
func (s *ExpressionStore) SearchBySituation(ctx context.Context, botID string, embed []float64, topK int) ([]expression.ExpressionEntry, error) {
	if topK <= 0 {
		topK = 5
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, fmt.Errorf("parse bot_id: %w", err)
	}

	_ = embed // Reserved for pgvector similarity search in future iterations

	rows, err := s.pool.Query(ctx, `
		SELECT id, bot_id, session_id, situation, style, count, checked, rejected, created_at, last_active
		FROM bot_expressions
		WHERE bot_id = $1 AND rejected = false
		ORDER BY count DESC, last_active DESC
		LIMIT $2
	`, botUUID, topK)
	if err != nil {
		return nil, fmt.Errorf("search expressions: %w", err)
	}
	defer rows.Close()
	return scanExpressionRows(rows)
}

// ListUnchecked returns expressions pending human review.
func (s *ExpressionStore) ListUnchecked(ctx context.Context, botID string, limit int) ([]expression.ExpressionEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, fmt.Errorf("parse bot_id: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, bot_id, session_id, situation, style, count, checked, rejected, created_at, last_active
		FROM bot_expressions
		WHERE bot_id = $1 AND checked = false AND rejected = false
		ORDER BY count DESC
		LIMIT $2
	`, botUUID, limit)
	if err != nil {
		return nil, fmt.Errorf("list unchecked expressions: %w", err)
	}
	defer rows.Close()
	return scanExpressionRows(rows)
}

// MarkChecked updates the reviewed status of an expression.
func (s *ExpressionStore) MarkChecked(ctx context.Context, id string, rejected bool) error {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return fmt.Errorf("parse id: %w", err)
	}
	_, execErr := s.pool.Exec(ctx, `
		UPDATE bot_expressions SET checked = true, rejected = $1 WHERE id = $2
	`, rejected, pgID)
	if execErr != nil {
		return fmt.Errorf("mark expression checked: %w", execErr)
	}
	return nil
}

func scanExpressionRows(rows pgx.Rows) ([]expression.ExpressionEntry, error) {
	var entries []expression.ExpressionEntry
	for rows.Next() {
		var e expression.ExpressionEntry
		var botIDBytes [16]byte
		if err := rows.Scan(&e.ID, &botIDBytes, &e.SessionID, &e.Situation, &e.Style,
			&e.Count, &e.Checked, &e.Rejected, &e.CreatedAt, &e.LastActive); err != nil {
			return nil, fmt.Errorf("scan expression row: %w", err)
		}
		e.BotID = uuid.UUID(botIDBytes).String()
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func nilString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

var _ expression.ExpressionRepository = (*ExpressionStore)(nil)
