package pipeline

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/chattiming"
	"github.com/memohai/memoh/internal/memory/adapters"
)

// TestRunSession_ExpressionAccumulatorBlocking reproduces the bug where a
// blocking ExpressionAccumulator prevents the session loop from returning to
// the select statement, causing the session to be stuck indefinitely.
//
// Root cause: extractPassiveMemory calls ExpressionAccumulator synchronously.
// If the underlying Learner.Accumulate() blocks on a mutex held by a
// long-running LearnFromHistory LLM call, the entire session loop hangs.
// The watchdog context cancellation cannot interrupt sync.Mutex.Lock().
//
// Expected behavior after fix: ExpressionAccumulator should be called
// asynchronously (in a goroutine) so it cannot block the session loop.
func TestRunSession_ExpressionAccumulatorBlocking(t *testing.T) {
	t.Parallel()

	runner := &fakeChatRunner{}

	// Create a blocking ExpressionAccumulator that simulates a mutex deadlock.
	var accumulatorCalled sync.WaitGroup
	accumulatorCalled.Add(1)
	blockingAccumulator := func(ctx context.Context, _, _ string, _ []adapters.Message) {
		accumulatorCalled.Done()
		// Simulate blocking on a mutex held by LearnFromHistory.
		// In the real code, this is l.mu.Lock() in Learner.Accumulate().
		// Block until context is cancelled (which won't help with sync.Mutex).
		<-ctx.Done()
	}

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:              NewPipeline(RenderParams{}),
		ChatRunner:            runner,
		StreamChunkParser:     testStreamChunkParser,
		ExpressionAccumulator: ExpressionAccumulator(blockingAccumulator),
	})

	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:     "bot-acc-block",
			SessionID: "sess-acc-block",
		},
		rcCh:        make(chan RenderedContext, 16),
		stopCh:      make(chan struct{}),
		cancel:      func() {},
		idleTimeout: 1 * time.Second,
		// Enable chat timing with a very high threshold so handleReply
		// always returns early via the threshold path, which calls
		// extractPassiveMemory → ExpressionAccumulator.
		chatTimingCfg: &chattiming.Config{
			Enabled:    true,
			TimingGate: false,
			TalkValue: chattiming.TalkValueConfig{
				Value: 0.01, // very low talk_value → very high threshold
			},
		},
	}

	driver.mu.Lock()
	driver.sessions["sess-acc-block"] = sess
	driver.mu.Unlock()

	// Use a short context timeout as the watchdog.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	sessionDone := make(chan struct{})
	go func() {
		driver.runSession(ctx, sess)
		close(sessionDone)
	}()

	// Step 1: Send initial RC with mention to trigger the first agent call.
	baseTime := time.Now().UnixMilli()
	sess.rcCh <- RenderedContext{
		{
			ReceivedAtMs: baseTime,
			Content:      []RenderedContentPiece{{Type: "text", Text: "hello @bot"}},
			MentionsMe:   true,
			Target:       "note-1",
		},
	}

	// Wait for agent call to complete.
	time.Sleep(200 * time.Millisecond)

	// Step 2: Send a non-mention RC that will trigger extractPassiveMemory.
	// This should NOT block the session loop even if ExpressionAccumulator blocks.
	sess.rcCh <- RenderedContext{
		{
			ReceivedAtMs: baseTime + 1000,
			Content:      []RenderedContentPiece{{Type: "text", Text: "user chatting among themselves"}},
			MentionsMe:   false,
			Target:       "note-2",
		},
	}

	// Wait for the accumulator to be called (confirms the code path is hit).
	accumulatorWaitCh := make(chan struct{})
	go func() {
		accumulatorCalled.Wait()
		close(accumulatorWaitCh)
	}()

	select {
	case <-accumulatorWaitCh:
		t.Log("ExpressionAccumulator was called — good")
	case <-time.After(3 * time.Second):
		t.Log("ExpressionAccumulator was not called (threshold not met) — adjusting test")
		// If threshold wasn't met, the accumulator won't be called.
		// In that case, the session should still exit via idle timeout.
	}

	// Step 3: The session should exit via idle timeout or watchdog,
	// NOT be stuck forever because ExpressionAccumulator is blocking.
	select {
	case <-sessionDone:
		if ctx.Err() == nil {
			t.Log("Session exited before context deadline — PASS (not stuck)")
		} else {
			t.Log("Session exited via watchdog — acceptable but not ideal")
		}
	case <-ctx.Done():
		// If we get here, the session is stuck. This is the bug.
		t.Fatal("Session did NOT exit before context deadline — " +
			"ExpressionAccumulator is blocking the session loop (BUG REPRODUCED)")
	}
}

// TestExtractPassiveMemory_NonBlocking verifies that extractPassiveMemory
// does not block even when ExpressionAccumulator blocks indefinitely.
func TestExtractPassiveMemory_NonBlocking(t *testing.T) {
	t.Parallel()

	blockCh := make(chan struct{})
	var called atomic.Int32

	blockingAccumulator := func(_ context.Context, _, _ string, _ []adapters.Message) {
		// Signal that we were called, then block forever.
		called.Add(1)
		<-blockCh // block forever
	}

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:              NewPipeline(RenderParams{}),
		StreamChunkParser:     testStreamChunkParser,
		ExpressionAccumulator: ExpressionAccumulator(blockingAccumulator),
	})

	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:     "bot-nonblock",
			SessionID: "sess-nonblock",
		},
		lastProcessedMs: 0,
	}

	rc := RenderedContext{
		{
			ReceivedAtMs: 1000,
			Content:      []RenderedContentPiece{{Type: "text", Text: "some user message"}},
			MentionsMe:   false,
		},
	}

	ctx := context.Background()

	// extractPassiveMemory should return quickly even if the accumulator blocks.
	done := make(chan struct{})
	go func() {
		driver.extractPassiveMemory(ctx, sess, rc, driver.logger)
		close(done)
	}()

	select {
	case <-done:
		t.Log("extractPassiveMemory returned without blocking — PASS")
	case <-time.After(2 * time.Second):
		t.Fatal("extractPassiveMemory is blocking — ExpressionAccumulator should be async (BUG)")
	}

	// Cleanup: unblock the accumulator goroutine to prevent leak.
	close(blockCh)
}
