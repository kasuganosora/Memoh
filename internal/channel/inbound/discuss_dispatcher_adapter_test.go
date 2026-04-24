package inbound

import (
	"log/slog"
	"testing"

	"github.com/memohai/memoh/internal/conversation"
	pipelinepkg "github.com/memohai/memoh/internal/pipeline"
)

func TestDiscussDispatcherAdapter_MarkActiveAndDone(t *testing.T) {
	d := NewRouteDispatcher(slog.Default())
	adapter := NewDiscussDispatcherAdapter(d)

	routeID := "discuss-route-1"

	if adapter.IsActive(routeID) {
		t.Fatal("expected route to be inactive initially")
	}

	injectCh := adapter.MarkActive(routeID)
	if injectCh == nil {
		t.Fatal("expected non-nil inject channel")
	}
	if !adapter.IsActive(routeID) {
		t.Fatal("expected route to be active after MarkActive")
	}

	result := adapter.MarkDone(routeID)
	if adapter.IsActive(routeID) {
		t.Fatal("expected route to be inactive after MarkDone")
	}
	if len(result.QueuedNotifications) != 0 {
		t.Fatalf("expected no queued notifications, got %d", len(result.QueuedNotifications))
	}
}

func TestDiscussDispatcherAdapter_InjectWhenActive(t *testing.T) {
	d := NewRouteDispatcher(slog.Default())
	adapter := NewDiscussDispatcherAdapter(d)

	routeID := "discuss-route-2"
	injectCh := adapter.MarkActive(routeID)

	msg := conversation.InjectMessage{Text: "injected text", HeaderifiedText: "[User] injected text"}
	if !adapter.Inject(routeID, msg) {
		t.Fatal("expected inject to succeed when route is active")
	}

	select {
	case got := <-injectCh:
		if got.Text != "injected text" {
			t.Errorf("got text %q, want %q", got.Text, "injected text")
		}
	default:
		t.Fatal("expected message on inject channel")
	}
}

func TestDiscussDispatcherAdapter_InjectWhenInactive(t *testing.T) {
	d := NewRouteDispatcher(slog.Default())
	adapter := NewDiscussDispatcherAdapter(d)

	msg := conversation.InjectMessage{Text: "hello"}
	if adapter.Inject("nonexistent-route", msg) {
		t.Fatal("expected inject to fail when route is inactive")
	}
}

func TestDiscussDispatcherAdapter_EnqueueAndDrainNotifications(t *testing.T) {
	d := NewRouteDispatcher(slog.Default())
	adapter := NewDiscussDispatcherAdapter(d)

	routeID := "discuss-route-3"
	adapter.MarkActive(routeID)

	// Enqueue two notifications.
	adapter.EnqueueNotification(routeID, pipelinepkg.DiscussQueuedNotification{
		SessionID: "sess-1",
		Config:    pipelinepkg.DiscussSessionConfig{BotID: "bot-1"},
	})
	adapter.EnqueueNotification(routeID, pipelinepkg.DiscussQueuedNotification{
		SessionID: "sess-1",
		Config:    pipelinepkg.DiscussSessionConfig{BotID: "bot-1"},
	})

	result := adapter.MarkDone(routeID)
	if len(result.QueuedNotifications) != 2 {
		t.Fatalf("expected 2 queued notifications, got %d", len(result.QueuedNotifications))
	}

	// Second MarkDone should return empty.
	adapter.MarkActive(routeID)
	result2 := adapter.MarkDone(routeID)
	if len(result2.QueuedNotifications) != 0 {
		t.Fatalf("expected 0 queued notifications after second drain, got %d", len(result2.QueuedNotifications))
	}
}
