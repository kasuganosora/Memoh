// Package contextkeys provides shared context keys for passing metadata
// between the agent package tree and its consumers.
package contextkeys

import "context"

// BudgetBotIDKey is a context value key for passing botID to budget model tasks
// (replyer, expression learner, profile extraction).
type BudgetBotIDKey struct{}

// WithBudgetBotID returns a context with the botID set for budget model tasks.
func WithBudgetBotID(ctx context.Context, botID string) context.Context {
	return context.WithValue(ctx, BudgetBotIDKey{}, botID)
}

// BudgetBotID extracts the botID stored via WithBudgetBotID.
// Returns empty string if not set.
func BudgetBotID(ctx context.Context) string {
	if v := ctx.Value(BudgetBotIDKey{}); v != nil {
		return v.(string)
	}
	return ""
}
