package tools

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/channel"
	"github.com/memohai/memoh/internal/messaging"
)

// fakeSender captures send requests for testing.
type fakeSender struct {
	called int
	req    channel.SendRequest
}

func (s *fakeSender) Send(_ context.Context, _ string, _ channel.ChannelType, req channel.SendRequest) error {
	s.called++
	s.req = req
	return nil
}

// fakeResolver resolves any platform string to a ChannelType.
type fakeResolver struct{}

func (fakeResolver) ParseChannelType(raw string) (channel.ChannelType, error) {
	return channel.ChannelType(raw), nil
}

func newTestMessageProvider(sender *fakeSender) *MessageProvider {
	return &MessageProvider{
		exec: &messaging.Executor{
			Sender:   sender,
			Resolver: fakeResolver{},
		},
		logger: slog.Default(),
	}
}

func newAtomicInt32(v int32) *atomic.Int32 {
	a := new(atomic.Int32)
	a.Store(v)
	return a
}

func TestExecSend_DiscussMode_AutoInjectsReplyTo(t *testing.T) {
	t.Parallel()

	sender := &fakeSender{}
	p := newTestMessageProvider(sender)

	session := SessionContext{
		BotID:           "bot-1",
		SessionID:       "sess-1",
		SessionType:     "discuss",
		CurrentPlatform: "misskey",
		ReplyTarget:     "note-target-123",
		IsMentioned:     true,
	}

	args := map[string]any{"text": "hello world"}
	_, err := p.execSend(context.Background(), session, args)
	if err != nil {
		t.Fatalf("execSend returned error: %v", err)
	}

	if sender.called != 1 {
		t.Fatalf("expected sender called once, got %d", sender.called)
	}
	if sender.req.Message.Reply == nil {
		t.Fatal("expected Reply to be auto-injected in discuss mode")
	}
	if sender.req.Message.Reply.MessageID != "note-target-123" {
		t.Fatalf("expected reply_to=note-target-123, got %q", sender.req.Message.Reply.MessageID)
	}
}

func TestExecSend_DiscussMode_DoesNotOverrideExplicitReplyTo(t *testing.T) {
	t.Parallel()

	sender := &fakeSender{}
	p := newTestMessageProvider(sender)

	session := SessionContext{
		BotID:           "bot-1",
		SessionID:       "sess-1",
		SessionType:     "discuss",
		CurrentPlatform: "misskey",
		ReplyTarget:     "note-target-123",
	}

	args := map[string]any{"text": "hello", "reply_to": "note-explicit"}
	_, err := p.execSend(context.Background(), session, args)
	if err != nil {
		t.Fatalf("execSend returned error: %v", err)
	}

	if sender.req.Message.Reply == nil {
		t.Fatal("expected Reply to be set")
	}
	if sender.req.Message.Reply.MessageID != "note-explicit" {
		t.Fatalf("expected reply_to=note-explicit, got %q", sender.req.Message.Reply.MessageID)
	}
}

func TestExecSend_DiscussMode_NoInjectWhenTargetSet(t *testing.T) {
	t.Parallel()

	sender := &fakeSender{}
	p := newTestMessageProvider(sender)

	session := SessionContext{
		BotID:           "bot-1",
		SessionID:       "sess-1",
		SessionType:     "discuss",
		CurrentPlatform: "misskey",
		ReplyTarget:     "note-target-123",
	}

	// Cross-conversation send with explicit target.
	args := map[string]any{"text": "hello", "target": "other-note"}
	_, err := p.execSend(context.Background(), session, args)
	if err != nil {
		t.Fatalf("execSend returned error: %v", err)
	}

	// Should NOT auto-inject reply_to when target is specified.
	if sender.req.Message.Reply != nil {
		t.Fatalf("expected no Reply when target is set, got %+v", sender.req.Message.Reply)
	}
}

