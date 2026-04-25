package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/expression"
)

// JargonStore implements expression.JargonRepository using PostgreSQL.
type JargonStore struct {
	pool *pgxpool.Pool
}

// NewJargonStore creates a new JargonStore.
func NewJargonStore(pool *pgxpool.Pool) *JargonStore {
	return &JargonStore{pool: pool}
}

// Upsert inserts or updates a jargon entry.
func (s *JargonStore) Upsert(ctx context.Context, j expression.JargonEntry) error {
	botID, err := db.ParseUUID(j.BotID)
	if err != nil {
		return fmt.Errorf("parse bot_id: %w", err)
	}
	_, execErr := s.pool.Exec(ctx, `
		INSERT INTO bot_jargons (bot_id, session_id, content, meaning, count)
		VALUES ($1, $2, $3, $4, 1)
		ON CONFLICT (bot_id, content) DO UPDATE
		SET count = bot_jargons.count + 1,
		    meaning = COALESCE(NULLIF($4, ''), bot_jargons.meaning)
	`, botID, nilString(j.SessionID), j.Content, j.Meaning)
	if execErr != nil {
		return fmt.Errorf("upsert jargon: %w", execErr)
	}
	return nil
}

// Query looks up jargon entries matching the given words.
func (s *JargonStore) Query(ctx context.Context, botID string, words []string) ([]expression.JargonEntry, error) {
	if len(words) == 0 {
		return nil, nil
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, fmt.Errorf("parse bot_id: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, bot_id, session_id, content, meaning, count, created_at
		FROM bot_jargons
		WHERE bot_id = $1 AND content = ANY($2)
		ORDER BY count DESC
	`, botUUID, words)
	if err != nil {
		return nil, fmt.Errorf("query jargons: %w", err)
	}
	defer rows.Close()
	return scanJargonRows(rows)
}

// List returns the most popular jargon entries for a bot.
func (s *JargonStore) List(ctx context.Context, botID string, limit int) ([]expression.JargonEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, fmt.Errorf("parse bot_id: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, bot_id, session_id, content, meaning, count, created_at
		FROM bot_jargons
		WHERE bot_id = $1
		ORDER BY count DESC
		LIMIT $2
	`, botUUID, limit)
	if err != nil {
		return nil, fmt.Errorf("list jargons: %w", err)
	}
	defer rows.Close()
	return scanJargonRows(rows)
}

func scanJargonRows(rows pgx.Rows) ([]expression.JargonEntry, error) {
	var entries []expression.JargonEntry
	for rows.Next() {
		var j expression.JargonEntry
		if err := rows.Scan(&j.ID, &j.BotID, &j.SessionID, &j.Content, &j.Meaning,
			&j.Count, &j.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan jargon row: %w", err)
		}
		entries = append(entries, j)
	}
	return entries, rows.Err()
}

var _ expression.JargonRepository = (*JargonStore)(nil)
