package handlers

import (
	"sync"
	"time"

	"github.com/memohai/memoh/internal/conversation"
)

// activeStreamCache stores intermediate UI messages for streams that are
// still running. This allows the ListMessages endpoint to return in-progress
// content when a user reconnects after closing their browser.
var activeStreamCache struct {
	mu   sync.RWMutex
	data map[string]streamEntry // key: "botID:sessionID"
}

type streamEntry struct {
	messages []conversation.UIMessage
	setAt    time.Time
}

const streamCacheTTL = 30 * time.Minute

func init() {
	activeStreamCache.data = make(map[string]streamEntry)
	go streamCacheEvictor()
}

// streamCacheEvictor periodically removes expired entries.
func streamCacheEvictor() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		activeStreamCache.mu.Lock()
		for k, v := range activeStreamCache.data {
			if now.Sub(v.setAt) > streamCacheTTL {
				delete(activeStreamCache.data, k)
			}
		}
		activeStreamCache.mu.Unlock()
	}
}

func streamCacheKey(botID, sessionID string) string {
	return botID + ":" + sessionID
}

// SetActiveStream stores the current snapshot of UI messages for an active stream.
func SetActiveStream(botID, sessionID string, messages []conversation.UIMessage) {
	key := streamCacheKey(botID, sessionID)
	activeStreamCache.mu.Lock()
	activeStreamCache.data[key] = streamEntry{messages: messages, setAt: time.Now()}
	activeStreamCache.mu.Unlock()
}

// GetActiveStream retrieves cached UI messages for an active stream.
// Returns nil if no active stream exists or the entry has expired.
func GetActiveStream(botID, sessionID string) []conversation.UIMessage {
	key := streamCacheKey(botID, sessionID)
	activeStreamCache.mu.RLock()
	defer activeStreamCache.mu.RUnlock()
	entry, ok := activeStreamCache.data[key]
	if !ok || time.Since(entry.setAt) > streamCacheTTL {
		return nil
	}
	return entry.messages
}

// ClearActiveStream removes the cached stream data (called when stream completes).
func ClearActiveStream(botID, sessionID string) {
	key := streamCacheKey(botID, sessionID)
	activeStreamCache.mu.Lock()
	delete(activeStreamCache.data, key)
	activeStreamCache.mu.Unlock()
}
