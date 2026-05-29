package builtin

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	adapters "github.com/memohai/memoh/internal/memory/adapters"
)

const (
	// defaultMatureThreshold is the trigger threshold after warm-up completes.
	defaultMatureThreshold = 8

	// maxRetries is the maximum number of retries for a failed formation batch.
	maxRetries = 3

	// retryBackoffBase is the base duration for exponential backoff between retries.
	// Actual backoff = retryBackoffBase * (1 << attempt), capped at retryBackoffMax.
	retryBackoffBase = 10 * time.Second
	retryBackoffMax  = 60 * time.Second

	// idleFlushDuration is how long the pipeline waits idle before auto-flushing.
	idleFlushDuration = 5 * time.Minute
)

// warmupThresholds defines the escalating trigger thresholds during warm-up.
// Each entry is the number of buffered messages required to trigger formation.
var warmupThresholds = []int{1, 2, 4}

// FormationPipeline buffers AfterChat messages and triggers memory formation
// in batches rather than on every single message. It implements a warm-up
// strategy where early messages trigger formation quickly (to capture initial
// context) and then gradually reduces frequency as the session matures.
type FormationPipeline struct {
	mu sync.Mutex

	// buffer holds pending messages awaiting formation.
	buffer []adapters.AfterChatRequest

	// threshold is the current number of messages needed to trigger formation.
	threshold int

	// warmupIndex tracks progress through warmupThresholds.
	// Once >= len(warmupThresholds), the pipeline uses defaultMatureThreshold.
	warmupIndex int

	// retryCount tracks consecutive formation failures for persist/restore.
	// Retries now happen synchronously within processBatch, so this is always 0.
	// Kept for backward compatibility with saved pipeline states.
	retryCount int

	// idleTimer fires after idleFlushDuration of inactivity to flush remaining buffer.
	idleTimer *time.Timer

	// formationFn is the function called to process a batch of messages.
	formationFn func(ctx context.Context, req adapters.AfterChatRequest) formationResult

	// dlqStore is the optional dead-letter-queue for failed batches.
	// When set, discarded batches are persisted for later retry instead of being lost.
	dlqStore PipelineDLQStore

	// botID is needed for DLQ writes (set via SetDLQ).
	botID string

	// retryBackoff overrides retryBackoffBase for testing. Zero means use default.
	retryBackoff time.Duration

	logger *slog.Logger
}

// NewFormationPipeline creates a new pipeline with the given formation function.
func NewFormationPipeline(logger *slog.Logger, fn func(ctx context.Context, req adapters.AfterChatRequest) formationResult) *FormationPipeline {
	if logger == nil {
		logger = slog.Default()
	}
	p := &FormationPipeline{
		threshold:   warmupThresholds[0],
		warmupIndex: 0,
		formationFn: fn,
		logger:      logger,
	}
	return p
}

// SetDLQ injects the dead-letter-queue store and bot ID. When set, batches that
// fail after max retries are persisted to the DLQ instead of being discarded.
func (p *FormationPipeline) SetDLQ(store PipelineDLQStore, botID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dlqStore = store
	p.botID = botID
}

// Enqueue adds a new AfterChat request to the buffer. If the buffer size
// reaches the current threshold, formation is triggered asynchronously.
func (p *FormationPipeline) Enqueue(req adapters.AfterChatRequest) {
	p.mu.Lock()
	p.buffer = append(p.buffer, req)
	shouldTrigger := len(p.buffer) >= p.threshold
	p.resetIdleTimerLocked()
	p.mu.Unlock()

	if shouldTrigger {
		go p.triggerFormation()
	}
}

// Flush immediately processes all buffered messages synchronously.
// Used before search operations to ensure latest memories are available.
// Unlike triggerFormation, Flush does NOT advance the warm-up threshold
// and does NOT retry on failure (to avoid infinite recursion).
func (p *FormationPipeline) Flush() {
	p.mu.Lock()
	if len(p.buffer) == 0 {
		p.mu.Unlock()
		return
	}
	batch := p.drainBufferLocked()
	p.mu.Unlock()

	p.processBatchNoRetry(batch)
}

// BufferSize returns the current number of buffered messages (for observability).
func (p *FormationPipeline) BufferSize() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.buffer)
}

// Threshold returns the current trigger threshold (for observability).
func (p *FormationPipeline) Threshold() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.threshold
}

// Stop cancels the idle timer. Should be called when the provider is shutting down.
func (p *FormationPipeline) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idleTimer != nil {
		p.idleTimer.Stop()
		p.idleTimer = nil
	}
}

