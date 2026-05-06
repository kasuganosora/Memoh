package workflow

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	sqlc "github.com/memohai/memoh/internal/db/sqlc"
)

// Repository wraps sqlc queries for pipeline persistence.
// Using an interface enables testability via mock implementations.
type Repository interface {
	// Pipeline CRUD
	CreatePipeline(ctx context.Context, botID uuid.UUID, goal string, status Status) (Pipeline, error)
	GetPipeline(ctx context.Context, id uuid.UUID) (Pipeline, error)
	ListPipelinesByBot(ctx context.Context, botID uuid.UUID, limit, offset int32) ([]Pipeline, error)
	UpdatePipelineStatus(ctx context.Context, id uuid.UUID, status Status) (Pipeline, error)
	DeletePipeline(ctx context.Context, id uuid.UUID) error

	// Node CRUD
	CreateNode(ctx context.Context, pipelineID uuid.UUID, input CreateNodeInput) (Node, error)
	GetNode(ctx context.Context, id uuid.UUID) (Node, error)
	ListNodesByPipeline(ctx context.Context, pipelineID uuid.UUID) ([]Node, error)
	UpdateNodeStatus(ctx context.Context, id uuid.UUID, status Status) (Node, error)
	UpdateNodeOutput(ctx context.Context, id uuid.UUID, output json.RawMessage, status Status) (Node, error)
	UpdateNodeError(ctx context.Context, id uuid.UUID, errMsg string) (Node, error)
	IncrementNodeRetry(ctx context.Context, id uuid.UUID) (Node, error)
	UpdateNodeReview(ctx context.Context, id uuid.UUID, result, feedback string) (Node, error)
	DeleteNodesByPipeline(ctx context.Context, pipelineID uuid.UUID) error
}

// SQLRepository is the PostgreSQL-backed implementation of Repository.
type SQLRepository struct {
	queries *sqlc.Queries
	logger  *slog.Logger
}

// NewSQLRepository creates a new SQL-backed pipeline repository.
func NewSQLRepository(queries *sqlc.Queries, logger *slog.Logger) *SQLRepository {
	return &SQLRepository{
		queries: queries,
		logger:  logger.With(slog.String("component", "workflow.repository")),
	}
}

// --- Pipeline CRUD ---

func (r *SQLRepository) CreatePipeline(ctx context.Context, botID uuid.UUID, goal string, status Status) (Pipeline, error) {
	row, err := r.queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
		BotID:  toPgUUID(botID),
		Goal:   goal,
		Status: string(status),
	})
	if err != nil {
		return Pipeline{}, err
	}
	return toPipeline(row), nil
}

func (r *SQLRepository) GetPipeline(ctx context.Context, id uuid.UUID) (Pipeline, error) {
	row, err := r.queries.GetPipeline(ctx, toPgUUID(id))
	if err != nil {
		return Pipeline{}, err
	}
	return toPipeline(row), nil
}

