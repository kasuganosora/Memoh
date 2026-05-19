package storefs

import (
	"sync"
	"time"
)

const defaultCacheTTL = 30 * time.Second

// cacheEntry holds cached memory items for a single bot.
type cacheEntry struct {
	items     []MemoryItem
	fetchedAt time.Time
}

// memoryCache is a simple in-memory TTL cache keyed by botID.
type memoryCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
	ttl     time.Duration
}

func newMemoryCache(ttl time.Duration) *memoryCache {
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	return &memoryCache{
		entries: make(map[string]*cacheEntry),
		ttl:     ttl,
	}
}

// get returns cached items for the given botID if the entry exists and is not expired.
// Returns nil, false if the cache misses.
func (c *memoryCache) get(botID string) ([]MemoryItem, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[botID]
	if !ok {
		return nil, false
	}
	if time.Since(entry.fetchedAt) > c.ttl {
		return nil, false
	}
	// Return a deep copy to prevent mutation of cached data (Metadata is a map reference)
	copied := make([]MemoryItem, len(entry.items))
	for i, item := range entry.items {
		copied[i] = deepCopyItem(item)
	}
	return copied, true
}

// set stores items in the cache for the given botID.
func (c *memoryCache) set(botID string, items []MemoryItem) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Store a deep copy to prevent external mutation
	copied := make([]MemoryItem, len(items))
	for i, item := range items {
		copied[i] = deepCopyItem(item)
	}
	c.entries[botID] = &cacheEntry{
		items:     copied,
		fetchedAt: time.Now(),
	}
}

// deepCopyItem returns a deep copy of a MemoryItem, cloning the Metadata map.
func deepCopyItem(item MemoryItem) MemoryItem {
	if item.Metadata != nil {
		cloned := make(map[string]any, len(item.Metadata))
		for k, v := range item.Metadata {
			cloned[k] = v
		}
		item.Metadata = cloned
	}
	return item
}

// invalidate removes the cache entry for the given botID.
func (c *memoryCache) invalidate(botID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, botID)
}

// invalidateAll clears the entire cache.
func (c *memoryCache) invalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*cacheEntry)
}
