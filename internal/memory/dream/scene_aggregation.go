package dream

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// sceneAggBatchSize is the max number of memories sent in one LLM call for scene aggregation.
const sceneAggBatchSize = 30

// sceneTaskResult holds the outcome of scene aggregation.
type sceneTaskResult struct {
	Created         int
	Updated         int
	MemoriesIndexed int // number of memories assigned to scenes
	LLMCalls        int // number of LLM AggregateScenes calls made
	LLMErrors       int // number of LLM calls that failed
}

// aggregateScenes clusters memories into coherent scenes using the compact LLM.
// It evaluates whether new memories belong to existing scenes or need new ones.
func (s *Service) aggregateScenes(ctx context.Context, botID string, filters map[string]any, since time.Time) sceneTaskResult {
	res := sceneTaskResult{}

	if s.sceneStore == nil || s.llm == nil {
		return res
	}

	// Fetch memories to process.
	allResp, err := s.runtime.GetAll(ctx, GetAllRequest{
		BotID:   botID,
		Limit:   200,
		Filters: filters,
		Since:   since,
	})
	if err != nil {
		s.logger.Warn("dream: getAll for scene aggregation failed",
			slog.String("bot_id", botID),
			slog.Any("error", err),
		)
		return res
	}

	items := allResp.Results
	if len(items) < 3 {
		// Not enough memories to form meaningful scenes.
		return res
	}

	// Get existing scenes for this bot.
	existingScenes, err := s.sceneStore.List(ctx, botID)
	if err != nil {
		s.logger.Warn("dream: list scenes failed",
			slog.String("bot_id", botID),
			slog.Any("error", err),
		)
		return res
	}

	// Build a set of memory IDs already assigned to scenes.
	assignedMemories := make(map[string]string) // memoryID → sceneID
	for _, sc := range existingScenes {
		for _, mid := range sc.MemoryIDs {
			assignedMemories[mid] = sc.ID
		}
	}

	// Filter to unassigned memories only.
	var unassigned []MemoryItem
	for _, item := range items {
		if _, ok := assignedMemories[item.ID]; !ok {
			unassigned = append(unassigned, item)
		}
	}

	if len(unassigned) < 3 {
		// Not enough unassigned memories to warrant scene creation.
		return res
	}

	// Process unassigned memories in batches.
	for offset := 0; offset < len(unassigned); offset += sceneAggBatchSize {
		end := offset + sceneAggBatchSize
		if end > len(unassigned) {
			end = len(unassigned)
		}
		batch := unassigned[offset:end]

		texts := make([]string, len(batch))
		for i, m := range batch {
			texts[i] = strings.TrimSpace(m.Memory)
		}

		res.LLMCalls++
		candidates, err := s.llm.AggregateScenes(ctx, texts)
		if err != nil {
			res.LLMErrors++
			s.logger.Warn("dream: AggregateScenes LLM call failed",
				slog.Int("batch_offset", offset),
				slog.Int("batch_size", len(texts)),
				slog.Any("error", err),
			)
			continue
		}

		s.logger.Debug("dream: AggregateScenes batch result",
			slog.Int("batch_offset", offset),
			slog.Int("batch_size", len(texts)),
			slog.Int("scenes_proposed", len(candidates)),
		)

		for _, candidate := range candidates {
			if candidate.Title == "" || len(candidate.MemoryIDs) == 0 {
				continue
			}

			// Resolve memory IDs from the candidate.
			// The LLM returns indices (as string) into the batch; convert to actual IDs.
			resolvedIDs := resolveMemoryIDs(candidate.MemoryIDs, batch)
			if len(resolvedIDs) < 2 {
				continue
			}

			// Check if this candidate overlaps significantly with an existing scene.
			matchedScene := findOverlappingScene(resolvedIDs, existingScenes)
			if matchedScene != nil {
				// Update existing scene: add new memory IDs.
				updated := false
				for _, mid := range resolvedIDs {
					if !containsString(matchedScene.MemoryIDs, mid) {
						matchedScene.MemoryIDs = append(matchedScene.MemoryIDs, mid)
						updated = true
					}
				}
				if updated {
					if err := s.sceneStore.Update(ctx, *matchedScene); err != nil {
						s.logger.Warn("dream: update scene failed",
							slog.String("scene_id", matchedScene.ID),
							slog.String("scene_title", matchedScene.Title),
							slog.Any("error", err),
						)
					} else {
						res.Updated++
						res.MemoriesIndexed += len(resolvedIDs)
						s.logger.Info("dream: scene updated with new memories",
							slog.String("bot_id", botID),
							slog.String("scene_id", matchedScene.ID),
							slog.String("scene_title", matchedScene.Title),
							slog.Int("added_memories", len(resolvedIDs)),
							slog.Int("total_memories", len(matchedScene.MemoryIDs)),
						)
					}
				}
			} else {
				// Create a new scene.
				newScene := SceneEntry{
					BotID:     botID,
					Title:     candidate.Title,
					Summary:   candidate.Summary,
					HeatScore: 1.0,
					MemoryIDs: resolvedIDs,
				}
				if err := s.sceneStore.Create(ctx, newScene); err != nil {
					s.logger.Warn("dream: create scene failed",
						slog.String("title", candidate.Title),
						slog.Any("error", err),
					)
				} else {
					res.Created++
					res.MemoriesIndexed += len(resolvedIDs)
					s.logger.Info("dream: new scene created",
						slog.String("bot_id", botID),
						slog.String("scene_title", candidate.Title),
						slog.Int("memory_count", len(resolvedIDs)),
					)
					// Add to existing scenes for overlap detection in subsequent batches.
					existingScenes = append(existingScenes, newScene)
				}
			}
		}
	}

	s.logger.Info("dream: scene aggregation complete",
		slog.String("bot_id", botID),
		slog.Int("unassigned_memories", len(unassigned)),
		slog.Int("scenes_created", res.Created),
		slog.Int("scenes_updated", res.Updated),
	)

	return res
}

