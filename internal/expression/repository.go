package expression

import "context"

// ExpressionRepository defines persistence operations for expression entries.
type ExpressionRepository interface {
	Upsert(ctx context.Context, expr ExpressionEntry) error
	SearchBySituation(ctx context.Context, botID string, situationEmbed []float64, topK int) ([]ExpressionEntry, error)
	ListUnchecked(ctx context.Context, botID string, limit int) ([]ExpressionEntry, error)
	MarkChecked(ctx context.Context, id string, rejected bool) error
}

// JargonRepository defines persistence operations for jargon entries.
type JargonRepository interface {
	Upsert(ctx context.Context, j JargonEntry) error
	Query(ctx context.Context, botID string, words []string) ([]JargonEntry, error)
	List(ctx context.Context, botID string, limit int) ([]JargonEntry, error)
}
