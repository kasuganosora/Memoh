package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/memohai/memoh/internal/channel"
	"github.com/memohai/memoh/internal/conversation"
)

// testStreamChunkParser is a simple parser for tests that handles agent_end and text_delta events.
func testStreamChunkParser(chunk conversation.StreamChunk) ([]channel.StreamEvent, []conversation.ModelMessage, error) {
	if len(chunk) == 0 {
		return nil, nil, nil
	}
	var envelope struct {
		Type     string                      `json:"type"`
		Delta    string                      `json:"delta"`
		Messages []conversation.ModelMessage `json:"messages"`
	}
	if err := json.Unmarshal(chunk, &envelope); err != nil {
		return nil, nil, err
	}
	switch strings.ToLower(envelope.Type) {
	case "agent_end":
		return []channel.StreamEvent{{Type: channel.StreamEventAgentEnd}}, envelope.Messages, nil
	case "text_delta":
		return []channel.StreamEvent{{Type: channel.StreamEventDelta, Delta: envelope.Delta, Phase: channel.StreamPhaseText}}, envelope.Messages, nil
	default:
		return nil, envelope.Messages, nil
	}
}

func TestHandleReplyWithAgent_CallsStreamChat(t *testing.T) {
	t.Parallel()

	runner := &fakeChatRunner{}

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		ChatRunner:        runner,
		StreamChunkParser: testStreamChunkParser,
	})

	rc := RenderedContext{
		{
			ReceivedAtMs: 200,
			Content:      []RenderedContentPiece{{Type: "text", Text: "hello world"}},
		},
	}

	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:     "bot-1",
			SessionID: "sess-1",
		},
		lastProcessedMs: 0,
	}

	driver.handleReplyWithAgent(context.Background(), sess, rc, driver.logger)

	if runner.lastReq == nil {
		t.Fatal("expected StreamChat to be called")
	}
	if runner.lastReq.BotID != "bot-1" {
		t.Fatalf("expected bot-1, got %q", runner.lastReq.BotID)
	}
	if runner.lastReq.SessionID != "sess-1" {
		t.Fatalf("expected sess-1, got %q", runner.lastReq.SessionID)
	}
	if runner.lastReq.SessionType != "discuss" {
		t.Fatalf("expected discuss session type, got %q", runner.lastReq.SessionType)
	}
	if runner.lastReq.DiscussLateBindingPrompt == "" {
		t.Fatal("expected late-binding prompt to be set")
	}
	if !runner.lastReq.UserMessagePersisted {
		t.Fatal("expected UserMessagePersisted to be true")
	}
}

func TestHandleReplyWithAgent_AdvancesLastProcessedMs(t *testing.T) {
	t.Parallel()

	runner := &fakeChatRunner{}

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
			BotID:     "bot-1",
			SessionID: "sess-1",
		},
		lastProcessedMs: 0,
	}

	driver.handleReplyWithAgent(context.Background(), sess, rc, driver.logger)

	if sess.lastProcessedMs == 0 {
		t.Fatal("expected lastProcessedMs to be advanced")
	}
}

func TestHandleReplyWithAgent_NoChatRunner(t *testing.T) {
	t.Parallel()

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		StreamChunkParser: testStreamChunkParser,
		// ChatRunner intentionally nil
	})

	rc := RenderedContext{
		{
			ReceivedAtMs: 200,
			Content:      []RenderedContentPiece{{Type: "text", Text: "hello"}},
		},
	}

	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:     "bot-1",
			SessionID: "sess-1",
		},
		lastProcessedMs: 0,
	}

	// Should not panic; just log error and return.
	driver.handleReplyWithAgent(context.Background(), sess, rc, driver.logger)

	if sess.lastProcessedMs == 0 {
		t.Fatal("expected lastProcessedMs to be advanced even on error")
	}
}

func TestHandleReplyWithAgent_OutboundStreamDelivery(t *testing.T) {
	t.Parallel()

	runner := &fakeChatRunnerWithTextDelta{}
	sender := &fakeChannelSender{}
	broadcaster := &fakeBroadcaster{}

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		ChatRunner:        runner,
		ChannelSender:     sender,
		Broadcaster:       broadcaster,
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
			BotID:           "bot-1",
			SessionID:       "sess-1",
			CurrentPlatform: "telegram",
			ReplyTarget:     "chat-123",
		},
		lastProcessedMs: 0,
	}

	driver.handleReplyWithAgent(context.Background(), sess, rc, driver.logger)

	if runner.lastReq == nil {
		t.Fatal("expected StreamChat to be called")
	}

	// Verify outbound stream was opened and events were pushed.
	if !sender.openStreamCalled {
		t.Fatal("expected outbound stream to be opened")
	}
	if len(sender.stream.events) == 0 {
		t.Fatal("expected events to be pushed to outbound stream")
	}
	if !sender.stream.closed {
		t.Fatal("expected outbound stream to be closed")
	}

	// Verify broadcaster also received events.
	if len(broadcaster.events) == 0 {
		t.Fatal("expected events to be broadcast to WebUI")
	}
}

