// Package profiles provides user profile building and management services.
package profiles

import (
	"context"
	"time"
)

// Profile represents a learned user personality profile.
type Profile struct {
	UserID    string    `json:"user_id"`
	BotID     string    `json:"bot_id"`
	Traits    []Trait   `json:"traits"`
	Facts     []Fact    `json:"facts"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Trait describes a personality characteristic.
type Trait struct {
	Name     string  `json:"name"`     // e.g. "communication_style"
	Value    string  `json:"value"`    // e.g. "casual_direct"
	Evidence string  `json:"evidence"` // supporting evidence
	Strength float64 `json:"strength"` // 0.0-1.0 confidence
}

// Fact is a discrete piece of information about a user.
type Fact struct {
	Category string  `json:"category"` // e.g. "preference", "knowledge", "habit"
	Content  string  `json:"content"`
	Source   string  `json:"source"`   // which message or memory it came from
	Strength float64 `json:"strength"` // confidence
}

// ProfileLLM abstracts LLM calls for profile aggregation.
type ProfileLLM interface {
	GenerateText(ctx context.Context, systemPrompt, userMessage string) (string, error)
}

// ProfileCache is an in-memory cache with TTL for profiles.
type ProfileCache interface {
	Get(botID, userID string) (*Profile, bool)
	Set(botID, userID string, profile *Profile)
}
