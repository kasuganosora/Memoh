package tools

import (
	"context"
	"log/slog"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/expression"
)

// JargonProvider supplies the query_jargon tool for looking up slang and jargon.
type JargonProvider struct {
	jargonRepo expression.JargonRepository
	logger     *slog.Logger
}

// NewJargonProvider creates a new JargonProvider.
func NewJargonProvider(log *slog.Logger, jargonRepo expression.JargonRepository) *JargonProvider {
	return &JargonProvider{
		jargonRepo: jargonRepo,
		logger:     log.With(slog.String("tool", "jargon")),
	}
}

func (p *JargonProvider) Tools(_ context.Context, session SessionContext) ([]sdk.Tool, error) {
	return []sdk.Tool{{
		Name:        "query_jargon",
		Description: "Look up the meaning of slang, abbreviations, or inside jokes used in the chat. Use this when you encounter unfamiliar terms.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"words": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Words or phrases to look up",
				},
			},
			"required": []string{"words"},
		},
		Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
			return p.execQuery(ctx.Context, session, inputAsMap(input))
		},
	}}, nil
}

func (p *JargonProvider) execQuery(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	words := jargonToStringSlice(args["words"])
	if len(words) == 0 {
		return map[string]any{"results": []any{}}, nil
	}

	entries, err := p.jargonRepo.Query(ctx, session.BotID, words)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}

	results := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		results = append(results, map[string]any{
			"content": e.Content,
			"meaning": e.Meaning,
		})
	}
	return map[string]any{"results": results}, nil
}

func jargonToStringSlice(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		s := make([]string, 0, len(v))
		for _, item := range v {
			if str, ok := item.(string); ok {
				s = append(s, str)
			}
		}
		return s
	default:
		return nil
	}
}
