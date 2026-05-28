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
	// UpdateMetadata sets metadata fields on a memory without changing its text
	// content or re-embedding it. Used for association tags and other annotations
	// that should not pollute the embedding vector.
	UpdateMetadata(ctx context.Context, memoryID string, metadata map[string]any) error
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
	Create(ctx context.Context, entry SceneEntry) (string, error) // returns generated ID
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
	MaxMergesPerCycle   int     // max merges in one cycle to avoid deleting too much
	MaxCandidatesPerMem int     // how many similar candidates to check per memory
	QueryPrefix         string
}

var defaultMergeConfig = MemoryMergeConfig{
	SimilarityThreshold: 0.9,
	MaxMergesPerCycle:   10, // cap total merges per cycle
	MaxCandidatesPerMem: 3,  // search top-3 similar for each new memory
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
	start := time.Now()
	result := MergeResult{}

	// Enforce a 10-minute timeout so dream never blocks the scheduler.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	s.logger.Info(
		"dream: cycle starting",
		slog.String("bot_id", botID),
		slog.String("since", opts.Since.Format(time.RFC3339)),
	)

	filters := map[string]any{
		"bot_id": botID,
	}

	// Task 1: Merge similar memories
	t1Start := time.Now()
	mergeRes := s.mergeSimilar(ctx, botID, filters, opts.Since, defaultMergeConfig)
	result.Scanned = mergeRes.Scanned
	result.Merged = mergeRes.Merged
	s.logger.Info(
		"dream: task 1/4 merge similar complete",
		slog.String("bot_id", botID),
		slog.Int("scanned", mergeRes.Scanned),
		slog.Int("merged", mergeRes.Merged),
		slog.Duration("duration", time.Since(t1Start)),
	)

	// Task 2: Mark harmful/outdated memories
	t2Start := time.Now()
	harmRes := s.cleanHarmful(ctx, botID, filters, opts.Since)
	result.Deleted += harmRes.Deleted
	result.HarmCount += harmRes.HarmCount
	s.logger.Info(
		"dream: task 2/4 clean harmful complete",
		slog.String("bot_id", botID),
		slog.Int("harm_detected", harmRes.HarmCount),
		slog.Int("deleted", harmRes.Deleted),
		slog.Duration("duration", time.Since(t2Start)),
	)

	// Task 3: Strengthen cross-memory associations (compact model)
	t3Start := time.Now()
	assocRes := s.strengthenAssociations(ctx, botID, filters, opts.Since)
	result.Associations = assocRes.Written
	s.logger.Info(
		"dream: task 3/4 associations complete",
		slog.String("bot_id", botID),
		slog.Int("cross_references", assocRes.Written),
		slog.Int("llm_calls", assocRes.LLMCalls),
		slog.Int("llm_errors", assocRes.LLMErrors),
		slog.Duration("duration", time.Since(t3Start)),
	)

	// Task 4: Scene aggregation (cluster memories into coherent scenes)
	t4Start := time.Now()
	sceneRes := s.aggregateScenes(ctx, botID, filters, opts.Since)
	result.ScenesCreated = sceneRes.Created
	result.ScenesUpdated = sceneRes.Updated
	s.logger.Info(
		"dream: task 4/4 scene aggregation complete",
		slog.String("bot_id", botID),
		slog.Int("scenes_created", sceneRes.Created),
		slog.Int("scenes_updated", sceneRes.Updated),
		slog.Int("memories_indexed", sceneRes.MemoriesIndexed),
		slog.Int("llm_calls", sceneRes.LLMCalls),
		slog.Int("llm_errors", sceneRes.LLMErrors),
		slog.Duration("duration", time.Since(t4Start)),
	)

	s.logger.Info(
		"dream: cycle complete",
		slog.String("bot_id", botID),
		slog.Int("scanned", result.Scanned),
		slog.Int("merged", result.Merged),
		slog.Int("deleted", result.Deleted),
		slog.Int("associations", result.Associations),
		slog.Int("scenes_created", result.ScenesCreated),
		slog.Int("scenes_updated", result.ScenesUpdated),
		slog.Duration("total_duration", time.Since(start)),
	)

	return result
}

type mergeTaskResult struct {
	Scanned int
	Merged  int
}

