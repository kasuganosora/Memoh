package builtin

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	adapters "github.com/memohai/memoh/internal/memory/adapters"
)

const (
	formationTimeout       = 200 * time.Second // overall safety-net timeout (accommodates decide retry)
	extractTimeout         = 60 * time.Second  // independent timeout for Extract LLM call
	decideTimeout          = 90 * time.Second  // independent timeout for Decide LLM call (raised from 60s to reduce timeouts)
	decideMaxAttempts      = 2                 // retry once on transient failure
	decideRetryDelay       = 5 * time.Second   // delay between decide retries
	candidateSearchLimit   = 20
	candidateGetAllLimit   = 50
	maxCandidatesPerDecide = 30

	actionADD    = "ADD"
	actionUPDATE = "UPDATE"
	actionDELETE = "DELETE"
	actionNOOP   = "NOOP"
)

// formationResult holds the outcome of a memory formation cycle.
type formationResult struct {
	ExtractedFacts int
	Added          int
	Updated        int
	Deleted        int
	Skipped        int
	// Err is set when the formation step failed due to an LLM call error
	// (e.g. timeout, network issue). Empty results without errors are valid
	// outcomes (nothing to extract), not failures.
	Err error
}

// runFormation executes the Extract -> candidate retrieval -> Decide -> apply pipeline.
func runFormation(ctx context.Context, logger *slog.Logger, llm adapters.LLM, runtime memoryRuntime, req adapters.AfterChatRequest) formationResult {
	ctx, cancel := context.WithTimeout(ctx, formationTimeout)
	defer cancel()

	botID := strings.TrimSpace(req.BotID)
	result := formationResult{}

	extractCtx, extractCancel := context.WithTimeout(ctx, extractTimeout)
	extracted, err := llm.Extract(extractCtx, adapters.ExtractRequest{
		BotID:            botID,
		Messages:         req.Messages,
		TimezoneLocation: req.TimezoneLocation,
	})
	extractCancel()
	if err != nil {
		logger.Warn("memory formation: extract failed", slog.String("bot_id", botID), slog.Any("error", err))
		result.Err = err
		return result
	}
	facts := filterNonEmpty(extracted.Facts)
	if len(facts) == 0 {
		return result
	}
	result.ExtractedFacts = len(facts)

	candidates := gatherCandidates(ctx, logger, runtime, botID, facts)

	var decided adapters.DecideResponse
	for attempt := range decideMaxAttempts {
		decideCtx, decideCancel := context.WithTimeout(ctx, decideTimeout)
		decided, err = llm.Decide(decideCtx, adapters.DecideRequest{
			BotID:      botID,
			Facts:      facts,
			Candidates: candidates,
		})
		decideCancel()
		if err == nil {
			break
		}
		if !isTransientError(err) || attempt == decideMaxAttempts-1 {
			break
		}
		logger.Info("memory formation: decide retrying",
			slog.String("bot_id", botID),
			slog.Int("attempt", attempt+1),
			slog.Any("error", err),
		)
		if !sleepOrCancel(ctx, decideRetryDelay) {
			err = ctx.Err()
			break
		}
	}
	if err != nil {
		logger.Warn("memory formation: decide failed", slog.String("bot_id", botID), slog.Any("error", err))
		result.Err = err
		return result
	}

	filters := map[string]any{
		"namespace": sharedMemoryNamespace,
		"scopeId":   botID,
		"bot_id":    botID,
	}
	metadata := adapters.BuildProfileMetadata(req.UserID, req.ChannelIdentityID, req.DisplayName)
	if req.SourcePlatform != "" || req.SourceSessionID != "" {
		if metadata == nil {
			metadata = make(map[string]any)
		}
		if req.SourcePlatform != "" {
			metadata["source_platform"] = req.SourcePlatform
		}
		if req.SourceSessionID != "" {
			metadata["source_session_id"] = req.SourceSessionID
		}
	}

	applyActions(ctx, logger, runtime, botID, decided.Actions, filters, metadata, &result)
	return result
}

// gatherCandidates collects existing memories relevant to the extracted facts.
func gatherCandidates(ctx context.Context, logger *slog.Logger, runtime memoryRuntime, botID string, facts []string) []adapters.CandidateMemory {
	seen := make(map[string]struct{})
	candidates := make([]adapters.CandidateMemory, 0, candidateSearchLimit)

	filters := map[string]any{
		"namespace": sharedMemoryNamespace,
		"scopeId":   botID,
		"bot_id":    botID,
	}

	for _, fact := range facts {
		if len(candidates) >= maxCandidatesPerDecide {
			break
		}
		resp, err := runtime.Search(ctx, adapters.SearchRequest{
			Query:   fact,
			BotID:   botID,
			Limit:   candidateSearchLimit / max(len(facts), 1),
			Filters: filters,
			NoStats: true,
		})
		if err != nil {
			logger.Debug("memory formation: search candidates failed", slog.String("bot_id", botID), slog.Any("error", err))
			continue
		}
		for _, item := range resp.Results {
			id := strings.TrimSpace(item.ID)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			candidates = append(candidates, adapters.CandidateMemory{
				ID:        id,
				Memory:    item.Memory,
				CreatedAt: item.CreatedAt,
				Metadata:  item.Metadata,
			})
			if len(candidates) >= maxCandidatesPerDecide {
				break
			}
		}
	}

	if len(candidates) < maxCandidatesPerDecide {
		resp, err := runtime.GetAll(ctx, adapters.GetAllRequest{
			BotID:   botID,
			Limit:   candidateGetAllLimit,
			Filters: filters,
			NoStats: true,
		})
		if err == nil {
			for _, item := range resp.Results {
				id := strings.TrimSpace(item.ID)
				if id == "" {
					continue
				}
				if _, ok := seen[id]; ok {
					continue
				}
				seen[id] = struct{}{}
				candidates = append(candidates, adapters.CandidateMemory{
					ID:        id,
					Memory:    item.Memory,
					CreatedAt: item.CreatedAt,
					Metadata:  item.Metadata,
				})
				if len(candidates) >= maxCandidatesPerDecide {
					break
				}
			}
		}
	}

	return candidates
}

