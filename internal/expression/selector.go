package expression

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// Selector finds matching expressions for a given conversational context.
// It uses embedding-based vector similarity with keyword fallback.
type Selector struct {
	repo     ExpressionRepository
	embedder Embedder
	logger   *slog.Logger
}

// NewSelector creates a new expression Selector.
// embedder can be nil — in that case only keyword matching is used.
func NewSelector(repo ExpressionRepository, embedder Embedder, logger *slog.Logger) *Selector {
	return &Selector{
		repo:     repo,
		embedder: embedder,
		logger:   logger.With(slog.String("component", "expression.selector")),
	}
}

// Select finds the topK matching expressions for the given conversation context.
// It prefers vector similarity search when an embedder is available,
// falling back to keyword matching.
func (s *Selector) Select(ctx context.Context, botID string, conversationCtx string, topK int) ([]ExpressionEntry, error) {
	if topK <= 0 {
		topK = 3
	}

	// Primary: vector similarity search
	if s.embedder != nil {
		vec, err := s.embedder.Embed(ctx, conversationCtx)
		if err == nil && len(vec) > 0 {
			entries, err := s.repo.SearchBySituation(ctx, botID, vec, topK)
			if err == nil && len(entries) > 0 {
				return entries, nil
			}
			if s.logger != nil {
				s.logger.Warn("vector search failed, falling back to keyword",
					slog.String("bot_id", botID),
					slog.Any("error", err),
				)
			}
		}
	}

	// Fallback: keyword match (empty vector → DB should return top by count)
	return s.repo.SearchBySituation(ctx, botID, nil, topK)
}

// FormatStyleReference formats matching expressions as a prompt snippet
// for injection into the replyer system prompt.
func FormatStyleReference(entries []ExpressionEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n## Style Reference\n\n")
	sb.WriteString("The bot's past expressions in similar situations:\n")
	for _, e := range entries {
		fmt.Fprintf(&sb, "- Situation: \"%s\" → Style: \"%s\"\n", e.Situation, e.Style)
	}
	sb.WriteString("\nMatch your reply to these styles naturally. Don't force them if they don't fit.\n")
	return sb.String()
}