// triggerFormation drains the buffer and processes the batch.
func (p *FormationPipeline) triggerFormation() {
	p.mu.Lock()
	if len(p.buffer) == 0 {
		p.mu.Unlock()
		return
	}
	batch := p.drainBufferLocked()
	p.advanceThresholdLocked()
	p.mu.Unlock()

	p.processBatch(batch)
}

// processBatch merges buffered requests into a single AfterChatRequest and
// calls the formation function. On transient LLM errors, it retries up to
// maxRetries times with exponential backoff. Empty results without errors
// are valid outcomes (nothing to extract) and not retried.
func (p *FormationPipeline) processBatch(batch []adapters.AfterChatRequest) {
	if len(batch) == 0 {
		return
	}

	merged := mergeBatch(batch)

	for attempt := 0; ; attempt++ {
		ctx := context.Background()
		result := p.formationFn(ctx, merged)

		// If the formation produced results (even NOOP/skipped), it succeeded.
		allZero := result.ExtractedFacts == 0 && result.Added == 0 && result.Updated == 0 && result.Deleted == 0 && result.Skipped == 0
		if !allZero || result.Err == nil {
			if allZero {
				p.logger.Debug("formation pipeline: batch produced no results (no new facts to extract)")
			} else {
				p.logger.Debug("formation pipeline: batch processed",
					slog.Int("batch_size", len(batch)),
					slog.Int("extracted", result.ExtractedFacts),
					slog.Int("added", result.Added),
					slog.Int("updated", result.Updated),
				)
			}
			return
		}

		// Transient LLM error — retry with backoff.
		if attempt >= maxRetries-1 {
			p.logger.Error("formation pipeline: batch discarded after max retries",
				slog.Int("max_retries", maxRetries),
				slog.Int("discarded_messages", countMessages(batch)),
				slog.Any("last_error", result.Err),
			)
			// Write to DLQ if available, so the batch can be retried later.
			p.enqueueToDLQ(batch, result.Err)
			return
		}

		p.logger.Warn("formation pipeline: batch produced no results, will retry",
			slog.Int("retry", attempt+1),
			slog.Int("max_retries", maxRetries),
			slog.Int("buffer_size", len(batch)),
			slog.Any("error", result.Err),
		)

		// Exponential backoff: gives LLM provider time to recover from transient overload.
		base := p.retryBackoff
		if base == 0 {
			base = retryBackoffBase
		}
		backoff := base * time.Duration(1<<uint(attempt))
		if backoff > retryBackoffMax {
			backoff = retryBackoffMax
		}
		time.Sleep(backoff)
	}
}

// processBatchNoRetry processes a batch without retry logic.
// Used by Flush() to avoid infinite recursion when formation produces no results.
func (p *FormationPipeline) processBatchNoRetry(batch []adapters.AfterChatRequest) {
	if len(batch) == 0 {
		return
	}

	merged := mergeBatch(batch)
	ctx := context.Background()

	result := p.formationFn(ctx, merged)

	p.logger.Debug("formation pipeline: flush batch processed",
		slog.Int("batch_size", len(batch)),
		slog.Int("extracted", result.ExtractedFacts),
		slog.Int("added", result.Added),
		slog.Int("updated", result.Updated),
	)
}

// drainBufferLocked removes and returns all buffered items. Must be called with mu held.
func (p *FormationPipeline) drainBufferLocked() []adapters.AfterChatRequest {
	batch := p.buffer
	p.buffer = nil
	return batch
}

// advanceThresholdLocked moves to the next warm-up threshold or settles at mature.
// Must be called with mu held.
func (p *FormationPipeline) advanceThresholdLocked() {
	p.warmupIndex++
	if p.warmupIndex < len(warmupThresholds) {
		p.threshold = warmupThresholds[p.warmupIndex]
	} else {
		p.threshold = defaultMatureThreshold
	}
}

// resetIdleTimerLocked resets or starts the idle flush timer. Must be called with mu held.
func (p *FormationPipeline) resetIdleTimerLocked() {
	if p.idleTimer != nil {
		p.idleTimer.Stop()
	}
	p.idleTimer = time.AfterFunc(idleFlushDuration, func() {
		p.mu.Lock()
		if len(p.buffer) == 0 {
			p.mu.Unlock()
			return
		}
		batch := p.drainBufferLocked()
		p.mu.Unlock()

		p.logger.Info("formation pipeline: idle flush triggered",
			slog.Int("batch_size", len(batch)),
		)
		p.processBatch(batch)
	})
}

