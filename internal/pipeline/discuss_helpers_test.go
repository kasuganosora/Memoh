package pipeline

import (
	"context"
	"testing"
	"time"
)

func TestLatestReplyTarget_PrefersMentionsMe(t *testing.T) {
	t.Parallel()

	nowMs := time.Now().UnixMilli()
	rc := RenderedContext{
		{
			ReceivedAtMs: nowMs - 60000, // 1 min ago
			Content:      []RenderedContentPiece{{Type: "text", Text: "normal msg"}},
			Target:       "note-100",
		},
		{
			ReceivedAtMs: nowMs - 30000, // 30s ago
			Content:      []RenderedContentPiece{{Type: "text", Text: "@bot hello"}},
			MentionsMe:   true,
			Target:       "note-200",
		},
		{
			ReceivedAtMs: nowMs - 10000, // 10s ago
			Content:      []RenderedContentPiece{{Type: "text", Text: "another msg"}},
			Target:       "note-300",
		},
	}

	got := latestReplyTarget(rc, 0)
	if got != "note-200" {
		t.Fatalf("expected note-200 (mentions_me), got %q", got)
	}
}

func TestLatestReplyTarget_FallsBackToLatestNonSelf(t *testing.T) {
	t.Parallel()

	nowMs := time.Now().UnixMilli()
	rc := RenderedContext{
		{
			ReceivedAtMs: nowMs - 60000,
			Content:      []RenderedContentPiece{{Type: "text", Text: "msg1"}},
			Target:       "note-100",
		},
		{
			ReceivedAtMs: nowMs - 30000,
			Content:      []RenderedContentPiece{{Type: "text", Text: "msg2"}},
			Target:       "note-200",
		},
	}

	got := latestReplyTarget(rc, 0)
	if got != "note-200" {
		t.Fatalf("expected note-200 (latest), got %q", got)
	}
}

func TestLatestReplyTarget_SkipsSelfMessages(t *testing.T) {
	t.Parallel()

	nowMs := time.Now().UnixMilli()
	rc := RenderedContext{
		{
			ReceivedAtMs: nowMs - 60000,
			Content:      []RenderedContentPiece{{Type: "text", Text: "user msg"}},
			Target:       "note-100",
		},
		{
			ReceivedAtMs: nowMs - 30000,
			Content:      []RenderedContentPiece{{Type: "text", Text: "my reply"}},
			IsMyself:     true,
			Target:       "note-200",
		},
	}

	got := latestReplyTarget(rc, 0)
	if got != "note-100" {
		t.Fatalf("expected note-100 (skip self), got %q", got)
	}
}

func TestLatestReplyTarget_RespectsAfterMs(t *testing.T) {
	t.Parallel()

	rc := RenderedContext{
		{
			ReceivedAtMs: 100,
			Content:      []RenderedContentPiece{{Type: "text", Text: "old msg"}},
			MentionsMe:   true,
			Target:       "note-100",
		},
		{
			ReceivedAtMs: 200,
			Content:      []RenderedContentPiece{{Type: "text", Text: "new msg"}},
			Target:       "note-200",
		},
	}

	// afterMs=150 should skip note-100
	got := latestReplyTarget(rc, 150)
	if got != "note-200" {
		t.Fatalf("expected note-200 (after filter), got %q", got)
	}
}

func TestLatestReplyTarget_EmptyRC(t *testing.T) {
	t.Parallel()

	got := latestReplyTarget(RenderedContext{}, 0)
	if got != "" {
		t.Fatalf("expected empty string for empty RC, got %q", got)
	}
}

func TestLatestReplyTarget_NoTarget(t *testing.T) {
	t.Parallel()

	nowMs := time.Now().UnixMilli()
	rc := RenderedContext{
		{
			ReceivedAtMs: nowMs - 30000,
			Content:      []RenderedContentPiece{{Type: "text", Text: "msg without target"}},
			// Target is empty
		},
	}

	got := latestReplyTarget(rc, 0)
	if got != "" {
		t.Fatalf("expected empty string when no target, got %q", got)
	}
}

func TestLatestReplyTarget_RepliesToMeAlsoPreferred(t *testing.T) {
	t.Parallel()

	nowMs := time.Now().UnixMilli()
	rc := RenderedContext{
		{
			ReceivedAtMs: nowMs - 60000,
			Content:      []RenderedContentPiece{{Type: "text", Text: "normal"}},
			Target:       "note-100",
		},
		{
			ReceivedAtMs: nowMs - 30000,
			Content:      []RenderedContentPiece{{Type: "text", Text: "reply to bot"}},
			RepliesToMe:  true,
			Target:       "note-200",
		},
		{
			ReceivedAtMs: nowMs - 10000,
			Content:      []RenderedContentPiece{{Type: "text", Text: "later msg"}},
			Target:       "note-300",
		},
	}

	got := latestReplyTarget(rc, 0)
	if got != "note-200" {
		t.Fatalf("expected note-200 (replies_to_me), got %q", got)
	}
}

func TestLatestReplyTarget_IgnoresStaleSegmentsOnNewSession(t *testing.T) {
	t.Parallel()

	// Segments older than 5 minutes should be ignored when afterMs=0.
	staleMs := time.Now().Add(-10 * time.Minute).UnixMilli()
	rc := RenderedContext{
		{
			ReceivedAtMs: staleMs,
			Content:      []RenderedContentPiece{{Type: "text", Text: "old mention"}},
			MentionsMe:   true,
			Target:       "note-stale",
		},
	}

	got := latestReplyTarget(rc, 0)
	if got != "" {
		t.Fatalf("expected empty string for stale segments, got %q", got)
	}
}

