package builtin

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adapters "github.com/memohai/memoh/internal/memory/adapters"
)

func TestFormationPipeline_WarmupThresholds(t *testing.T) {
	var callCount atomic.Int32
	fn := func(_ context.Context, _ adapters.AfterChatRequest) formationResult {
		callCount.Add(1)
		return formationResult{ExtractedFacts: 1, Added: 1}
	}

	p := NewFormationPipeline(nil, fn)
	defer p.Stop()

	// Warm-up threshold starts at 1: first message should trigger immediately.
	p.Enqueue(adapters.AfterChatRequest{BotID: "bot1", Messages: []adapters.Message{{Role: "user", Content: "hello"}}})
	time.Sleep(50 * time.Millisecond)
	if got := callCount.Load(); got != 1 {
		t.Fatalf("expected 1 formation call after first message, got %d", got)
	}

	// After first trigger, threshold advances to 2.
	if got := p.Threshold(); got != 2 {
		t.Fatalf("expected threshold=2 after first trigger, got %d", got)
	}

	// Enqueue 1 message — should NOT trigger (need 2).
	p.Enqueue(adapters.AfterChatRequest{BotID: "bot1", Messages: []adapters.Message{{Role: "user", Content: "msg2"}}})
	time.Sleep(50 * time.Millisecond)
	if got := callCount.Load(); got != 1 {
		t.Fatalf("expected still 1 formation call, got %d", got)
	}

	// Enqueue second message — should trigger (buffer=2, threshold=2).
	p.Enqueue(adapters.AfterChatRequest{BotID: "bot1", Messages: []adapters.Message{{Role: "user", Content: "msg3"}}})
	time.Sleep(50 * time.Millisecond)
	if got := callCount.Load(); got != 2 {
		t.Fatalf("expected 2 formation calls, got %d", got)
	}

	// After second trigger, threshold advances to 4.
	if got := p.Threshold(); got != 4 {
		t.Fatalf("expected threshold=4 after second trigger, got %d", got)
	}
}

func TestFormationPipeline_MatureThreshold(t *testing.T) {
	var callCount atomic.Int32
	fn := func(_ context.Context, _ adapters.AfterChatRequest) formationResult {
		callCount.Add(1)
		return formationResult{ExtractedFacts: 1, Added: 1}
	}

	p := NewFormationPipeline(nil, fn)
	defer p.Stop()

	// Exhaust warm-up thresholds: 1, 2, 4
	// Trigger 1 (threshold=1)
	p.Enqueue(adapters.AfterChatRequest{BotID: "bot1", Messages: []adapters.Message{{Role: "user", Content: "a"}}})
	time.Sleep(50 * time.Millisecond)

	// Trigger 2 (threshold=2)
	p.Enqueue(adapters.AfterChatRequest{BotID: "bot1", Messages: []adapters.Message{{Role: "user", Content: "b"}}})
	p.Enqueue(adapters.AfterChatRequest{BotID: "bot1", Messages: []adapters.Message{{Role: "user", Content: "c"}}})
	time.Sleep(50 * time.Millisecond)

	// Trigger 3 (threshold=4)
	for i := 0; i < 4; i++ {
		p.Enqueue(adapters.AfterChatRequest{BotID: "bot1", Messages: []adapters.Message{{Role: "user", Content: "d"}}})
	}
	time.Sleep(50 * time.Millisecond)

	// Now threshold should be defaultMatureThreshold (8).
	if got := p.Threshold(); got != defaultMatureThreshold {
		t.Fatalf("expected mature threshold=%d, got %d", defaultMatureThreshold, got)
	}
}