// applyActions executes the decided CRUD actions against the runtime.
func applyActions(ctx context.Context, logger *slog.Logger, runtime memoryRuntime, botID string, actions []adapters.DecisionAction, filters map[string]any, metadata map[string]any, result *formationResult) {
	deleted := make(map[string]struct{})
	updated := make(map[string]struct{})

	for _, action := range actions {
		event := strings.ToUpper(strings.TrimSpace(action.Event))

		// Enrich metadata with importance if provided by the LLM.
		actionMeta := cloneMetadata(metadata)
		if imp := strings.TrimSpace(action.Importance); imp != "" {
			actionMeta["importance"] = imp
		}

		switch event {
		case actionADD:
			text := strings.TrimSpace(action.Text)
			if text == "" {
				logger.Debug("memory formation: ADD skipped (empty text)", slog.String("bot_id", botID))
				result.Skipped++
				continue
			}
			if _, err := runtime.Add(ctx, adapters.AddRequest{
				Message:  text,
				BotID:    botID,
				Metadata: actionMeta,
				Filters:  filters,
			}); err != nil {
				logger.Warn("memory formation: ADD failed", slog.String("bot_id", botID), slog.Any("error", err))
			} else {
				result.Added++
			}

		case actionUPDATE:
			id := strings.TrimSpace(action.ID)
			text := strings.TrimSpace(action.Text)
			if id == "" || text == "" {
				logger.Debug("memory formation: UPDATE skipped (missing id or text)", slog.String("bot_id", botID))
				result.Skipped++
				continue
			}
			if _, ok := updated[id]; ok {
				result.Skipped++
				continue
			}
			if _, err := runtime.Update(ctx, adapters.UpdateRequest{
				MemoryID: id,
				Memory:   text,
			}); err != nil {
				logger.Warn("memory formation: UPDATE failed", slog.String("bot_id", botID), slog.String("memory_id", id), slog.Any("error", err))
			} else {
				updated[id] = struct{}{}
				result.Updated++
			}

		case actionDELETE:
			id := strings.TrimSpace(action.ID)
			if id == "" {
				logger.Debug("memory formation: DELETE skipped (missing id)", slog.String("bot_id", botID))
				result.Skipped++
				continue
			}
			if _, ok := deleted[id]; ok {
				result.Skipped++
				continue
			}
			if _, err := runtime.Delete(ctx, id); err != nil {
				logger.Warn("memory formation: DELETE failed", slog.String("bot_id", botID), slog.String("memory_id", id), slog.Any("error", err))
			} else {
				deleted[id] = struct{}{}
				result.Deleted++
			}

		case actionNOOP, "":
			result.Skipped++

		default:
			logger.Debug("memory formation: unknown action event", slog.String("bot_id", botID), slog.String("event", event))
			result.Skipped++
		}
	}
}

func filterNonEmpty(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// cloneMetadata shallow-copies a metadata map so each action can have
// its own enriched copy (e.g., with importance) without mutating the shared one.
func cloneMetadata(meta map[string]any) map[string]any {
	if meta == nil {
		return make(map[string]any)
	}
	cp := make(map[string]any, len(meta))
	for k, v := range meta {
		cp[k] = v
	}
	return cp
}

// isTransientError returns true for errors that are likely temporary and worth
// retrying (e.g., deadline exceeded, network timeouts, server errors).
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Don't retry on parent-context cancellation (e.g., overall formation timeout hit).
	if errors.Is(err, context.Canceled) {
		return false
	}
	// Match common transient error patterns from LLM providers.
	msg := strings.ToLower(err.Error())
	for _, keyword := range []string{
		"deadline exceeded",
		"timeout",
		"connection reset",
		"unexpected eof",
		"status 429",
		"status 500",
		"status 502",
		"status 503",
		"status 504",
	} {
		if strings.Contains(msg, keyword) {
			return true
		}
	}
	return false
}

// sleepOrCancel waits for the specified duration or returns early if the context
// is cancelled. Returns true if the sleep completed, false if cancelled.
func sleepOrCancel(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