// --- Test helpers ---

type fakeChatRunner struct {
	lastReq *conversation.ChatRequest
}

func (f *fakeChatRunner) StreamChat(_ context.Context, req conversation.ChatRequest) (<-chan conversation.StreamChunk, <-chan error) {
	f.lastReq = &req
	chunkCh := make(chan conversation.StreamChunk, 1)
	errCh := make(chan error, 1)

	// Simulate a minimal successful stream with a terminal event.
	evt := map[string]any{
		"type":     "agent_end",
		"messages": []any{},
	}
	data, _ := json.Marshal(evt)
	chunkCh <- conversation.StreamChunk(data)

	close(chunkCh)
	close(errCh)
	return chunkCh, errCh
}

type fakeChatRunnerWithTextDelta struct {
	lastReq *conversation.ChatRequest
}

func (f *fakeChatRunnerWithTextDelta) StreamChat(_ context.Context, req conversation.ChatRequest) (<-chan conversation.StreamChunk, <-chan error) {
	f.lastReq = &req
	chunkCh := make(chan conversation.StreamChunk, 3)
	errCh := make(chan error, 1)

	// Simulate a text delta followed by agent_end.
	delta := map[string]any{"type": "text_delta", "delta": "Hello world!"}
	deltaData, _ := json.Marshal(delta)
	chunkCh <- conversation.StreamChunk(deltaData)

	end := map[string]any{"type": "agent_end", "messages": []any{}}
	endData, _ := json.Marshal(end)
	chunkCh <- conversation.StreamChunk(endData)

	close(chunkCh)
	close(errCh)
	return chunkCh, errCh
}

type fakeChannelSender struct {
	openStreamCalled bool
	stream           *fakeOutboundStream
}

func (f *fakeChannelSender) GetReplySender(_ string, _ channel.ChannelType) (channel.StreamReplySender, error) {
	f.stream = &fakeOutboundStream{}
	f.openStreamCalled = true
	return &fakeReplySender{stream: f.stream}, nil
}

type fakeReplySender struct {
	stream *fakeOutboundStream
}

func (*fakeReplySender) Send(_ context.Context, _ channel.OutboundMessage) error {
	return nil
}

func (f *fakeReplySender) OpenStream(_ context.Context, _ string, _ channel.StreamOptions) (channel.OutboundStream, error) {
	return f.stream, nil
}

type fakeOutboundStream struct {
	events []channel.StreamEvent
	closed bool
}

func (f *fakeOutboundStream) Push(_ context.Context, event channel.StreamEvent) error {
	f.events = append(f.events, event)
	return nil
}

func (f *fakeOutboundStream) Close(_ context.Context) error {
	f.closed = true
	return nil
}

type fakeBroadcaster struct {
	events []channel.StreamEvent
}

func (f *fakeBroadcaster) PublishEvent(_ string, event channel.StreamEvent) {
	f.events = append(f.events, event)
}

// --- Phase 3: Dispatcher integration tests ---

type fakeDiscussDispatcher struct {
	mu              sync.Mutex
	markActiveCalled bool
	markDoneCalled   bool
	activeRoutes     map[string]bool
	injectCh         chan conversation.InjectMessage
	queuedNotifs     []DiscussQueuedNotification
}

func newFakeDiscussDispatcher() *fakeDiscussDispatcher {
	return &fakeDiscussDispatcher{
		activeRoutes: make(map[string]bool),
		injectCh:     make(chan conversation.InjectMessage, 16),
	}
}

func (f *fakeDiscussDispatcher) IsActive(routeID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeRoutes[routeID]
}

func (f *fakeDiscussDispatcher) MarkActive(routeID string) <-chan conversation.InjectMessage {
	f.mu.Lock()
	f.markActiveCalled = true
	f.activeRoutes[routeID] = true
	f.mu.Unlock()
	return f.injectCh
}

func (f *fakeDiscussDispatcher) MarkDone(routeID string) DiscussMarkDoneResult {
	f.mu.Lock()
	f.markDoneCalled = true
	delete(f.activeRoutes, routeID)
	notifs := f.queuedNotifs
	f.queuedNotifs = nil
	f.mu.Unlock()
	return DiscussMarkDoneResult{QueuedNotifications: notifs}
}

func (f *fakeDiscussDispatcher) Inject(routeID string, msg conversation.InjectMessage) bool {
	f.mu.Lock()
	active := f.activeRoutes[routeID]
	f.mu.Unlock()
	if !active {
		return false
	}
	select {
	case f.injectCh <- msg:
		return true
	default:
		return false
	}
}

