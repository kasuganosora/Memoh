package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/chattiming"
	"github.com/memohai/memoh/internal/conversation"
	"github.com/memohai/memoh/internal/memory/adapters"
)

// TestRunSession_WatchdogEffectiveWhenHandleReplyBlocks reproduces the bug
// where the watchdog timeout is completely ineffective when handleReply blocks
// on a synchronous operation that doesn't respond to context cancellation.
//
// Root cause: runSession's for-loop only checks ctx.Done() in the top-level
// select statement. Once code enters handleReply, if any synchronous call
// blocks (e.g. sync.Mutex.Lock, blocking function call), the watchdog context
// cancellation is never detected because the code never returns to select.
//
// Expected behavior after fix: The session should exit within a bounded time
// even when handleReply is stuck in a blocking operation.
func TestRunSession_WatchdogEffectiveWhenHandleReplyBlocks(t *testing.T) {
	t.Parallel()

	// blockingRunner simulates a ChatRunner that blocks forever in StreamChat,
	// never closing channels and not responding to context cancellation.
	// This simulates the scenario where the LLM provider connection hangs
	// at a level below context awareness (e.g. TCP half-open).
	var streamCalled sync.WaitGroup
	streamCalled.Add(1)

	blockForever := make(chan struct{}) // never closed during test
	runner := &funcChatRunner{
		streamChat: func(_ context.Context, _ conversation.ChatRequest) (<-chan conversation.StreamChunk, <-chan error) {
			streamCalled.Done()
			chunkCh := make(chan conversation.StreamChunk)
			errCh := make(chan error)
			// Simulate a completely hung connection that NEVER responds to ctx.
			// This is the key difference from the existing deadlock test:
			// the channels are never closed, period. This simulates scenarios
			// like sync.Mutex.Lock() blocking or a TCP connection that's
			// completely unresponsive to context cancellation.
			go func() {
				<-blockForever // blocks forever
				close(chunkCh)
				close(errCh)
			}()
			return chunkCh, errCh
		},
	}

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		ChatRunner:        runner,
		StreamChunkParser: testStreamChunkParser,
	})

	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:     "bot-watchdog-effective",
			SessionID: "sess-watchdog-effective",
		},
		rcCh:        make(chan RenderedContext, 16),
		stopCh:      make(chan struct{}),
		cancel:      func() {},
		idleTimeout: 1 * time.Second,
	}

	driver.mu.Lock()
	driver.sessions["sess-watchdog-effective"] = sess
	driver.mu.Unlock()

	// Use a 3-second watchdog timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	sessionDone := make(chan struct{})
	go func() {
		driver.runSession(ctx, sess)
		close(sessionDone)
	}()

	// Send a mention RC to trigger handleReplyWithAgent → StreamChat (which blocks).
	sess.rcCh <- RenderedContext{
		{
			ReceivedAtMs: time.Now().UnixMilli(),
			Content:      []RenderedContentPiece{{Type: "text", Text: "hello @bot"}},
			MentionsMe:   true,
			Target:       "note-1",
		},
	}

	// Wait for StreamChat to be called (confirms we're in the blocking path).
	streamCalled.Wait()

	// The session MUST exit within the watchdog timeout (3s) + reasonable margin.
	// Before the fix, it would hang forever because ctx.Done() is only checked
	// in the top-level select, and handleReplyWithAgent is blocking.
	select {
	case <-sessionDone:
		t.Log("Session exited despite handleReply blocking — watchdog effective")
	case <-time.After(5 * time.Second):
		t.Fatal("Session did NOT exit within watchdog timeout — " +
			"watchdog is ineffective when handleReply blocks (BUG)")
	}
}

// --- Test helpers ---

// funcChatRunner is a ChatRunner backed by a function for flexible test scenarios.
type funcChatRunner struct {
	streamChat func(ctx context.Context, req conversation.ChatRequest) (<-chan conversation.StreamChunk, <-chan error)
}

func (f *funcChatRunner) StreamChat(ctx context.Context, req conversation.ChatRequest) (<-chan conversation.StreamChunk, <-chan error) {
	return f.streamChat(ctx, req)
}

// TestRunSession_WatchdogForcesExitOnUnresponsiveBlock verifies that the
// runSession goroutine-wrapper around handleReply allows the watchdog to
// force-exit even when handleReply is stuck in a completely unresponsive
// blocking operation (one that ignores context cancellation entirely).
//
// This test simulates the exact scenario from the production bug: a
// synchronous call inside handleReply (ExpressionAccumulator → Learner.Accumulate
// → sync.Mutex.Lock) that blocks forever and cannot be interrupted by context.
func TestRunSession_WatchdogForcesExitOnUnresponsiveBlock(t *testing.T) {
	t.Parallel()

	// We need a ChatRunner that will never be called (because the block
	// happens before handleReplyWithAgent in the threshold-not-met path).
	runner := &fakeChatRunner{}

	// Create a blocking ExpressionAccumulator that simulates sync.Mutex.Lock
	// blocking forever — completely ignoring context cancellation.
	blockForever := make(chan struct{}) // never closed
	blockingAccumulator := func(_ context.Context, _, _ string, _ []adapters.Message) {
		<-blockForever // blocks forever, ignores ctx
	}

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:              NewPipeline(RenderParams{}),
		ChatRunner:            runner,
		StreamChunkParser:     testStreamChunkParser,
		ExpressionAccumulator: ExpressionAccumulator(blockingAccumulator),
	})

	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:     "bot-unresponsive",
			SessionID: "sess-unresponsive",
		},
		rcCh:        make(chan RenderedContext, 16),
		stopCh:      make(chan struct{}),
		cancel:      func() {},
		idleTimeout: 1 * time.Second,
		// Enable timing with very low talk_value → very high threshold
		// so handleReply takes the "threshold not met" path which calls
		// extractPassiveMemory → ExpressionAccumulator (blocking).
		chatTimingCfg: &chattiming.Config{
			Enabled:    true,
			TimingGate: false,
			TalkValue: chattiming.TalkValueConfig{
				Value: 0.01,
			},
		},
	}

	driver.mu.Lock()
	driver.sessions["sess-unresponsive"] = sess
	driver.mu.Unlock()

	// 2-second watchdog.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sessionDone := make(chan struct{})
	go func() {
		driver.runSession(ctx, sess)
		close(sessionDone)
	}()

	// Send a non-mention RC to trigger the threshold-not-met path.
	sess.rcCh <- RenderedContext{
		{
			ReceivedAtMs: time.Now().UnixMilli(),
			Content:      []RenderedContentPiece{{Type: "text", Text: "user chatting"}},
			MentionsMe:   false,
			Target:       "note-1",
		},
	}

	// The session MUST exit within the watchdog timeout (2s) + margin.
	// Without the goroutine wrapper, the session would hang forever because
	// ExpressionAccumulator blocks and sync.Mutex.Lock ignores context.
	select {
	case <-sessionDone:
		t.Log("Session exited despite unresponsive block in handleReply — watchdog effective")
	case <-time.After(4 * time.Second):
		t.Fatal("Session did NOT exit within watchdog timeout — " +
			"watchdog is ineffective when handleReply has unresponsive block (BUG)")
	}
}