func (r *SQLRepository) ListPipelinesByBot(ctx context.Context, botID uuid.UUID, limit, offset int32) ([]Pipeline, error) {
	rows, err := r.queries.ListPipelinesByBot(ctx, sqlc.ListPipelinesByBotParams{
		BotID:  toPgUUID(botID),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return nil, err
	}
	pipelines := make([]Pipeline, 0, len(rows))
	for _, row := range rows {
		pipelines = append(pipelines, toPipeline(row))
	}
	return pipelines, nil
}

func (r *SQLRepository) UpdatePipelineStatus(ctx context.Context, id uuid.UUID, status Status) (Pipeline, error) {
	row, err := r.queries.UpdatePipelineStatus(ctx, sqlc.UpdatePipelineStatusParams{
		ID:     toPgUUID(id),
		Status: string(status),
	})
	if err != nil {
		return Pipeline{}, err
	}
	return toPipeline(row), nil
}

func (r *SQLRepository) DeletePipeline(ctx context.Context, id uuid.UUID) error {
	return r.queries.DeletePipeline(ctx, toPgUUID(id))
}

// --- Node CRUD ---

func (r *SQLRepository) CreateNode(ctx context.Context, pipelineID uuid.UUID, input CreateNodeInput) (Node, error) {
	row, err := r.queries.CreatePipelineNode(ctx, sqlc.CreatePipelineNodeParams{
		PipelineID:     toPgUUID(pipelineID),
		Name:           input.Name,
		Description:    toPgText(input.Description),
		DependsOn:      toPgUUIDSlice(input.DependsOn),
		ModelTier:      string(input.ModelTier),
		Status:         string(StatusPending),
		Input:          input.Input,
		MaxRetries:     input.MaxRetries,
		TimeoutSeconds: input.TimeoutSeconds,
		NeedsReview:    input.NeedsReview,
	})
	if err != nil {
		return Node{}, err
	}
	return toNode(row), nil
}

func (r *SQLRepository) GetNode(ctx context.Context, id uuid.UUID) (Node, error) {
	row, err := r.queries.GetNode(ctx, toPgUUID(id))
	if err != nil {
		return Node{}, err
	}
	return toNode(row), nil
}

func (r *SQLRepository) ListNodesByPipeline(ctx context.Context, pipelineID uuid.UUID) ([]Node, error) {
	rows, err := r.queries.ListNodesByPipeline(ctx, toPgUUID(pipelineID))
	if err != nil {
		return nil, err
	}
	nodes := make([]Node, 0, len(rows))
	for _, row := range rows {
		nodes = append(nodes, toNode(row))
	}
	return nodes, nil
}

func (r *SQLRepository) UpdateNodeStatus(ctx context.Context, id uuid.UUID, status Status) (Node, error) {
	row, err := r.queries.UpdateNodeStatus(ctx, sqlc.UpdateNodeStatusParams{
		ID:     toPgUUID(id),
		Status: string(status),
	})
	if err != nil {
		return Node{}, err
	}
	return toNode(row), nil
}

func (r *SQLRepository) UpdateNodeOutput(ctx context.Context, id uuid.UUID, output json.RawMessage, status Status) (Node, error) {
	row, err := r.queries.UpdateNodeOutput(ctx, sqlc.UpdateNodeOutputParams{
		ID:     toPgUUID(id),
		Output: []byte(output),
		Status: string(status),
	})
	if err != nil {
		return Node{}, err
	}
	return toNode(row), nil
}

func (r *SQLRepository) UpdateNodeError(ctx context.Context, id uuid.UUID, errMsg string) (Node, error) {
	row, err := r.queries.UpdateNodeError(ctx, sqlc.UpdateNodeErrorParams{
		ID:    toPgUUID(id),
		Error: toPgText(&errMsg),
	})
	if err != nil {
		return Node{}, err
	}
	return toNode(row), nil
}

func (r *SQLRepository) IncrementNodeRetry(ctx context.Context, id uuid.UUID) (Node, error) {
	row, err := r.queries.IncrementNodeRetry(ctx, toPgUUID(id))
	if err != nil {
		return Node{}, err
	}
	return toNode(row), nil
}

func (r *SQLRepository) UpdateNodeReview(ctx context.Context, id uuid.UUID, result, feedback string) (Node, error) {
	row, err := r.queries.UpdateNodeReview(ctx, sqlc.UpdateNodeReviewParams{
		ID:             toPgUUID(id),
		ReviewResult:   toPgText(&result),
		ReviewFeedback: toPgText(&feedback),
	})
	if err != nil {
		return Node{}, err
	}
	return toNode(row), nil
}

func (r *SQLRepository) DeleteNodesByPipeline(ctx context.Context, pipelineID uuid.UUID) error {
	return r.queries.DeleteNodesByPipeline(ctx, toPgUUID(pipelineID))
}

// --- Conversion helpers ---

func toPipeline(row sqlc.Pipeline) Pipeline {
	return Pipeline{
		ID:        row.ID.Bytes,
		BotID:     row.BotID.Bytes,
		Goal:      row.Goal,
		Status:    Status(row.Status),
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
	}
}

func toNode(row sqlc.PipelineNode) Node {
	return Node{
		ID:             row.ID.Bytes,
		PipelineID:     row.PipelineID.Bytes,
		Name:           row.Name,
		Description:    pgTextToString(row.Description),
		DependsOn:      pgUUIDSliceToUUIDs(row.DependsOn),
		ModelTier:      ModelTier(row.ModelTier),
		Status:         Status(row.Status),
		Input:          json.RawMessage(row.Input),
		Output:         json.RawMessage(row.Output),
		Error:          pgTextToString(row.Error),
		RetryCount:     row.RetryCount,
		MaxRetries:     row.MaxRetries,
		TimeoutSeconds: row.TimeoutSeconds,
		NeedsReview:    row.NeedsReview,
		ReviewResult:   pgTextToString(row.ReviewResult),
		ReviewFeedback: pgTextToString(row.ReviewFeedback),
		CreatedAt:      row.CreatedAt.Time,
		UpdatedAt:      row.UpdatedAt.Time,
		StartedAt:      pgTimestamptzToTime(row.StartedAt),
		CompletedAt:    pgTimestamptzToTime(row.CompletedAt),
	}
}

func toPgUUID(id uuid.UUID) pgtype.UUID {
	var pg pgtype.UUID
	pg.Bytes = id
	pg.Valid = true
	return pg
}

func toPgText(s *string) pgtype.Text {
	var t pgtype.Text
	if s != nil {
		t.String = *s
		t.Valid = true
	}
	return t
}

func toPgUUIDSlice(ids []uuid.UUID) []pgtype.UUID {
	if ids == nil {
		return []pgtype.UUID{}
	}
	result := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		result = append(result, toPgUUID(id))
	}
	return result
}

func pgTextToString(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func pgUUIDSliceToUUIDs(ids []pgtype.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	result := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id.Valid {
			result = append(result, id.Bytes)
		}
	}
	return result
}

func pgTimestamptzToTime(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	return &ts.Time
}