func TestHandleReplyWithAgent_DispatcherMarkActiveAndDone(t *testing.T) {
	t.Parallel()

	runner := &fakeChatRunner{}
	disp := newFakeDiscussDispatcher()

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		ChatRunner:        runner,
		Dispatcher:        disp,
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
			BotID:     "bot-1",
			SessionID: "sess-1",
			RouteID:   "route-discuss-1",
		},
		lastProcessedMs: 0,
	}

	driver.handleReplyWithAgent(context.Background(), sess, rc, driver.logger)

	disp.mu.Lock()
	if !disp.markActiveCalled {
		disp.mu.Unlock()
		t.Fatal("expected dispatcher.MarkActive to be called")
	}
	if !disp.markDoneCalled {
		disp.mu.Unlock()
		t.Fatal("expected dispatcher.MarkDone to be called")
	}
	disp.mu.Unlock()
	// After MarkDone, route should no longer be active.
	if disp.IsActive("route-discuss-1") {
		t.Fatal("expected route to be inactive after MarkDone")
	}
}

func TestHandleReplyWithAgent_InjectChPassedToChatRequest(t *testing.T) {
	t.Parallel()

	runner := &fakeChatRunner{}
	disp := newFakeDiscussDispatcher()

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		ChatRunner:        runner,
		Dispatcher:        disp,
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
			BotID:     "bot-1",
			SessionID: "sess-1",
			RouteID:   "route-discuss-2",
		},
		lastProcessedMs: 0,
	}

	driver.handleReplyWithAgent(context.Background(), sess, rc, driver.logger)

	if runner.lastReq == nil {
		t.Fatal("expected StreamChat to be called")
	}
	if runner.lastReq.InjectCh == nil {
		t.Fatal("expected InjectCh to be set in ChatRequest when dispatcher is configured")
	}
}

func TestHandleReplyWithAgent_NoDispatcher_NoInjectCh(t *testing.T) {
	t.Parallel()

	runner := &fakeChatRunner{}

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		ChatRunner:        runner,
		StreamChunkParser: testStreamChunkParser,
		// Dispatcher intentionally nil
	})

	rc := RenderedContext{
		{
			ReceivedAtMs: 200,
			Content:      []RenderedContentPiece{{Type: "text", Text: "hello"}},
		},
	}

	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:     "bot-1",
			SessionID: "sess-1",
			RouteID:   "route-discuss-3",
		},
		lastProcessedMs: 0,
	}

	driver.handleReplyWithAgent(context.Background(), sess, rc, driver.logger)

	if runner.lastReq == nil {
		t.Fatal("expected StreamChat to be called")
	}
	if runner.lastReq.InjectCh != nil {
		t.Fatal("expected InjectCh to be nil when dispatcher is not configured")
	}
}

func TestHandleReplyWithAgent_DispatcherQueueDrain(t *testing.T) {
	t.Parallel()

	runner := &fakeChatRunner{}
	disp := newFakeDiscussDispatcher()

	// Pre-queue a notification that should be replayed after the agent completes.
	queuedRC := RenderedContext{
		{
			ReceivedAtMs: 300,
			Content:      []RenderedContentPiece{{Type: "text", Text: "queued msg"}},
		},
	}
	disp.mu.Lock()
	disp.queuedNotifs = []DiscussQueuedNotification{
		{
			SessionID: "sess-1",
			RC:        queuedRC,
			Config: DiscussSessionConfig{
				BotID:     "bot-1",
				SessionID: "sess-1",
				RouteID:   "route-discuss-4",
			},
		},
	}
	disp.mu.Unlock()

	driver := NewDiscussTrigger(DiscussTriggerDeps{
		Pipeline:          NewPipeline(RenderParams{}),
		ChatRunner:        runner,
		Dispatcher:        disp,
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
			BotID:     "bot-1",
			SessionID: "sess-1",
			RouteID:   "route-discuss-4",
		},
		lastProcessedMs: 0,
	}

	driver.handleReplyWithAgent(context.Background(), sess, rc, driver.logger)

	// After MarkDone, the queued notification should have been replayed via NotifyRC.
	// Since the session already exists, the RC should be pushed to the session's rcCh.
	disp.mu.Lock()
	if !disp.markDoneCalled {
		disp.mu.Unlock()
		t.Fatal("expected dispatcher.MarkDone to be called")
	}
	// The queued notifications should have been consumed.
	if len(disp.queuedNotifs) != 0 {
		disp.mu.Unlock()
		t.Fatalf("expected queued notifications to be consumed, got %d", len(disp.queuedNotifs))
	}
	disp.mu.Unlock()
}