func TestExecSend_NonDiscussMode_NoAutoInject(t *testing.T) {
	t.Parallel()

	sender := &fakeSender{}
	p := newTestMessageProvider(sender)

	session := SessionContext{
		BotID:           "bot-1",
		SessionID:       "sess-1",
		SessionType:     "normal",
		CurrentPlatform: "misskey",
		ReplyTarget:     "note-target-123",
	}

	args := map[string]any{"text": "hello"}
	_, err := p.execSend(context.Background(), session, args)
	if err != nil {
		t.Fatalf("execSend returned error: %v", err)
	}

	// Non-discuss mode should not auto-inject reply_to at the execSend level.
	// (The SendDirect Misskey fallback may still inject it, but that's tested separately.)
	// In normal mode, Send returns Local=true so SendDirect is not called.
}

func TestExecSend_DiscussMode_NoInjectWhenReplyTargetEmpty(t *testing.T) {
	t.Parallel()

	sender := &fakeSender{}
	p := newTestMessageProvider(sender)

	session := SessionContext{
		BotID:           "bot-1",
		SessionID:       "sess-1",
		SessionType:     "discuss",
		CurrentPlatform: "misskey",
		ReplyTarget:     "",
	}

	args := map[string]any{"text": "hello", "target": "some-target"}
	_, err := p.execSend(context.Background(), session, args)
	if err != nil {
		t.Fatalf("execSend returned error: %v", err)
	}

	if sender.req.Message.Reply != nil {
		t.Fatalf("expected no Reply when ReplyTarget is empty, got %+v", sender.req.Message.Reply)
	}
}

func TestExecSend_DiscussMode_NoInjectWhenNotMentioned(t *testing.T) {
	t.Parallel()

	sender := &fakeSender{}
	p := newTestMessageProvider(sender)

	session := SessionContext{
		BotID:           "bot-1",
		SessionID:       "sess-1",
		SessionType:     "discuss",
		CurrentPlatform: "misskey",
		ReplyTarget:     "note-target-123",
		IsMentioned:     false, // NOT mentioned — should not auto-inject reply_to
	}

	args := map[string]any{"text": "hello"}
	_, err := p.execSend(context.Background(), session, args)
	if err != nil {
		t.Fatalf("execSend returned error: %v", err)
	}

	if sender.req.Message.Reply != nil {
		t.Fatalf("expected no Reply when IsMentioned is false, got %+v", sender.req.Message.Reply)
	}
}

func TestExecReply_DiscussMode_AutoInjectsReplyTo(t *testing.T) {
	t.Parallel()

	sender := &fakeSender{}
	p := newTestMessageProvider(sender)
	// Set a simple text generator that returns the reasoning as-is.
	p.textGen = &fakeTextGenerator{}

	session := SessionContext{
		BotID:           "bot-1",
		SessionID:       "sess-1",
		SessionType:     "discuss",
		CurrentPlatform: "misskey",
		ReplyTarget:     "note-reply-target",
		IsMentioned:     true,
	}

	args := map[string]any{"reasoning": "I want to say hello"}
	_, err := p.execReply(context.Background(), session, args)
	if err != nil {
		t.Fatalf("execReply returned error: %v", err)
	}

	if sender.called != 1 {
		t.Fatalf("expected sender called once, got %d", sender.called)
	}
	if sender.req.Message.Reply == nil {
		t.Fatal("expected Reply to be auto-injected in replyer discuss mode")
	}
	if sender.req.Message.Reply.MessageID != "note-reply-target" {
		t.Fatalf("expected reply_to=note-reply-target, got %q", sender.req.Message.Reply.MessageID)
	}
}