// mergeSimilar finds and merges near-duplicate memories using semantic search.
// For each recently added memory, it uses the Search API (embedding similarity)
// to find top-K candidates, then asks the LLM to confirm merge decisions.
func (s *Service) mergeSimilar(ctx context.Context, botID string, filters map[string]any, since time.Time, cfg MemoryMergeConfig) mergeTaskResult {
	res := mergeTaskResult{}

	// Fetch recent memories (incremental via Since).
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

	if s.llm == nil {
		return res
	}

	// Track IDs that have been merged away to avoid double-processing.
	mergedIDs := make(map[string]bool)

	for _, mem := range memories {
		if res.Merged >= cfg.MaxMergesPerCycle {
			break
		}
		if mergedIDs[mem.ID] {
			continue
		}
		text := strings.TrimSpace(mem.Memory)
		if text == "" {
			continue
		}

		// Skip profile memories — they are now upserted (single entry per user)
		// so they should never appear as duplicates. Also skip heartbeat noise.
		if strings.HasPrefix(text, "[profile]") || isHeartbeatNoiseForMerge(text) {
			continue
		}

		// Use the memory text itself as a search query to find semantically similar ones.
		searchResp, err := s.runtime.Search(ctx, SearchRequest{
			Query:   text,
			BotID:   botID,
			Limit:   cfg.MaxCandidatesPerMem + 1, // +1 because the memory itself may appear
			Filters: filters,
		})
		if err != nil {
			s.logger.Warn("dream: search for similar failed",
				slog.String("bot_id", botID),
				slog.String("mem_id", mem.ID),
				slog.Any("error", err),
			)
			continue
		}

		for _, candidate := range searchResp.Results {
			if res.Merged >= cfg.MaxMergesPerCycle {
				break
			}
			// Skip self and already-merged memories.
			if candidate.ID == mem.ID || mergedIDs[candidate.ID] {
				continue
			}
			candidateText := strings.TrimSpace(candidate.Memory)
			if candidateText == "" {
				continue
			}

			shouldMerge, mergedText, err := s.llm.ShouldMerge(ctx, text, candidateText)
			if err != nil {
				continue
			}
			if shouldMerge {
				s.logger.Info(
					"dream: merging duplicate memories",
					slog.String("bot_id", botID),
					slog.String("keep_id", mem.ID),
					slog.String("delete_id", candidate.ID),
					slog.String("keep_preview", truncateForLog(text, 60)),
					slog.String("merged_preview", truncateForLog(mergedText, 60)),
				)
				if _, err := s.runtime.Delete(ctx, candidate.ID); err != nil {
					s.logger.Warn(
						"dream: delete for merge failed",
						slog.String("id", candidate.ID),
						slog.Any("error", err),
					)
					continue
				}
				mergedIDs[candidate.ID] = true
				if mergedText != "" {
					if err := s.runtime.Update(ctx, mem.ID, mergedText); err != nil {
						s.logger.Warn(
							"dream: update merged text failed",
							slog.String("id", mem.ID),
							slog.Any("error", err),
						)
					}
					// Update local text for subsequent searches.
					text = mergedText
				}
				res.Merged++
			}
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

	// Only call LLM for the first N memories to avoid unlimited
	// sequential calls with the budget model. Keyword-based
	// detection always runs on all memories.
	const maxLLMChecks = 20
	llmChecks := 0

	for _, item := range allResp.Results {
		mem := strings.ToLower(item.Memory)
		harmful := false

		for _, kw := range harmKeywords {
			if strings.Contains(mem, kw) {
				harmful = true
				break
			}
		}

		if !harmful && s.llm != nil && llmChecks < maxLLMChecks {
			llmChecks++
			isHarm, err := s.llm.IsHarmful(ctx, item.Memory)
			if err == nil && isHarm {
				harmful = true
			}
		}

		if harmful {
			res.HarmCount++
			if _, err := s.runtime.Delete(ctx, item.ID); err != nil {
				s.logger.Warn(
					"dream: delete harmful memory failed",
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
	Written   int // number of cross-reference tags written back to memories
	LLMCalls  int // number of LLM FindAssociations calls made
	LLMErrors int // number of LLM calls that failed
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
		res.LLMCalls++
		if err != nil {
			res.LLMErrors++
			s.logger.Warn(
				"dream: FindAssociations LLM call failed",
				slog.Int("batch_offset", offset),
				slog.Int("batch_size", len(texts)),
				slog.Any("error", err),
			)
			continue
		}

		s.logger.Debug(
			"dream: FindAssociations batch result",
			slog.Int("batch_offset", offset),
			slog.Int("batch_size", len(texts)),
			slog.Int("associations_found", len(assocs)),
		)

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

	// Write cross-reference associations into metadata rather than appending
	// tags to the memory text body. Text-body tags like "[↗ label: preview]"
	// pollute the embedding vector and degrade retrieval quality.
	for memIdx, links := range assocMap {
		if memIdx >= len(items) {
			continue
		}
		item := items[memIdx]

		seen := make(map[int]bool)
		assocEntries := make([]map[string]string, 0, len(links))
		for _, l := range links {
			if l.to >= len(items) || seen[l.to] {
				continue
			}
			seen[l.to] = true

			assocEntries = append(assocEntries, map[string]string{
				"related_id": items[l.to].ID,
				"label":      l.label,
			})
		}

		if len(assocEntries) == 0 {
			continue
		}

		metadata := map[string]any{
			"associations": assocEntries,
		}
		if err := s.runtime.UpdateMetadata(ctx, item.ID, metadata); err != nil {
			s.logger.Warn(
				"dream: update association metadata failed",
				slog.String("id", item.ID),
				slog.Any("error", err),
			)
			continue
		}
		res.Written += len(assocEntries)
	}

	if res.Written > 0 {
		s.logger.Info(
			"dream: association strengthening complete",
			slog.String("bot_id", botID),
			slog.Int("total_memories", len(items)),
			slog.Int("cross_references", res.Written),
		)
	}

	return res
}

// truncateForLog truncates a string to maxLen for use in log messages.
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

// isHeartbeatNoiseForMerge delegates to the stricter isHeartbeatNoise (defined
// in scene_aggregation.go) to avoid false positives on legitimate memories that
// happen to contain the word "heartbeat" (e.g., "discussed Apple Watch heartbeat
// monitor feature").
func isHeartbeatNoiseForMerge(text string) bool {
	return isHeartbeatNoise(text)
}
