package builtin

import (
	"context"
	"encoding/json"
	"log/slog"

	adapters "github.com/memohai/memoh/internal/memory/adapters"
)

// PipelineStateStore abstracts the database operations for pipeline state persistence.
// This avoids a direct dependency on the sqlc package.
type PipelineStateStore interface {
	SavePipelineState(ctx context.Context, botID string, bufferJSON []byte, threshold, warmupIndex, retryCount int) error
	LoadPipelineState(ctx context.Context, botID string) (bufferJSON []byte, threshold, warmupIndex, retryCount int, err error)
	DeletePipelineState(ctx context.Context, botID string) error
}

// Save persists the current pipeline state to the database for crash recovery.
// It serializes the buffer as JSON and stores threshold/retry metadata.
func (p *FormationPipeline) Save(ctx context.Context, store PipelineStateStore, botID string) error {
	p.mu.Lock()
	bufferCopy := make([]adapters.AfterChatRequest, len(p.buffer))
	copy(bufferCopy, p.buffer)
	threshold := p.threshold
	warmupIndex := p.warmupIndex
	retryCount := p.retryCount
	p.mu.Unlock()

	if len(bufferCopy) == 0 {
		// No buffer to persist — delete any existing state.
		if err := store.DeletePipelineState(ctx, botID); err != nil {
			p.logger.Warn("pipeline: failed to delete empty state",
				slog.String("bot_id", botID),
				slog.Any("error", err),
			)
		}
		return nil
	}

	bufferJSON, err := json.Marshal(bufferCopy)
	if err != nil {
		p.logger.Error("pipeline: failed to marshal buffer for persistence",
			slog.String("bot_id", botID),
			slog.Any("error", err),
		)
		return err
	}

	if err := store.SavePipelineState(ctx, botID, bufferJSON, threshold, warmupIndex, retryCount); err != nil {
		p.logger.Error("pipeline: failed to save state",
			slog.String("bot_id", botID),
			slog.Any("error", err),
		)
		return err
	}

	p.logger.Debug("pipeline: state saved",
		slog.String("bot_id", botID),
		slog.Int("buffer_size", len(bufferCopy)),
		slog.Int("threshold", threshold),
	)
	return nil
}

// Restore loads pipeline state from the database and restores the buffer,
// threshold, and retry count. Should be called during service startup.
func (p *FormationPipeline) Restore(ctx context.Context, store PipelineStateStore, botID string) error {
	bufferJSON, threshold, warmupIndex, retryCount, err := store.LoadPipelineState(ctx, botID)
	if err != nil {
		// Not found is not an error — just means no persisted state.
		p.logger.Debug("pipeline: no persisted state found",
			slog.String("bot_id", botID),
			slog.Any("error", err),
		)
		return nil
	}

	if len(bufferJSON) == 0 {
		return nil
	}

	var buffer []adapters.AfterChatRequest
	if err := json.Unmarshal(bufferJSON, &buffer); err != nil {
		p.logger.Error("pipeline: failed to unmarshal persisted buffer",
			slog.String("bot_id", botID),
			slog.Any("error", err),
		)
		return err
	}

	p.mu.Lock()
	p.buffer = buffer
	if threshold > 0 {
		p.threshold = threshold
	}
	// Only restore warmupIndex if it was explicitly persisted (non-default).
	// warmupIndex of 0 is valid (first warm-up stage), so always restore it
	// when we have a valid persisted state.
	p.warmupIndex = warmupIndex
	p.retryCount = retryCount
	p.mu.Unlock()

	p.logger.Info("pipeline: state restored from database",
		slog.String("bot_id", botID),
		slog.Int("buffer_size", len(buffer)),
		slog.Int("threshold", threshold),
		slog.Int("warmup_index", warmupIndex),
		slog.Int("retry_count", retryCount),
	)

	// Delete persisted state after successful restore to avoid re-processing on next restart.
	if err := store.DeletePipelineState(ctx, botID); err != nil {
		p.logger.Warn("pipeline: failed to delete state after restore",
			slog.String("bot_id", botID),
			slog.Any("error", err),
		)
	}

	return nil
}
