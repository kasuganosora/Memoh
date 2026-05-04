package inbound

import (
	"strings"
	"sync"

	"github.com/memohai/memoh/internal/conversation"
	pipelinepkg "github.com/memohai/memoh/internal/pipeline"
)

// DiscussDispatcherAdapter wraps a RouteDispatcher to implement the
// pipeline.DiscussDispatcher interface. It reuses the same per-route
// active/inject mechanism but maintains a separate queue of discuss-mode
// notifications (RenderedContext + config) instead of QueuedTask.
//
// This allows discuss mode to share the same RouteDispatcher instance as
// normal inbound processing, so inject/queue decisions are unified across
// both modes.
type DiscussDispatcherAdapter struct {
	dispatcher *RouteDispatcher

	mu    sync.Mutex
	queue map[string][]pipelinepkg.DiscussQueuedNotification // routeID → queued notifications
}

// NewDiscussDispatcherAdapter creates an adapter around the given dispatcher.
func NewDiscussDispatcherAdapter(d *RouteDispatcher) *DiscussDispatcherAdapter {
	return &DiscussDispatcherAdapter{
		dispatcher: d,
		queue:      make(map[string][]pipelinepkg.DiscussQueuedNotification),
	}
}

// IsActive delegates to the underlying RouteDispatcher.
func (a *DiscussDispatcherAdapter) IsActive(routeID string) bool {
	return a.dispatcher.IsActive(routeID)
}

// MarkActive delegates to the underlying RouteDispatcher.
func (a *DiscussDispatcherAdapter) MarkActive(routeID string) <-chan conversation.InjectMessage {
	return a.dispatcher.MarkActive(routeID)
}

// TryMarkActive delegates to the underlying RouteDispatcher.
func (a *DiscussDispatcherAdapter) TryMarkActive(routeID string) <-chan conversation.InjectMessage {
	return a.dispatcher.TryMarkActive(routeID)
}

// MarkDone marks the route as idle in the underlying RouteDispatcher and
// returns any queued discuss notifications. The underlying dispatcher's
// QueuedTasks and PendingPersists are intentionally ignored here — discuss
// mode does not use them.
func (a *DiscussDispatcherAdapter) MarkDone(routeID string) pipelinepkg.DiscussMarkDoneResult {
	// Mark done in the underlying dispatcher (drains inject channel, etc.).
	_ = a.dispatcher.MarkDone(routeID)

	// Drain our discuss-specific queue.
	a.mu.Lock()
	notifications := a.queue[routeID]
	delete(a.queue, routeID)
	a.mu.Unlock()

	return pipelinepkg.DiscussMarkDoneResult{
		QueuedNotifications: notifications,
	}
}

// Inject delegates to the underlying RouteDispatcher.
func (a *DiscussDispatcherAdapter) Inject(routeID string, msg conversation.InjectMessage) bool {
	return a.dispatcher.Inject(routeID, msg)
}

// EnqueueNotification adds a discuss-mode notification to the queue for the
// given route. This is called by the inbound processor when a new message
// arrives for a discuss route that already has an active agent stream.
func (a *DiscussDispatcherAdapter) EnqueueNotification(routeID string, notif pipelinepkg.DiscussQueuedNotification) {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return
	}
	a.mu.Lock()
	a.queue[routeID] = append(a.queue[routeID], notif)
	a.mu.Unlock()
}
