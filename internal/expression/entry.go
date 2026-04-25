// Package expression provides the domain model and services for learning
// and selecting bot expressions (situation→style mappings) and jargon terms.
package expression

import "time"

// ExpressionEntry represents a learned expression pattern:
// a situation description paired with the bot's characteristic style response.
type ExpressionEntry struct {
	ID         string
	BotID      string
	SessionID  string
	Situation  string // e.g. "对某件事表示惊叹"
	Style      string // e.g. "我嘞个xxxx"
	Count      int
	Checked    bool
	Rejected   bool
	CreatedAt  time.Time
	LastActive time.Time
}

// JargonEntry represents a learned slang, abbreviation, or inside joke.
type JargonEntry struct {
	ID        string
	BotID     string
	SessionID string
	Content   string // e.g. "yyds"
	Meaning   string // e.g. "永远的神"
	Count     int
	CreatedAt time.Time
}

// Message is a minimal message representation used by the learner.
type Message struct {
	Role    string // "user" or "assistant"
	Content string
}
