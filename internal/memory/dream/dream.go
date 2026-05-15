// Package dream provides nightly memory maintenance tasks:
// merging similar memories and cleaning up harmful/outdated entries.
// It is triggered by the existing schedule service, not a new cron.
package dream

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// MemoryRuntime is the subset of the memory service needed for dream tasks.
type MemoryRuntime interface {
	Search(ctx context.Context, req SearchRequest) (SearchResponse, error)
	GetAll(ctx context.Context, req GetAllRequest) (GetAllResponse, error)
	Delete(ctx context.Context, memoryID string) (DeleteResponse, error)
	Update(ctx context.Context, memoryID, newText string) error
}

// SearchRequest is a simplified search input.
type SearchRequest struct {
	Query   string
	BotID   string
	Limit   int
	Filters map[string]any
}

// SearchResponse wraps search results.
type SearchResponse struct {
	Results []MemoryItem `json:"results"`
}

// GetAllRequest fetches all memories for a scope.
type GetAllRequest struct {
	BotID   string
	Limit   int
	Filters map[string]any

	// Since filters to memories created or updated after this time.
	// Zero value means no filter (full scan). Set to time.Now().Add(-24h)
	// for incremental daily scans.
	Since time.Time
}

// GetAllResponse wraps all-fetch results.
type GetAllResponse struct {
	Results []MemoryItem `json:"results"`
}

// DeleteResponse confirms a deletion.
type DeleteResponse struct {
	OK bool `json:"ok"`
}