// mergeBatch combines multiple AfterChatRequest into a single request by
// concatenating all messages. Metadata is taken from the last request.
func mergeBatch(batch []adapters.AfterChatRequest) adapters.AfterChatRequest {
	if len(batch) == 1 {
		return batch[0]
	}

	var allMessages []adapters.Message
	for _, req := range batch {
		allMessages = append(allMessages, req.Messages...)
	}

	// Use metadata from the last request (most recent context).
	last := batch[len(batch)-1]
	return adapters.AfterChatRequest{
		BotID:             last.BotID,
		Messages:          allMessages,
		UserID:            last.UserID,
		ChannelIdentityID: last.ChannelIdentityID,
		DisplayName:       last.DisplayName,
		TimezoneLocation:  last.TimezoneLocation,
		SourcePlatform:    last.SourcePlatform,
		SourceSessionID:   last.SourceSessionID,
	}
}

// countMessages counts total messages across a batch of requests.
func countMessages(batch []adapters.AfterChatRequest) int {
	total := 0
	for _, req := range batch {
		total += len(req.Messages)
	}
	return total
}

// enqueueToDLQ persists a failed batch to the dead-letter-queue for later retry.
func (p *FormationPipeline) enqueueToDLQ(batch []adapters.AfterChatRequest, lastErr error) {
	if p.dlqStore == nil || p.botID == "" {
		return
	}

	merged := mergeBatch(batch)
	batchJSON, err := json.Marshal(merged)
	if err != nil {
		p.logger.Error("formation pipeline: failed to marshal batch for DLQ",
			slog.Any("error", err),
		)
		return
	}

	errMsg := ""
	if lastErr != nil {
		errMsg = lastErr.Error()
	}

	// Schedule retry after 30 minutes (give LLM provider time to recover).
	nextRetry := time.Now().Add(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := p.dlqStore.EnqueueDLQ(ctx, p.botID, batchJSON, errMsg, nextRetry); err != nil {
		p.logger.Error("formation pipeline: failed to enqueue to DLQ",
			slog.Any("error", err),
		)
	} else {
		p.logger.Info("formation pipeline: batch saved to DLQ for later retry",
			slog.Int("messages", countMessages(batch)),
		)
	}
}

// RetryDLQ processes entries from the dead-letter-queue. Called during dream
// cycles or scheduled maintenance. Returns the number of successfully processed entries.
func (p *FormationPipeline) RetryDLQ(ctx context.Context) int {
	if p.dlqStore == nil || p.botID == "" {
		return 0
	}

	const maxDLQBatch = 5
	const maxDLQAttempts = 10

	entries, err := p.dlqStore.DequeueDLQ(ctx, p.botID, maxDLQBatch)
	if err != nil {
		p.logger.Warn("formation pipeline: failed to dequeue DLQ",
			slog.Any("error", err),
		)
		return 0
	}

	if len(entries) == 0 {
		return 0
	}

	p.logger.Info("formation pipeline: retrying DLQ entries",
		slog.Int("count", len(entries)),
	)

	processed := 0
	for _, entry := range entries {
		var merged adapters.AfterChatRequest
		if err := json.Unmarshal(entry.BatchJSON, &merged); err != nil {
			p.logger.Warn("formation pipeline: DLQ entry unmarshal failed, deleting",
				slog.Int64("dlq_id", entry.ID),
				slog.Any("error", err),
			)
			_ = p.dlqStore.DeleteDLQEntry(ctx, entry.ID)
			continue
		}

		result := p.formationFn(ctx, merged)
		allZero := result.ExtractedFacts == 0 && result.Added == 0 && result.Updated == 0 && result.Deleted == 0 && result.Skipped == 0

		if !allZero || result.Err == nil {
			// Success — remove from DLQ.
			_ = p.dlqStore.DeleteDLQEntry(ctx, entry.ID)
			processed++
			p.logger.Info("formation pipeline: DLQ entry processed successfully",
				slog.Int64("dlq_id", entry.ID),
			)
		} else {
			// Still failing — bump attempts or discard if too old.
			newAttempts := entry.Attempts + 1
			if newAttempts >= maxDLQAttempts {
				p.logger.Warn("formation pipeline: DLQ entry exceeded max attempts, discarding",
					slog.Int64("dlq_id", entry.ID),
					slog.Int("attempts", newAttempts),
				)
				_ = p.dlqStore.DeleteDLQEntry(ctx, entry.ID)
			} else {
				// Exponential backoff: 30m * 2^attempts (capped at 24h).
				if newAttempts > 20 {
					newAttempts = 20 // prevent overflow
				}
				backoff := 30 * time.Minute * time.Duration(1<<uint(newAttempts)) //nolint:gosec
				if backoff > 24*time.Hour {
					backoff = 24 * time.Hour
				}
				nextRetry := time.Now().Add(backoff)
				_ = p.dlqStore.UpdateDLQEntry(ctx, entry.ID, newAttempts, nextRetry)
			}
		}
	}

	return processed
}
