package expression

import "context"

// Embedder converts text into a vector ([]float64) for similarity search.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float64, error)
}
