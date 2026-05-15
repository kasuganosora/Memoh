package scene

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Store defines the interface for scene persistence operations.
type Store interface {
	// List returns all scenes for a bot, ordered by heat score descending.
	List(ctx context.Context, botID string) ([]Scene, error)

	// Get returns a single scene by ID.
	Get(ctx context.Context, sceneID string) (*Scene, error)

	// Create persists a new scene and returns it with a generated ID.
	Create(ctx context.Context, scene Scene) (*Scene, error)

	// Update replaces an existing scene.
	Update(ctx context.Context, scene Scene) error

	// Delete removes a scene by ID.
	Delete(ctx context.Context, sceneID string) error

	// MergeLowHeat merges the two lowest-heat scenes into one when the
	// bot exceeds MaxScenesPerBot. Returns the merged scene.
	MergeLowHeat(ctx context.Context, botID string) (*Scene, error)
}

// VectorStore implements Store using Qdrant as the backend.
// Scenes are stored as points with metadata in a dedicated payload namespace.
type VectorStore struct {
	backend SceneBackend
	logger  *slog.Logger
}

// SceneBackend abstracts the vector database operations needed by the scene store.
type SceneBackend interface {
	UpsertScene(ctx context.Context, id string, payload map[string]any, embedding []float32) error
	GetScene(ctx context.Context, id string) (map[string]any, error)
	ListScenes(ctx context.Context, botID string) ([]map[string]any, error)
	DeleteScene(ctx context.Context, id string) error
}

// NewVectorStore creates a new VectorStore with the given backend.
func NewVectorStore(backend SceneBackend, logger *slog.Logger) *VectorStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &VectorStore{
		backend: backend,
		logger:  logger.With(slog.String("component", "scene_store")),
	}
}

func (s *VectorStore) List(ctx context.Context, botID string) ([]Scene, error) {
	payloads, err := s.backend.ListScenes(ctx, botID)
	if err != nil {
		return nil, fmt.Errorf("scene list: %w", err)
	}

	scenes := make([]Scene, 0, len(payloads))
	for _, p := range payloads {
		scene, err := payloadToScene(p)
		if err != nil {
			s.logger.Warn("scene list: failed to parse payload", slog.Any("error", err))
			continue
		}
		scenes = append(scenes, *scene)
	}

	// Sort by heat score descending.
	sort.Slice(scenes, func(i, j int) bool {
		return scenes[i].HeatScore > scenes[j].HeatScore
	})

	return scenes, nil
}

func (s *VectorStore) Get(ctx context.Context, sceneID string) (*Scene, error) {
	payload, err := s.backend.GetScene(ctx, sceneID)
	if err != nil {
		return nil, fmt.Errorf("scene get: %w", err)
	}
	if payload == nil {
		return nil, fmt.Errorf("scene not found: %s", sceneID)
	}
	return payloadToScene(payload)
}

