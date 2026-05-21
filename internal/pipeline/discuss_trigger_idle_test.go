package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/chattiming"
)

// TestRunSession_IdleNotResetOnNoReply reproduces the bug where a flood of
// notes keeps resetting the idle timer even though the bot never actually
// replies (timing threshold not met). This causes the session to stay alive
// until the watchdog timeout (~21 min) instead of exiting after idle timeout.
//
// Scenario: session starts, first agent call completes (tool_calls=0), then
// many notes arrive in quick succession. Each note triggers handleReply which
// returns early (threshold not met), but the idle timer was being reset on
// every incoming RC regardless. The session should exit via idle timeout
// shortly after the last meaningful interaction.
func TestRunSession_IdleNotResetOnNoReply(t *testing.T) {
	t.Parallel()

	runner := &fakeChatRunner{}

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		ChatRunner:        runner,
		StreamChunkParser: testStreamChunkParser,
	})

	// Use a short idle timeout (1s) so the test completes quickly.
	// The context deadline (8s) acts as the watchdog.
	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:     "bot-idle-flood",
			SessionID: "sess-idle-flood",
		},
		rcCh:        make(chan RenderedContext, 16),
		stopCh:      make(chan struct{}),
		cancel:      func() {},
		idleTimeout: 1 * time.Second, // short idle for testing
		// Enable chat timing with a very high threshold so handleReply always
		// returns early without calling the agent (simulating the no_reply path).
		chatTimingCfg: &chattiming.Config{
			Enabled:    true,
			TimingGate: false, // disable gate to avoid needing a real LLM
			TalkValue: chattiming.TalkValueConfig{
				Value: 0.01, // very low talk_value → very high threshold
			},
		},
	}

	// Register the session.
	driver.mu.Lock()
	driver.sessions["sess-idle-flood"] = sess
	driver.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	sessionDone := make(chan struct{})
	go func() {
		driver.runSession(ctx, sess)
		close(sessionDone)
	}()

	// Step 1: Send initial RC with mention to trigger the first agent call.
	// This resets the idle timer (agent was called = meaningful interaction).
	baseTime := time.Now().UnixMilli()
	sess.rcCh <- RenderedContext{
		{
			ReceivedAtMs: baseTime,
			Content:      []RenderedContentPiece{{Type: "text", Text: "hello @bot"}},
			MentionsMe:   true,
			Target:       "note-1",
		},
	}

	// Wait for the agent call to complete, then flood with non-mention notes.
	time.Sleep(200 * time.Millisecond)

	// Step 2: Flood with non-mention notes (simulating ce_observe news, timeline activity).
	// These should NOT reset the idle timer since they don't trigger agent calls.
	// We send them over ~1.5s which is longer than the 1s idle timeout.
	for i := 0; i < 15; i++ {
		time.Sleep(100 * time.Millisecond)
		sess.rcCh <- RenderedContext{
			{
				ReceivedAtMs: baseTime + int64((i+1)*100),
				Content:      []RenderedContentPiece{{Type: "text", Text: "unrelated timeline noise"}},
				MentionsMe:   false,
				Target:       "note-noise",
			},
		}
	}

	// Step 3: After flooding, the session should exit via idle timeout.
	// With the bug: idle timer keeps getting reset → session stays alive until 8s deadline.
	// With the fix: idle timer fires ~1s after the last agent call → session exits early.
	select {
	case <-sessionDone:
		// Session exited — check if it was via idle or watchdog.
		if ctx.Err() == nil {
			t.Log("Session exited via idle timeout — PASS")
		} else {
			t.Fatal("Session exited but context was already cancelled (watchdog behavior)")
		}
	case <-ctx.Done():
		t.Fatal("Session did NOT exit before context deadline — idle timer was being reset by no-reply RCs (BUG REPRODUCED)")
	}

	if runner.lastReq == nil {
		t.Fatal("expected at least one agent call for the initial mention")
	}
}

// TestRunSession_IdleResetOnActualReply verifies that the idle timer IS reset
// when the agent actually processes a message (even if it doesn't send output).
// This ensures we don't break the normal case where the bot is actively engaged.
func TestRunSession_IdleResetOnActualReply(t *testing.T) {
	t.Parallel()

	runner := &fakeChatRunner{}

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		ChatRunner:        runner,
		StreamChunkParser: testStreamChunkParser,
	})

	// Use a 2s idle timeout. We'll send agent-triggering RCs (with mentions
	// to bypass the 15s cooldown) every 500ms, which should keep resetting
	// the idle timer. After we stop sending, the session should exit ~2s later.
	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:     "bot-idle-active",
			SessionID: "sess-idle-active",
		},
		rcCh:        make(chan RenderedContext, 16),
		stopCh:      make(chan struct{}),
		cancel:      func() {},
		idleTimeout: 2 * time.Second,
		// No chat timing config → handleReply always calls the agent.
	}

	driver.mu.Lock()
	driver.sessions["sess-idle-active"] = sess
	driver.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sessionDone := make(chan struct{})
	go func() {
		driver.runSession(ctx, sess)
		close(sessionDone)
	}()

	// Send RCs that will trigger agent calls (no timing gate, no threshold).
	// Use mentions to bypass the 15s agent cooldown between calls.
	// Each call resets the idle timer, keeping the session alive.
	baseTime := time.Now().UnixMilli()
	for i := 0; i < 4; i++ {
		time.Sleep(500 * time.Millisecond)
		sess.rcCh <- RenderedContext{
			{
				ReceivedAtMs: baseTime + int64((i+1)*500),
				Content:      []RenderedContentPiece{{Type: "text", Text: "message that triggers agent"}},
				MentionsMe:   true, // bypass cooldown
				Target:       "note-active",
			},
		}
	}

	// After the last agent call, the session should stay alive for ~1s (idle timeout).
	// Verify it doesn't exit immediately (idle was reset by agent calls).
	time.Sleep(500 * time.Millisecond)

	select {
	case <-sessionDone:
		if ctx.Err() != nil {
			return
		}
		t.Fatal("Session exited too quickly — idle timer should have been reset by agent calls")
	default:
		t.Log("Session still alive after agent calls — idle timer correctly reset")
	}

	// Now wait for idle timeout to fire (~1s more).
	select {
	case <-sessionDone:
		if ctx.Err() == nil {
			t.Log("Session exited via idle timeout after agent calls stopped — PASS")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Session did not exit via idle timeout after agent calls stopped")
	}
}
