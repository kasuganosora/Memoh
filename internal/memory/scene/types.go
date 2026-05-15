// Package scene provides scene aggregation for memory context.
// Scenes are clusters of semantically related memories that represent
// a coherent topic, event, or interaction pattern.
package scene

import "time"

// MaxScenesPerBot is the maximum number of scenes allowed per bot.
// When exceeded, the lowest-heat scenes are merged.
const MaxScenesPerBot = 20

// Scene represents a cluster of semantically related memories.
type Scene struct {
	// ID is the unique identifier for this scene.
	ID string `json:"id"`

	// BotID is the bot this scene belongs to.
	BotID string `json:"bot_id"`

	// Title is a short descriptive title for the scene.
	Title string `json:"title"`

	// Summary is a concise description of what this scene covers.
	Summary string `json:"summary"`

	// HeatScore tracks how frequently this scene is accessed or updated.
	// Higher scores indicate more active/relevant scenes.
	HeatScore float64 `json:"heat_score"`

	// TimeRange records the earliest and latest timestamps of memories in this scene.
	TimeRange TimeRange `json:"time_range"`

	// MemoryIDs lists the IDs of memories that belong to this scene.
	MemoryIDs []string `json:"memory_ids"`

	// CreatedAt is when this scene was first created.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when this scene was last modified.
	UpdatedAt time.Time `json:"updated_at"`
}

// TimeRange represents a time window for a scene.
type TimeRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// SceneCandidate is a proposed scene from the LLM aggregation step.
type SceneCandidate struct {
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	MemoryIDs []string `json:"memory_ids"`
}

// NavigationEntry is a lightweight representation of a scene for the
// navigation index injected into the system prompt.
type NavigationEntry struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
}