func (s *VectorStore) Create(ctx context.Context, scene Scene) (*Scene, error) {
	if scene.ID == "" {
		scene.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	scene.CreatedAt = now
	scene.UpdatedAt = now

	payload := sceneToPayload(&scene)
	// Use a zero embedding for now; scenes are retrieved by metadata filter, not similarity.
	embedding := make([]float32, 0)

	if err := s.backend.UpsertScene(ctx, scene.ID, payload, embedding); err != nil {
		return nil, fmt.Errorf("scene create: %w", err)
	}
	return &scene, nil
}

func (s *VectorStore) Update(ctx context.Context, scene Scene) error {
	scene.UpdatedAt = time.Now().UTC()
	payload := sceneToPayload(&scene)
	embedding := make([]float32, 0)

	if err := s.backend.UpsertScene(ctx, scene.ID, payload, embedding); err != nil {
		return fmt.Errorf("scene update: %w", err)
	}
	return nil
}

func (s *VectorStore) Delete(ctx context.Context, sceneID string) error {
	if err := s.backend.DeleteScene(ctx, sceneID); err != nil {
		return fmt.Errorf("scene delete: %w", err)
	}
	return nil
}

func (s *VectorStore) MergeLowHeat(ctx context.Context, botID string) (*Scene, error) {
	scenes, err := s.List(ctx, botID)
	if err != nil {
		return nil, err
	}
	if len(scenes) <= MaxScenesPerBot {
		return nil, nil // no merge needed
	}

	// Find the two lowest-heat scenes (they are at the end since sorted desc).
	n := len(scenes)
	low1 := scenes[n-1]
	low2 := scenes[n-2]

	// Merge: combine memory IDs, pick the broader time range, average heat.
	merged := Scene{
		BotID:     botID,
		Title:     low1.Title + " + " + low2.Title,
		Summary:   low1.Summary + "; " + low2.Summary,
		HeatScore: (low1.HeatScore + low2.HeatScore) / 2,
		TimeRange: mergeTimeRanges(low1.TimeRange, low2.TimeRange),
		MemoryIDs: mergeStringSlices(low1.MemoryIDs, low2.MemoryIDs),
	}

	// Truncate merged title/summary if too long.
	if len(merged.Title) > 100 {
		merged.Title = merged.Title[:97] + "..."
	}
	if len(merged.Summary) > 500 {
		merged.Summary = merged.Summary[:497] + "..."
	}

	// Delete the two old scenes.
	if err := s.Delete(ctx, low1.ID); err != nil {
		return nil, fmt.Errorf("merge: delete scene %s: %w", low1.ID, err)
	}
	if err := s.Delete(ctx, low2.ID); err != nil {
		return nil, fmt.Errorf("merge: delete scene %s: %w", low2.ID, err)
	}

	// Create the merged scene.
	created, err := s.Create(ctx, merged)
	if err != nil {
		return nil, fmt.Errorf("merge: create merged scene: %w", err)
	}

	s.logger.Info("scenes merged",
		slog.String("bot_id", botID),
		slog.String("merged_id", created.ID),
		slog.String("source_1", low1.ID),
		slog.String("source_2", low2.ID),
	)

	return created, nil
}

// --- Helpers ---

func sceneToPayload(s *Scene) map[string]any {
	memoryIDsJSON, _ := json.Marshal(s.MemoryIDs)
	return map[string]any{
		"type":             "scene",
		"id":               s.ID,
		"bot_id":           s.BotID,
		"title":            s.Title,
		"summary":          s.Summary,
		"heat_score":       s.HeatScore,
		"time_range_start": s.TimeRange.Start.Format(time.RFC3339),
		"time_range_end":   s.TimeRange.End.Format(time.RFC3339),
		"memory_ids":       string(memoryIDsJSON),
		"created_at":       s.CreatedAt.Format(time.RFC3339),
		"updated_at":       s.UpdatedAt.Format(time.RFC3339),
	}
}

func payloadToScene(p map[string]any) (*Scene, error) {
	s := &Scene{}
	s.ID = stringFromPayload(p, "id")
	s.BotID = stringFromPayload(p, "bot_id")
	s.Title = stringFromPayload(p, "title")
	s.Summary = stringFromPayload(p, "summary")

	if v, ok := p["heat_score"].(float64); ok {
		s.HeatScore = v
	}

	if ts := stringFromPayload(p, "time_range_start"); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			s.TimeRange.Start = t
		}
	}
	if ts := stringFromPayload(p, "time_range_end"); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			s.TimeRange.End = t
		}
	}

	if raw := stringFromPayload(p, "memory_ids"); raw != "" {
		var ids []string
		if err := json.Unmarshal([]byte(raw), &ids); err == nil {
			s.MemoryIDs = ids
		}
	}

	if ts := stringFromPayload(p, "created_at"); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			s.CreatedAt = t
		}
	}
	if ts := stringFromPayload(p, "updated_at"); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			s.UpdatedAt = t
		}
	}

	return s, nil
}

func stringFromPayload(p map[string]any, key string) string {
	v, _ := p[key].(string)
	return strings.TrimSpace(v)
}

func mergeTimeRanges(a, b TimeRange) TimeRange {
	result := TimeRange{Start: a.Start, End: a.End}
	if !b.Start.IsZero() && (result.Start.IsZero() || b.Start.Before(result.Start)) {
		result.Start = b.Start
	}
	if !b.End.IsZero() && b.End.After(result.End) {
		result.End = b.End
	}
	return result
}

func mergeStringSlices(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	result := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			result = append(result, s)
		}
	}
	for _, s := range b {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			result = append(result, s)
		}
	}
	return result
}
