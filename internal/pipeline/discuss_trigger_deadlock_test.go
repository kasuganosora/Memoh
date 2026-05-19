package pipeline

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/conversation"
)

// --- Deadlock prevention integration tests ---

// blockingChatRunner simulates a ChatRunner.StreamChat that blocks forever
// until the context is cancelled, reproducing the LLM hang scenario.
type blockingChatRunner struct {
	mu      sync.Mutex
	called  bool
	ctxDone bool
}

func (f *blockingChatRunner) StreamChat(ctx context.Context, req conversation.ChatRequest) (<-chan conversation.StreamChunk, <-chan error) {
	f.mu.Lock()
	f.called = true
	f.mu.Unlock()

	chunkCh := make(chan conversation.StreamChunk, 1)
	errCh := make(chan error, 1)

	// Block until context is cancelled (simulates hung LLM)
	go func() {
		<-ctx.Done()
		f.mu.Lock()
		f.ctxDone = true
		f.mu.Unlock()

		// Send agent_end after cancellation to allow stream consumption to complete
		evt := map[string]any{"type": "agent_end", "messages": []any{}}
		data, _ := json.Marshal(evt)
		chunkCh <- conversation.StreamChunk(data)
		close(chunkCh)
		close(errCh)
	}()

	return chunkCh, errCh
}

func TestHandleReplyWithAgent_HardTimeout(t *testing.T) {
	t.Parallel()

	runner := &blockingChatRunner{}

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		ChatRunner:        runner,
		StreamChunkParser: testStreamChunkParser,
	})

	rc := RenderedContext{
		{
			ReceivedAtMs: 200,
			Content:      []RenderedContentPiece{{Type: "text", Text: "hello"}},
		},
	}

	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:     "bot-timeout",
			SessionID: "sess-timeout",
		},
		lastProcessedMs: 0,
	}

	// Use a short parent context timeout to speed up the test.
	// The handleReplyWithAgent has a 5min hard timeout, but we override
	// by passing a context that expires sooner.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	driver.handleReplyWithAgent(ctx, sess, rc, driver.logger)
	elapsed := time.Since(start)

	// Verify the function returned within a reasonable time (context timeout + margin)
	if elapsed > 5*time.Second {
		t.Fatalf("handleReplyWithAgent took %v, expected to return within ~2s due to context timeout", elapsed)
	}

	runner.mu.Lock()
	if !runner.called {
		t.Fatal("expected StreamChat to be called")
	}
	if !runner.ctxDone {
		t.Fatal("expected context to be cancelled, indicating timeout worked")
	}
	runner.mu.Unlock()
}

func TestRunSession_WatchdogTimeout(t *testing.T) {
	t.Parallel()

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		StreamChunkParser: testStreamChunkParser,
	})

	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:     "bot-watchdog",
			SessionID: "sess-watchdog",
		},
		rcCh:   make(chan RenderedContext, 8),
		stopCh: make(chan struct{}),
	}

	// Register the session in the map
	driver.mu.Lock()
	driver.sessions["sess-watchdog"] = sess
	driver.mu.Unlock()

	// Use a very short-lived parent context to simulate watchdog timeout
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	driver.runSession(ctx, sess)
	elapsed := time.Since(start)

	// Verify session exited due to watchdog (context deadline)
	if elapsed > 3*time.Second {
		t.Fatalf("runSession took %v, expected to exit within ~500ms due to watchdog context", elapsed)
	}

	// Verify session was cleaned up from the map
	driver.mu.Lock()
	_, exists := driver.sessions["sess-watchdog"]
	driver.mu.Unlock()

	if exists {
		t.Fatal("expected session to be removed from sessions map after watchdog exit")
	}
}

func TestRunSession_IdleTimeout(t *testing.T) {
	t.Parallel()

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		StreamChunkParser: testStreamChunkParser,
	})

	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:     "bot-idle",
			SessionID: "sess-idle",
		},
		rcCh:   make(chan RenderedContext, 8),
		stopCh: make(chan struct{}),
	}

	// Register the session
	driver.mu.Lock()
	driver.sessions["sess-idle"] = sess
	driver.mu.Unlock()

	// Use a context with a longer timeout than the idle timer test
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// We can't easily test the full 10-minute idle timeout in a unit test,
	// but we can verify the session exits cleanly via stopCh.
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(sess.stopCh)
	}()

	start := time.Now()
	driver.runSession(ctx, sess)
	elapsed := time.Since(start)

	// Should exit quickly via stopCh
	if elapsed > 2*time.Second {
		t.Fatalf("runSession took %v, expected to exit within ~200ms via stopCh", elapsed)
	}

	// Verify cleanup
	driver.mu.Lock()
	_, exists := driver.sessions["sess-idle"]
	driver.mu.Unlock()

	if exists {
		t.Fatal("expected session to be removed from sessions map after stop")
	}
}

func TestRunSession_SessionMapCleanup(t *testing.T) {
	t.Parallel()

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		StreamChunkParser: testStreamChunkParser,
	})

	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:     "bot-cleanup",
			SessionID: "sess-cleanup",
		},
		rcCh:   make(chan RenderedContext, 8),
		stopCh: make(chan struct{}),
	}

	// Register the session
	driver.mu.Lock()
	driver.sessions["sess-cleanup"] = sess
	driver.mu.Unlock()

	// Immediately close stopCh to trigger exit
	close(sess.stopCh)

	ctx := context.Background()
	driver.runSession(ctx, sess)

	// Verify the session was removed
	driver.mu.Lock()
	_, exists := driver.sessions["sess-cleanup"]
	driver.mu.Unlock()

	if exists {
		t.Fatal("expected session to be cleaned up from sessions map")
	}

	// Verify that a different session with the same ID is NOT removed
	otherSess := &discussSession{
		config: DiscussSessionConfig{
			BotID:     "bot-other",
			SessionID: "sess-cleanup",
		},
		rcCh:   make(chan RenderedContext, 8),
		stopCh: make(chan struct{}),
	}

	driver.mu.Lock()
	driver.sessions["sess-cleanup"] = otherSess
	driver.mu.Unlock()

	// Run the original session again (already stopped)
	sess2 := &discussSession{
		config: DiscussSessionConfig{
			BotID:     "bot-cleanup",
			SessionID: "sess-cleanup",
		},
		rcCh:   make(chan RenderedContext, 8),
		stopCh: make(chan struct{}),
	}
	close(sess2.stopCh)
	driver.runSession(ctx, sess2)

	// The other session should still be in the map (different pointer)
	driver.mu.Lock()
	cur, exists := driver.sessions["sess-cleanup"]
	driver.mu.Unlock()

	if !exists {
		t.Fatal("expected other session to still be in the map")
	}
	if cur != otherSess {
		t.Fatal("expected the other session pointer to be preserved")
	}
}