// MemoryItem is a single memory from storage.
type MemoryItem struct {
	ID        string         `json:"id"`
	Memory    string         `json:"memory"`
	CreatedAt time.Time      `json:"created_at"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// DreamLLM is the LLM interface needed for merging decisions.
// All calls use the compact model for cost efficiency.
type DreamLLM interface {
	ShouldMerge(ctx context.Context, memory1, memory2 string) (bool, string, error)
	IsHarmful(ctx context.Context, memory string) (bool, error)
	// FindAssociations analyzes a batch of memories and returns pairs of related
	// entries. Input is a slice of memory strings; output indices are 0-based.
	// Called by Task 3 (association strengthening) with the compact model.
	FindAssociations(ctx context.Context, memories []string) ([]MemoryAssociation, error)
	// AggregateScenes analyzes a batch of memories and proposes scene clusters.
	// Each SceneCandidate groups semantically related memories into a coherent scene.
	// Called by Task 4 (scene aggregation) with the compact model.
	AggregateScenes(ctx context.Context, memories []string) ([]SceneCandidate, error)
}

// SceneCandidate is a proposed scene from the LLM aggregation step.
// Note: This is intentionally separate from scene.SceneCandidate to keep the
// dream package decoupled from the scene package. The dream service uses its
// own SceneStore interface and SceneEntry type for the same reason.
type SceneCandidate struct {
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	MemoryIDs []string `json:"memory_ids"` // IDs of memories belonging to this scene
}

// MemoryAssociation is a semantic link between two memories.
type MemoryAssociation struct {
	IndexA int    `json:"a"`     // 0-based index into the input batch
	IndexB int    `json:"b"`     // 0-based index into the input batch
	Label  string `json:"label"` // short relation, e.g. "same_topic", "contradicts", "supports"
}

// SceneStore is the interface for scene persistence used by the dream service.
type SceneStore interface {
	List(ctx context.Context, botID string) ([]SceneEntry, error)
	Create(ctx context.Context, entry SceneEntry) error
	Update(ctx context.Context, entry SceneEntry) error
	Delete(ctx context.Context, sceneID string) error
}

// SceneEntry is the dream service's view of a scene (decoupled from scene package).
type SceneEntry struct {
	ID        string
	BotID     string
	Title     string
	Summary   string
	HeatScore float64
	MemoryIDs []string
}

// Service performs periodic memory maintenance (dream).
type Service struct {
	runtime    MemoryRuntime
	llm        DreamLLM
	sceneStore SceneStore
	logger     *slog.Logger
}

// New creates a new dream service.
func New(runtime MemoryRuntime, llm DreamLLM, logger *slog.Logger) *Service {
	return &Service{
		runtime: runtime,
		llm:     llm,
		logger:  logger.With(slog.String("component", "dream")),
	}
}

// SetSceneStore injects the scene store for scene aggregation tasks.
func (s *Service) SetSceneStore(store SceneStore) {
	s.sceneStore = store
}

// MemoryMergeConfig controls how merge decisions are made.
type MemoryMergeConfig struct {
	SimilarityThreshold float64 // 0.0-1.0, default 0.9
	MaxPairsToCheck     int
	QueryPrefix         string
}

var defaultMergeConfig = MemoryMergeConfig{
	SimilarityThreshold: 0.9,
	MaxPairsToCheck:     20,
	QueryPrefix:         "memory maintenance",
}

// MergeResult holds the outcome of a dream maintenance cycle.
type MergeResult struct {
	Scanned       int `json:"scanned"`
	Merged        int `json:"merged"`
	Deleted       int `json:"deleted"`
	HarmCount     int `json:"harm_count"`
	Associations  int `json:"associations"`   // Task 3: number of cross-references written
	ScenesCreated int `json:"scenes_created"` // Task 4: new scenes created
	ScenesUpdated int `json:"scenes_updated"` // Task 4: existing scenes updated
}

// RunOptions controls incremental scan behavior.
type RunOptions struct {
	// Since limits processing to memories created or updated after this time.
	// Zero means full scan. Pass time.Now().Add(-24 * time.Hour) for daily.
	Since time.Time
}

// Run executes the full dream cycle for a bot.
// If opts.Since is set, only memories created/updated after that time
// are processed (incremental mode). Otherwise, all memories are processed.
func (s *Service) Run(ctx context.Context, botID string, opts RunOptions) MergeResult {
	result := MergeResult{}

	filters := map[string]any{
		"bot_id": botID,
	}

	// Task 1: Merge similar memories
	mergeRes := s.mergeSimilar(ctx, botID, filters, opts.Since, defaultMergeConfig)
	result.Scanned = mergeRes.Scanned
	result.Merged = mergeRes.Merged

	// Task 2: Mark harmful/outdated memories
	harmRes := s.cleanHarmful(ctx, botID, filters, opts.Since)
	result.Deleted += harmRes.Deleted
	result.HarmCount += harmRes.HarmCount

	// Task 3: Strengthen cross-memory associations (compact model)
	assocRes := s.strengthenAssociations(ctx, botID, filters, opts.Since)
	result.Associations = assocRes.Written

	// Task 4: Scene aggregation (cluster memories into coherent scenes)
	sceneRes := s.aggregateScenes(ctx, botID, filters, opts.Since)
	result.ScenesCreated = sceneRes.Created
	result.ScenesUpdated = sceneRes.Updated

	s.logger.Info("dream cycle complete",
		slog.String("bot_id", botID),
		slog.Int("scanned", result.Scanned),
		slog.Int("merged", result.Merged),
		slog.Int("deleted", result.Deleted),
		slog.Int("associations", result.Associations),
		slog.Int("scenes_created", result.ScenesCreated),
		slog.Int("scenes_updated", result.ScenesUpdated),
	)

	return result
}

type mergeTaskResult struct {
	Scanned int
	Merged  int
}

// mergeSimilar finds and merges near-duplicate memories.
func (s *Service) mergeSimilar(ctx context.Context, botID string, filters map[string]any, since time.Time, cfg MemoryMergeConfig) mergeTaskResult {
	res := mergeTaskResult{}

	// Fetch all memories for this bot
	allResp, err := s.runtime.GetAll(ctx, GetAllRequest{
		BotID:   botID,
		Limit:   200,
		Filters: filters,
		Since:   since,
	})
	if err != nil {
		s.logger.Warn("dream: getAll failed", slog.String("bot_id", botID), slog.Any("error", err))
		return res
	}

	memories := allResp.Results
	if len(memories) < 2 {
		return res
	}
	res.Scanned = len(memories)

	pairsChecked := 0
	for i := 0; i < len(memories)-1 && pairsChecked < cfg.MaxPairsToCheck; i++ {
		for j := i + 1; j < len(memories) && pairsChecked < cfg.MaxPairsToCheck; j++ {
			m1 := strings.TrimSpace(memories[i].Memory)
			m2 := strings.TrimSpace(memories[j].Memory)
			if m1 == "" || m2 == "" {
				continue
			}

			// Quick pre-filter: if content significantly differs in length, skip
			l1, l2 := len(m1), len(m2)
			if float64(min(l1, l2))/float64(max(l1, l2)) < 0.5 {
				continue
			}

			pairsChecked++

			if s.llm == nil {
				continue
			}
			shouldMerge, mergedText, err := s.llm.ShouldMerge(ctx, m1, m2)
			if err != nil {
				continue
			}
			if !shouldMerge {
				continue
			}

			// Keep the first, delete the second, then update the first
			// with the merged text from the LLM.
			if _, err := s.runtime.Delete(ctx, memories[j].ID); err != nil {
				s.logger.Warn("dream: delete for merge failed",
					slog.String("id", memories[j].ID),
					slog.Any("error", err),
				)
				continue
			}
			if mergedText != "" {
				if err := s.runtime.Update(ctx, memories[i].ID, mergedText); err != nil {
					s.logger.Warn("dream: update merged text failed",
						slog.String("id", memories[i].ID),
						slog.Any("error", err),
					)
				}
			}
			res.Merged++
		}
	}

	return res
}

type harmTaskResult struct {
	Deleted   int
	HarmCount int
}

// cleanHarmful identifies and removes harmful/outdated memories.
func (s *Service) cleanHarmful(ctx context.Context, botID string, filters map[string]any, since time.Time) harmTaskResult {
	res := harmTaskResult{}

	allResp, err := s.runtime.GetAll(ctx, GetAllRequest{
		BotID:   botID,
		Limit:   100,
		Filters: filters,
		Since:   since,
	})
	if err != nil {
		s.logger.Warn("dream: getAll for cleanup failed", slog.String("bot_id", botID), slog.Any("error", err))
		return res
	}

	harmKeywords := []string{
		"password", "token", "secret", "api_key", "credit card",
		"social security", "ssn", "private key",
	}

	for _, item := range allResp.Results {
		mem := strings.ToLower(item.Memory)
		harmful := false

		for _, kw := range harmKeywords {
			if strings.Contains(mem, kw) {
				harmful = true
				break
			}
		}

		if !harmful && s.llm != nil {
			isHarm, err := s.llm.IsHarmful(ctx, item.Memory)
			if err == nil && isHarm {
				harmful = true
			}
		}

		if harmful {
			res.HarmCount++
			if _, err := s.runtime.Delete(ctx, item.ID); err != nil {
				s.logger.Warn("dream: delete harmful memory failed",
					slog.String("id", item.ID),
					slog.Any("error", err),
				)
			} else {
				res.Deleted++
			}
		}
	}

	return res
}

// --- Task 3: Association Strengthening ---

// assocTaskResult holds the outcome of association strengthening.
type assocTaskResult struct {
	Written int // number of cross-reference tags written back to memories
}

// associationBatchSize is the max number of memories sent in one LLM call.
// Uses compact model, kept small for fast inference and minimal cost.
const associationBatchSize = 15

// strengthenAssociations sends all memories in batches to a compact LLM,
// collects association pairs, and writes cross-reference tags back into
// each memory's text so that semantic search naturally picks up the links.
func (s *Service) strengthenAssociations(ctx context.Context, botID string, filters map[string]any, since time.Time) assocTaskResult {
	res := assocTaskResult{}

	allResp, err := s.runtime.GetAll(ctx, GetAllRequest{
		BotID:   botID,
		Limit:   200,
		Filters: filters,
		Since:   since,
	})
	if err != nil {
		s.logger.Warn("dream: getAll for associations failed", slog.String("bot_id", botID), slog.Any("error", err))
		return res
	}

	items := allResp.Results
	if len(items) < 2 || s.llm == nil {
		return res
	}

	// Collect all associations across batches.
	// Map: memory index → set of (related_index, label) pairs.
	type link struct {
		to    int
		label string
	}
	assocMap := make(map[int][]link)

	// Process in batches to keep the compact-model prompt small.
	for offset := 0; offset < len(items); offset += associationBatchSize {
		end := offset + associationBatchSize
		if end > len(items) {
			end = len(items)
		}
		batch := items[offset:end]

		texts := make([]string, len(batch))
		for i, m := range batch {
			texts[i] = strings.TrimSpace(m.Memory)
		}

		assocs, err := s.llm.FindAssociations(ctx, texts)
		if err != nil {
			s.logger.Warn("dream: FindAssociations LLM call failed",
				slog.Int("batch_offset", offset),
				slog.Any("error", err),
			)
			continue
		}

		for _, a := range assocs {
			if a.IndexA < 0 || a.IndexA >= len(batch) || a.IndexB < 0 || a.IndexB >= len(batch) {
				continue
			}
			if a.IndexA == a.IndexB {
				continue
			}
			globalA := offset + a.IndexA
			globalB := offset + a.IndexB
			label := strings.TrimSpace(a.Label)
			if label == "" {
				label = "related"
			}
			assocMap[globalA] = append(assocMap[globalA], link{to: globalB, label: label})
		}
	}

	// Write cross-reference tags back into each memory's text.
	// Format: appended line "[↗ {label}: {first 40 chars of related memory}]"
	for memIdx, links := range assocMap {
		if memIdx >= len(items) {
			continue
		}
		item := items[memIdx]

		existing := strings.TrimSpace(item.Memory)
		// Skip if already has association tags (idempotent).
		// Check trailing lines rather than a simple substring match to avoid
		// false positives when the memory content itself contains "[↗".
		if hasAssociationTags(existing) {
			continue
		}

		var sb strings.Builder
		sb.WriteString(existing)
		sb.WriteString("\n\n")

		seen := make(map[int]bool)
		added := 0
		for _, l := range links {
			if l.to >= len(items) || seen[l.to] {
				continue
			}
			seen[l.to] = true

			preview := items[l.to].Memory
			if len(preview) > 40 {
				preview = preview[:40] + "…"
			}
			sb.WriteString("[↗ ")
			sb.WriteString(l.label)
			sb.WriteString(": ")
			sb.WriteString(preview)
			sb.WriteString("]\n")
			added++
		}

		if added == 0 {
			continue
		}

		newText := sb.String()
		if err := s.runtime.Update(ctx, item.ID, newText); err != nil {
			s.logger.Warn("dream: update association tags failed",
				slog.String("id", item.ID),
				slog.Any("error", err),
			)
			continue
		}
		res.Written += added
	}

	if res.Written > 0 {
		s.logger.Info("dream: association strengthening complete",
			slog.String("bot_id", botID),
			slog.Int("total_memories", len(items)),
			slog.Int("cross_references", res.Written),
		)
	}

	return res
}

// hasAssociationTags checks whether a memory already has cross-reference tags
// appended at the end. Instead of a naive substring search (which would false-
// positive on memory content that happens to contain "[↗"), it inspects the
// trailing lines of the text for the expected tag prefix pattern.
func hasAssociationTags(text string) bool {
	lines := strings.Split(text, "\n")
	// Walk backwards from the end, skipping empty lines.
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		// A valid association tag line starts with "[↗ " and ends with "]".
		if strings.HasPrefix(line, "[↗ ") && strings.HasSuffix(line, "]") {
			return true
		}
		// Once we hit a non-empty, non-tag line, stop — tags are always at the end.
		return false
	}
	return false
}
