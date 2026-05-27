package scene

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresBackend implements SceneBackend using PostgreSQL.
// Scenes are stored in the `scenes` table. No vector operations needed
// since scenes are retrieved by bot_id filter, not similarity search.
type PostgresBackend struct {
	pool *pgxpool.Pool
}

// NewPostgresBackend creates a new PostgreSQL-backed scene backend.
func NewPostgresBackend(pool *pgxpool.Pool) *PostgresBackend {
	return &PostgresBackend{pool: pool}
}

func (b *PostgresBackend) UpsertScene(ctx context.Context, id string, payload map[string]any) error {
	memoryIDsJSON, _ := json.Marshal(payloadStringSlice(payload, "memory_ids"))

	var timeRangeStart, timeRangeEnd *time.Time
	if ts, ok := payload["time_range_start"].(string); ok && ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			timeRangeStart = &t
		}
	}
	if ts, ok := payload["time_range_end"].(string); ok && ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			timeRangeEnd = &t
		}
	}

	_, err := b.pool.Exec(ctx, `
		INSERT INTO scenes (id, bot_id, title, summary, heat_score, time_range_start, time_range_end, memory_ids, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (id) DO UPDATE SET
			title = EXCLUDED.title,
			summary = EXCLUDED.summary,
			heat_score = EXCLUDED.heat_score,
			time_range_start = EXCLUDED.time_range_start,
			time_range_end = EXCLUDED.time_range_end,
			memory_ids = EXCLUDED.memory_ids,
			updated_at = EXCLUDED.updated_at
	`,
		id,
		payloadString(payload, "bot_id"),
		payloadString(payload, "title"),
		payloadString(payload, "summary"),
		payloadFloat64(payload, "heat_score"),
		timeRangeStart,
		timeRangeEnd,
		memoryIDsJSON,
		parseTimeOrNow(payload, "created_at"),
		parseTimeOrNow(payload, "updated_at"),
	)
	if err != nil {
		return fmt.Errorf("postgres scene upsert: %w", err)
	}
	return nil
}

func (b *PostgresBackend) GetScene(ctx context.Context, id string) (map[string]any, error) {
	row := b.pool.QueryRow(ctx, `
		SELECT id, bot_id, title, summary, heat_score, time_range_start, time_range_end, memory_ids, created_at, updated_at
		FROM scenes WHERE id = $1
	`, id)

	var (
		sceneID, botID, title, summary string
		heatScore                      float64
		timeRangeStart, timeRangeEnd   *time.Time
		memoryIDsJSON                  []byte
		createdAt, updatedAt           time.Time
	)
	err := row.Scan(&sceneID, &botID, &title, &summary, &heatScore, &timeRangeStart, &timeRangeEnd, &memoryIDsJSON, &createdAt, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("postgres scene get: %w", err)
	}

	return rowToPayload(sceneID, botID, title, summary, heatScore, timeRangeStart, timeRangeEnd, memoryIDsJSON, createdAt, updatedAt), nil
}

func (b *PostgresBackend) ListScenes(ctx context.Context, botID string) ([]map[string]any, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT id, bot_id, title, summary, heat_score, time_range_start, time_range_end, memory_ids, created_at, updated_at
		FROM scenes WHERE bot_id = $1
		ORDER BY heat_score DESC
	`, botID)
	if err != nil {
		return nil, fmt.Errorf("postgres scene list: %w", err)
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var (
			sceneID, bID, title, summary string
			heatScore                    float64
			timeRangeStart, timeRangeEnd *time.Time
			memoryIDsJSON                []byte
			createdAt, updatedAt         time.Time
		)
		if err := rows.Scan(&sceneID, &bID, &title, &summary, &heatScore, &timeRangeStart, &timeRangeEnd, &memoryIDsJSON, &createdAt, &updatedAt); err != nil {
			continue
		}
		results = append(results, rowToPayload(sceneID, bID, title, summary, heatScore, timeRangeStart, timeRangeEnd, memoryIDsJSON, createdAt, updatedAt))
	}
	return results, nil
}

func (b *PostgresBackend) DeleteScene(ctx context.Context, id string) error {
	_, err := b.pool.Exec(ctx, `DELETE FROM scenes WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres scene delete: %w", err)
	}
	return nil
}

// --- Helpers ---

func rowToPayload(id, botID, title, summary string, heatScore float64, start, end *time.Time, memoryIDsJSON []byte, createdAt, updatedAt time.Time) map[string]any {
	startStr := ""
	if start != nil {
		startStr = start.Format(time.RFC3339)
	}
	endStr := ""
	if end != nil {
		endStr = end.Format(time.RFC3339)
	}
	return map[string]any{
		"type":             "scene",
		"id":               id,
		"bot_id":           botID,
		"title":            title,
		"summary":          summary,
		"heat_score":       heatScore,
		"time_range_start": startStr,
		"time_range_end":   endStr,
		"memory_ids":       string(memoryIDsJSON),
		"created_at":       createdAt.Format(time.RFC3339),
		"updated_at":       updatedAt.Format(time.RFC3339),
	}
}

func payloadString(p map[string]any, key string) string {
	v, _ := p[key].(string)
	return v
}

func payloadFloat64(p map[string]any, key string) float64 {
	v, _ := p[key].(float64)
	return v
}

func payloadStringSlice(p map[string]any, key string) []string {
	raw := payloadString(p, key)
	if raw == "" {
		return nil
	}
	var ids []string
	_ = json.Unmarshal([]byte(raw), &ids)
	return ids
}

func parseTimeOrNow(p map[string]any, key string) time.Time {
	if ts, ok := p[key].(string); ok && ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			return t
		}
	}
	return time.Now().UTC()
}