func TestFormationPipeline_Flush(t *testing.T) {
	var callCount atomic.Int32
	var mu sync.Mutex
	var lastMsgCount int

	fn := func(_ context.Context, req adapters.AfterChatRequest) formationResult {
		callCount.Add(1)
		mu.Lock()
		lastMsgCount = len(req.Messages)
		mu.Unlock()
		return formationResult{ExtractedFacts: 1, Added: 1}
	}

	p := NewFormationPipeline(nil, fn)
	defer p.Stop()

	// Exhaust first warm-up trigger.
	p.Enqueue(adapters.AfterChatRequest{BotID: "bot1", Messages: []adapters.Message{{Role: "user", Content: "a"}}})
	time.Sleep(50 * time.Millisecond)

	// Buffer some messages without reaching threshold.
	p.Enqueue(adapters.AfterChatRequest{BotID: "bot1", Messages: []adapters.Message{{Role: "user", Content: "b"}}})

	if p.BufferSize() != 1 {
		t.Fatalf("expected buffer size=1, got %d", p.BufferSize())
	}

	// Flush should process immediately.
	p.Flush()

	if got := callCount.Load(); got != 2 {
		t.Fatalf("expected 2 formation calls after flush, got %d", got)
	}
	mu.Lock()
	if lastMsgCount != 1 {
		t.Fatalf("expected flush batch to have 1 message, got %d", lastMsgCount)
	}
	mu.Unlock()

	if p.BufferSize() != 0 {
		t.Fatalf("expected empty buffer after flush, got %d", p.BufferSize())
	}
}

func TestFormationPipeline_RetryAndDiscard(t *testing.T) {
	var callCount atomic.Int32
	fn := func(_ context.Context, _ adapters.AfterChatRequest) formationResult {
		callCount.Add(1)
		// Return empty result with error to simulate transient LLM failure.
		return formationResult{Err: errors.New("simulated LLM timeout")}
	}

	p := NewFormationPipeline(nil, fn)
	p.retryBackoff = 1 * time.Millisecond // fast retries for testing
	defer p.Stop()

	// First enqueue triggers immediately (threshold=1).
	p.Enqueue(adapters.AfterChatRequest{BotID: "bot1", Messages: []adapters.Message{{Role: "user", Content: "fail"}}})
	// processBatch is called in a goroutine and retries with minimal backoff in tests.
	time.Sleep(500 * time.Millisecond)

	// After 3 failures (maxRetries = 3), the batch should be discarded.
	if got := callCount.Load(); got != 3 {
		t.Fatalf("expected 3 total calls (max retries), got %d", got)
	}
	if p.BufferSize() != 0 {
		t.Fatalf("expected buffer=0 after max retries discard, got %d", p.BufferSize())
	}
}

func TestFormationPipeline_NoRetryOnEmptyResult(t *testing.T) {
	var callCount atomic.Int32
	fn := func(_ context.Context, _ adapters.AfterChatRequest) formationResult {
		callCount.Add(1)
		// Return empty result without error — valid "nothing to extract" outcome.
		return formationResult{}
	}

	p := NewFormationPipeline(nil, fn)
	defer p.Stop()

	p.Enqueue(adapters.AfterChatRequest{BotID: "bot1", Messages: []adapters.Message{{Role: "user", Content: "hello"}}})
	time.Sleep(100 * time.Millisecond)

	// Should only be called once — no retry on valid empty result.
	if got := callCount.Load(); got != 1 {
		t.Fatalf("expected 1 call (no retry on valid empty result), got %d", got)
	}
}

func TestFormationPipeline_MergeBatch(t *testing.T) {
	batch := []adapters.AfterChatRequest{
		{BotID: "bot1", Messages: []adapters.Message{{Role: "user", Content: "msg1"}}, UserID: "u1"},
		{BotID: "bot1", Messages: []adapters.Message{{Role: "assistant", Content: "resp1"}, {Role: "user", Content: "msg2"}}, UserID: "u1"},
	}

	merged := mergeBatch(batch)

	if merged.BotID != "bot1" {
		t.Fatalf("expected BotID=bot1, got %s", merged.BotID)
	}
	if len(merged.Messages) != 3 {
		t.Fatalf("expected 3 merged messages, got %d", len(merged.Messages))
	}
	if merged.Messages[0].Content != "msg1" {
		t.Fatalf("expected first message content=msg1, got %s", merged.Messages[0].Content)
	}
	if merged.Messages[2].Content != "msg2" {
		t.Fatalf("expected last message content=msg2, got %s", merged.Messages[2].Content)
	}
}
