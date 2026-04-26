package profiles

import (
	"sync"
	"time"
)

// defaultCacheTTL is the default time-to-live for cached profiles.
const defaultCacheTTL = 10 * time.Minute

// memCache is a simple in-memory cache with TTL for profiles.
type memCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	ttl     time.Duration
}

type cacheEntry struct {
	profile  *Profile
	expireAt time.Time
}

// NewMemCache creates a new in-memory profile cache with default TTL.
func NewMemCache() ProfileCache {
	return &memCache{
		entries: make(map[string]cacheEntry),
		ttl:     defaultCacheTTL,
	}
}

// NewMemCacheWithTTL creates a new in-memory profile cache with a custom TTL.
func NewMemCacheWithTTL(ttl time.Duration) ProfileCache {
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	return &memCache{
		entries: make(map[string]cacheEntry),
		ttl:     ttl,
	}
}

func (*memCache) key(botID, userID string) string {
	return botID + ":" + userID
}

func (c *memCache) Get(botID, userID string) (*Profile, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[c.key(botID, userID)]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expireAt) {
		delete(c.entries, c.key(botID, userID))
		return nil, false
	}
	return entry.profile, true
}

func (c *memCache) Set(botID, userID string, profile *Profile) {
	c.mu.Lock()
	c.entries[c.key(botID, userID)] = cacheEntry{
		profile:  profile,
		expireAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()
}