func TestExecReply_DiscussMode_DoesNotOverrideExplicitReplyTo(t *testing.T) {
	t.Parallel()

	sender := &fakeSender{}
	p := newTestMessageProvider(sender)
	p.textGen = &fakeTextGenerator{}

	session := SessionContext{
		BotID:           "bot-1",
		SessionID:       "sess-1",
		SessionType:     "discuss",
		CurrentPlatform: "misskey",
		ReplyTarget:     "note-reply-target",
	}

	args := map[string]any{"reasoning": "hello", "reply_to": "note-explicit-reply"}
	_, err := p.execReply(context.Background(), session, args)
	if err != nil {
		t.Fatalf("execReply returned error: %v", err)
	}

	if sender.req.Message.Reply == nil {
		t.Fatal("expected Reply to be set")
	}
	if sender.req.Message.Reply.MessageID != "note-explicit-reply" {
		t.Fatalf("expected reply_to=note-explicit-reply, got %q", sender.req.Message.Reply.MessageID)
	}
}

// fakeTextGenerator returns the input text as-is for testing.
type fakeTextGenerator struct{}

func (*fakeTextGenerator) GenerateText(_ context.Context, _ string, _ []sdk.Message) (string, error) {
	return "generated reply", nil
}

func TestLooksLikeInternalNote(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want bool
	}{
		{"empty", "", false},
		{"normal message", "你好，今天天气不错", false},
		{"timestamp prefixed observation", "12:25 心跳检查——大小姐上班中...", true},
		{"timestamp with note", "09:30 状态观察：一切正常", true},
		{"heartbeat keyword", "进行心跳检查", true},
		{"heartbeat english", "heartbeat check on timeline", true},
		{"status monitoring", "状态监控：timeline 没有新消息", true},
		{"internal marker CN", "[内部] 这是一个内部观察", true},
		{"internal marker EN", "[internal] just an observation", true},
		{"monologue marker", "[monologue] thinking about stuff", true},
		{"normal discussion", "我觉得这个技术方案挺好的", false},
		{"question", "大家觉得这个怎么样？", false},
		{"reply with numbers", "2024年的数据显示增长了20%", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := looksLikeInternalNote(tt.text)
			if got != tt.want {
				t.Errorf("looksLikeInternalNote(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestExecSend_DiscussMode_BlocksInternalNote(t *testing.T) {
	t.Parallel()

	sender := &fakeSender{}
	p := newTestMessageProvider(sender)

	session := SessionContext{
		BotID:           "bot-1",
		SessionID:       "sess-1",
		SessionType:     "discuss",
		CurrentPlatform: "misskey",
		ReplyTarget:     "note-123",
		IsMentioned:     false,
		SendCount:       newAtomicInt32(0),
		RepliedTargets:  NewRepliedTargetTracker(),
	}

	args := map[string]any{
		"text": "12:25 心跳检查——大小姐上班中...",
	}

	result, err := p.execSend(context.Background(), session, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if m["ok"] != false {
		t.Fatalf("expected ok=false, got %v", m["ok"])
	}
	errMsg, _ := m["error"].(string)
	if errMsg == "" {
		t.Fatal("expected non-empty error message")
	}
	if sender.called != 0 {
		t.Fatalf("expected sender not called, but was called %d times", sender.called)
	}
}

func TestExecSend_DiscussMode_AllowsNormalMessage(t *testing.T) {
	t.Parallel()

	sender := &fakeSender{}
	p := newTestMessageProvider(sender)

	session := SessionContext{
		BotID:           "bot-1",
		SessionID:       "sess-1",
		SessionType:     "discuss",
		CurrentPlatform: "misskey",
		ReplyTarget:     "note-123",
		IsMentioned:     true,
		SendCount:       newAtomicInt32(0),
		RepliedTargets:  NewRepliedTargetTracker(),
	}

	args := map[string]any{
		"text": "我觉得这个方案不错！",
	}

	result, err := p.execSend(context.Background(), session, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if m["ok"] != true {
		t.Fatalf("expected ok=true, got %v (error: %v)", m["ok"], m["error"])
	}
	if sender.called != 1 {
		t.Fatalf("expected sender called once, got %d", sender.called)
	}
}