func TestNotifyRC_UpdatesReplyTarget(t *testing.T) {
	t.Parallel()

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		StreamChunkParser: testStreamChunkParser,
	})

	rc := RenderedContext{
		{
			ReceivedAtMs: 100,
			Content:      []RenderedContentPiece{{Type: "text", Text: "hello"}},
			Target:       "note-first",
		},
	}

	// First call creates the session with initial ReplyTarget.
	config := DiscussSessionConfig{
		BotID:       "bot-1",
		SessionID:   "sess-1",
		ReplyTarget: "note-first",
	}
	driver.NotifyRC(context.Background(), "sess-1", rc, config)

	// Verify session was created.
	driver.mu.Lock()
	sess := driver.sessions["sess-1"]
	driver.mu.Unlock()
	if sess == nil {
		t.Fatal("expected session to be created")
		return
	}
	sess.configMu.RLock()
	firstTarget := sess.config.ReplyTarget
	sess.configMu.RUnlock()
	if firstTarget != "note-first" {
		t.Fatalf("expected ReplyTarget=note-first, got %q", firstTarget)
	}

	// Second call should update ReplyTarget.
	config2 := DiscussSessionConfig{
		BotID:       "bot-1",
		SessionID:   "sess-1",
		ReplyTarget: "note-second",
	}
	driver.NotifyRC(context.Background(), "sess-1", rc, config2)

	sess.configMu.RLock()
	updatedTarget := sess.config.ReplyTarget
	sess.configMu.RUnlock()
	if updatedTarget != "note-second" {
		t.Fatalf("expected ReplyTarget=note-second after update, got %q", updatedTarget)
	}

	// Cleanup
	driver.StopSession("sess-1")
}

func TestNotifyRC_DoesNotClearReplyTarget(t *testing.T) {
	t.Parallel()

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		StreamChunkParser: testStreamChunkParser,
	})

	rc := RenderedContext{
		{
			ReceivedAtMs: 100,
			Content:      []RenderedContentPiece{{Type: "text", Text: "hello"}},
		},
	}

	// First call creates the session with a ReplyTarget.
	config := DiscussSessionConfig{
		BotID:       "bot-1",
		SessionID:   "sess-2",
		ReplyTarget: "note-original",
	}
	driver.NotifyRC(context.Background(), "sess-2", rc, config)

	// Second call with empty ReplyTarget should NOT clear it.
	config2 := DiscussSessionConfig{
		BotID:       "bot-1",
		SessionID:   "sess-2",
		ReplyTarget: "",
	}
	driver.NotifyRC(context.Background(), "sess-2", rc, config2)

	driver.mu.Lock()
	sess := driver.sessions["sess-2"]
	driver.mu.Unlock()
	sess.configMu.RLock()
	target := sess.config.ReplyTarget
	sess.configMu.RUnlock()
	if target != "note-original" {
		t.Fatalf("expected ReplyTarget to remain note-original, got %q", target)
	}

	// Cleanup
	driver.StopSession("sess-2")
}

func TestHandleReplyWithAgent_UpdatesReplyTargetFromRC(t *testing.T) {
	t.Parallel()

	runner := &fakeChatRunner{}

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		ChatRunner:        runner,
		StreamChunkParser: testStreamChunkParser,
	})

	nowMs := time.Now().UnixMilli()
	rc := RenderedContext{
		{
			ReceivedAtMs: nowMs - 60000,
			Content:      []RenderedContentPiece{{Type: "text", Text: "old msg"}},
			Target:       "note-old",
		},
		{
			ReceivedAtMs: nowMs - 30000,
			Content:      []RenderedContentPiece{{Type: "text", Text: "@bot new mention"}},
			MentionsMe:   true,
			Target:       "note-mention",
		},
		{
			ReceivedAtMs: nowMs - 10000,
			Content:      []RenderedContentPiece{{Type: "text", Text: "latest msg"}},
			Target:       "note-latest",
		},
	}

	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:       "bot-1",
			SessionID:   "sess-1",
			ReplyTarget: "note-stale",
		},
		lastProcessedMs: 0,
	}

	driver.handleReplyWithAgent(context.Background(), sess, rc, driver.logger)

	// Should prefer the mentions_me message's target.
	if sess.config.ReplyTarget != "note-mention" {
		t.Fatalf("expected ReplyTarget=note-mention, got %q", sess.config.ReplyTarget)
	}
	// ChatRequest should also use the updated target.
	if runner.lastReq.ReplyTarget != "note-mention" {
		t.Fatalf("expected ChatRequest.ReplyTarget=note-mention, got %q", runner.lastReq.ReplyTarget)
	}
}

func TestHandleReplyWithAgent_FallsBackToLatestTarget(t *testing.T) {
	t.Parallel()

	runner := &fakeChatRunner{}

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		ChatRunner:        runner,
		StreamChunkParser: testStreamChunkParser,
	})

	nowMs := time.Now().UnixMilli()
	rc := RenderedContext{
		{
			ReceivedAtMs: nowMs - 60000,
			Content:      []RenderedContentPiece{{Type: "text", Text: "msg1"}},
			Target:       "note-100",
		},
		{
			ReceivedAtMs: nowMs - 30000,
			Content:      []RenderedContentPiece{{Type: "text", Text: "msg2"}},
			Target:       "note-200",
		},
	}

	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:       "bot-1",
			SessionID:   "sess-1",
			ReplyTarget: "note-stale",
		},
		lastProcessedMs: 0,
	}

	driver.handleReplyWithAgent(context.Background(), sess, rc, driver.logger)

	// No mentions_me, should fall back to latest target.
	if sess.config.ReplyTarget != "note-200" {
		t.Fatalf("expected ReplyTarget=note-200, got %q", sess.config.ReplyTarget)
	}
}