// resolveMemoryIDs converts LLM-returned identifiers to actual memory IDs.
// The LLM may return 0-based indices as strings (e.g., "0", "1", "2") or
// actual memory IDs. This function handles both cases.
func resolveMemoryIDs(candidates []string, batch []MemoryItem) []string {
	var resolved []string
	seen := make(map[string]bool)

	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}

		// Try as 0-based index first.
		if idx := parseIndex(c); idx >= 0 && idx < len(batch) {
			id := batch[idx].ID
			if !seen[id] {
				seen[id] = true
				resolved = append(resolved, id)
			}
			continue
		}

		// Try as direct memory ID.
		for _, item := range batch {
			if item.ID == c && !seen[c] {
				seen[c] = true
				resolved = append(resolved, c)
				break
			}
		}
	}

	return resolved
}

// parseIndex attempts to parse a string as a non-negative integer index.
func parseIndex(s string) int {
	if len(s) == 0 || len(s) > 4 {
		return -1
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return -1
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// findOverlappingScene returns the first existing scene that shares >= 50%
// of its memories with the given IDs, or nil if no significant overlap.
func findOverlappingScene(memoryIDs []string, scenes []SceneEntry) *SceneEntry {
	idSet := make(map[string]bool, len(memoryIDs))
	for _, id := range memoryIDs {
		idSet[id] = true
	}

	for i := range scenes {
		overlap := 0
		for _, mid := range scenes[i].MemoryIDs {
			if idSet[mid] {
				overlap++
			}
		}
		// If >= 50% of the candidate's memories are already in this scene, it's a match.
		if len(memoryIDs) > 0 && float64(overlap)/float64(len(memoryIDs)) >= 0.5 {
			return &scenes[i]
		}
	}
	return nil
}

// containsString checks if a slice contains a specific string.
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}
