package pipeline

import (
	"testing"
	"time"

	"github.com/memohai/memoh/internal/channel"
)

func TestAdaptInbound_EditEvent(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 5, 20, 10, 30, 0, 0, time.FixedZone("CST", 8*3600))
	msg := channel.InboundMessage{
		Channel: "telegram",
		Message: channel.Message{
			ID:   "msg-123",
			Text: "edited content",
		},
		Sender: channel.Identity{
			SubjectID:   "user-1",
			DisplayName: "Alice",
			Attributes:  map[string]string{"username": "alice"},
		},
		Conversation: channel.Conversation{
			ID:   "conv-1",
			Type: "group",
			Name: "Test Group",
		},
		ReceivedAt: now,
		Metadata: map[string]any{
			"event_type": "edit",
			"is_bot":     false,
		},
	}

	event := AdaptInbound(msg, "session-1", "cid-1", "Alice")

	editEvent, ok := event.(EditEvent)
	if !ok {
		t.Fatalf("expected EditEvent, got %T", event)
	}

	if editEvent.SessionID != "session-1" {
		t.Errorf("SessionID = %q, want %q", editEvent.SessionID, "session-1")
	}
	if editEvent.MessageID != "msg-123" {
		t.Errorf("MessageID = %q, want %q", editEvent.MessageID, "msg-123")
	}
	if editEvent.Sender == nil {
		t.Fatal("Sender is nil")
	}
	if editEvent.Sender.ID != "cid-1" {
		t.Errorf("Sender.ID = %q, want %q", editEvent.Sender.ID, "cid-1")
	}
	if editEvent.Sender.DisplayName != "Alice" {
		t.Errorf("Sender.DisplayName = %q, want %q", editEvent.Sender.DisplayName, "Alice")
	}
	if editEvent.Sender.Username != "alice" {
		t.Errorf("Sender.Username = %q, want %q", editEvent.Sender.Username, "alice")
	}
	if editEvent.ReceivedAtMs != now.UnixMilli() {
		t.Errorf("ReceivedAtMs = %d, want %d", editEvent.ReceivedAtMs, now.UnixMilli())
	}
	if editEvent.UTCOffsetMin != 480 {
		t.Errorf("UTCOffsetMin = %d, want 480", editEvent.UTCOffsetMin)
	}
	if len(editEvent.Content) != 1 || editEvent.Content[0].Text != "edited content" {
		t.Errorf("Content = %+v, want [{Type:text Text:edited content}]", editEvent.Content)
	}
	if editEvent.Kind() != EventEdit {
		t.Errorf("Kind() = %q, want %q", editEvent.Kind(), EventEdit)
	}
}

func TestAdaptInbound_ServiceEvent(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 5, 20, 10, 30, 0, 0, time.FixedZone("CST", 8*3600))
	msg := channel.InboundMessage{
		Channel: "telegram",
		Message: channel.Message{},
		Sender: channel.Identity{
			SubjectID:   "user-admin",
			DisplayName: "Admin",
			Attributes:  map[string]string{"username": "admin"},
		},
		Conversation: channel.Conversation{
			ID:   "conv-1",
			Type: "group",
			Name: "Test Group",
		},
		ReceivedAt: now,
		Metadata: map[string]any{
			"event_type":     "service",
			"service_action": "chat_renamed",
			"new_title":      "New Group Name",
			"old_title":      "Old Group Name",
		},
	}

	event := AdaptInbound(msg, "session-1", "cid-admin", "Admin")

	svcEvent, ok := event.(ServiceEvent)
	if !ok {
		t.Fatalf("expected ServiceEvent, got %T", event)
	}

	if svcEvent.SessionID != "session-1" {
		t.Errorf("SessionID = %q, want %q", svcEvent.SessionID, "session-1")
	}
	if svcEvent.Action != ServiceChatRenamed {
		t.Errorf("Action = %q, want %q", svcEvent.Action, ServiceChatRenamed)
	}
	if svcEvent.Actor == nil {
		t.Fatal("Actor is nil")
	}
	if svcEvent.Actor.ID != "user-admin" {
		t.Errorf("Actor.ID = %q, want %q", svcEvent.Actor.ID, "user-admin")
	}
	if svcEvent.Actor.DisplayName != "Admin" {
		t.Errorf("Actor.DisplayName = %q, want %q", svcEvent.Actor.DisplayName, "Admin")
	}
	if svcEvent.NewTitle != "New Group Name" {
		t.Errorf("NewTitle = %q, want %q", svcEvent.NewTitle, "New Group Name")
	}
	if svcEvent.OldTitle != "Old Group Name" {
		t.Errorf("OldTitle = %q, want %q", svcEvent.OldTitle, "Old Group Name")
	}
	if svcEvent.ReceivedAtMs != now.UnixMilli() {
		t.Errorf("ReceivedAtMs = %d, want %d", svcEvent.ReceivedAtMs, now.UnixMilli())
	}
	if svcEvent.UTCOffsetMin != 480 {
		t.Errorf("UTCOffsetMin = %d, want 480", svcEvent.UTCOffsetMin)
	}
	if svcEvent.Kind() != EventService {
		t.Errorf("Kind() = %q, want %q", svcEvent.Kind(), EventService)
	}
}

func TestAdaptInbound_ServiceEvent_NoSender(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 5, 20, 10, 30, 0, 0, time.UTC)
	msg := channel.InboundMessage{
		Channel:    "telegram",
		Message:    channel.Message{},
		Sender:     channel.Identity{}, // empty sender
		ReceivedAt: now,
		Metadata: map[string]any{
			"event_type":     "service",
			"service_action": "member_left",
		},
	}

	event := AdaptInbound(msg, "session-1", "", "")

	svcEvent, ok := event.(ServiceEvent)
	if !ok {
		t.Fatalf("expected ServiceEvent, got %T", event)
	}

	if svcEvent.Actor != nil {
		t.Errorf("expected nil Actor for empty sender, got %+v", svcEvent.Actor)
	}
	if svcEvent.Action != ServiceMemberLeft {
		t.Errorf("Action = %q, want %q", svcEvent.Action, ServiceMemberLeft)
	}
}

func TestAdaptInbound_DefaultMessage(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 5, 20, 10, 30, 0, 0, time.UTC)
	msg := channel.InboundMessage{
		Channel: "telegram",
		Message: channel.Message{
			ID:   "msg-456",
			Text: "hello",
		},
		Sender: channel.Identity{
			SubjectID:   "user-2",
			DisplayName: "Bob",
		},
		Conversation: channel.Conversation{
			ID:   "conv-2",
			Type: "private",
		},
		ReceivedAt: now,
		Metadata:   map[string]any{},
	}

	event := AdaptInbound(msg, "session-2", "cid-2", "Bob")

	msgEvent, ok := event.(MessageEvent)
	if !ok {
		t.Fatalf("expected MessageEvent, got %T", event)
	}

	if msgEvent.SessionID != "session-2" {
		t.Errorf("SessionID = %q, want %q", msgEvent.SessionID, "session-2")
	}
	if msgEvent.MessageID != "msg-456" {
		t.Errorf("MessageID = %q, want %q", msgEvent.MessageID, "msg-456")
	}
	if msgEvent.Conversation.ConversationType != "private" {
		t.Errorf("ConversationType = %q, want %q", msgEvent.Conversation.ConversationType, "private")
	}
	if msgEvent.Kind() != EventMessage {
		t.Errorf("Kind() = %q, want %q", msgEvent.Kind(), EventMessage)
	}
}
