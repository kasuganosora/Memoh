// Package working provides a per-bot short-term working memory cache
// backed by an LRU eviction policy. It sits between the instant
// conversation context and the long-term Qdrant memory store.
package working

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// defaultCapacity is the default number of entries retained per bot.
const defaultCapacity = 1000

// MemoryEntry is a single cached memory fact.
// Mutations to AccessCount and LastAccess are protected by mu for concurrent safety.
type MemoryEntry struct {
	mu          sync.Mutex
	Content     string         `json:"content"`
	Importance  string         `json:"importance"` // high / medium / low
	AccessCount int            `json:"access_count"`
	CreatedAt   time.Time      `json:"created_at"`
	LastAccess  time.Time      `json:"last_access_at"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// touch increments AccessCount and updates LastAccess atomically.
func (e *MemoryEntry) touch() {
	e.mu.Lock()
	e.AccessCount++
	e.LastAccess = time.Now()
	e.mu.Unlock()
}

// touchUpdate also updates importance. Meant for Add dedup path.
func (e *MemoryEntry) touchUpdate(importance string, metadata map[string]any) {
	e.mu.Lock()
	e.AccessCount++
	e.LastAccess = time.Now()
	if importance != "" {
		e.Importance = importance
	}
	if metadata != nil {
		e.Metadata = metadata
	}
	e.mu.Unlock()
}

// readSnapshot returns a consistent snapshot of mutable fields.
func (e *MemoryEntry) readSnapshot() (int, time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.AccessCount, e.LastAccess
}

// WorkingMemory manages per-bot LRU caches for short-term memory.
// It is safe for concurrent use.
type WorkingMemory struct {
	mu       sync.RWMutex
	caches   map[string]*lru.Cache[string, *MemoryEntry]
	capacity int
}

// New creates a WorkingMemory with the default per-bot capacity (1000).
func New() *WorkingMemory {
	return NewWithCapacity(defaultCapacity)
}

// NewWithCapacity creates a WorkingMemory with a custom per-bot capacity.
func NewWithCapacity(capacity int) *WorkingMemory {
	return &WorkingMemory{
		caches:   make(map[string]*lru.Cache[string, *MemoryEntry]),
		capacity: capacity,
	}
}

// getOrCreateCache returns the LRU cache for a bot, creating it lazily.
func (w *WorkingMemory) getOrCreateCache(botID string) *lru.Cache[string, *MemoryEntry] {
	w.mu.RLock()
	c, ok := w.caches[botID]
	w.mu.RUnlock()
	if ok {
		return c
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	// Double-check after acquiring write lock
	if c, ok = w.caches[botID]; ok {
		return c
	}
	c, _ = lru.New[string, *MemoryEntry](w.capacity)
	w.caches[botID] = c
	return c
}

// Add inserts or updates a memory entry for a bot. The key is derived
// from the content hash to avoid duplicates within the LRU limits.
func (w *WorkingMemory) Add(botID, content string, importance string, metadata map[string]any) {
	if strings.TrimSpace(content) == "" {
		return
	}
	cache := w.getOrCreateCache(botID)
	now := time.Now()

	key := hashContent(content)
	if existing, found := cache.Get(key); found {
		existing.touchUpdate(importance, metadata)
		return
	}

	entry := &MemoryEntry{
		Content:    content,
		Importance: importance,
		CreatedAt:  now,
		Metadata:   metadata,
	}
	entry.AccessCount = 1
	entry.LastAccess = now
	cache.Add(key, entry)
}

// Search retrieves up to `limit` entries from the bot's working memory
// whose content contains the query (case-insensitive substring match).
// Results are sorted by access count descending.
func (w *WorkingMemory) Search(botID, query string, limit int) []*MemoryEntry {
	cache := w.getOrCreateCache(botID)
	if cache.Len() == 0 {
		return nil
	}

	keys := cache.Keys()
	var hits []*MemoryEntry
	queryLower := strings.ToLower(strings.TrimSpace(query))

	for _, key := range keys {
		entry, ok := cache.Get(key)
		if !ok {
			continue
		}
		if queryLower == "" || strings.Contains(strings.ToLower(entry.Content), queryLower) {
			entry.touch()
			hits = append(hits, entry)
		}
	}

	// Sort by access count descending, then by recency
	sortByAccess(hits)

	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// GetHighAccessEntries returns entries with access_count >= threshold and
// importance != "low". Used by the LRU→Qdrant promotion logic.
func (w *WorkingMemory) GetHighAccessEntries(botID string, minAccess int) []*MemoryEntry {
	cache := w.getOrCreateCache(botID)
	if cache.Len() == 0 {
		return nil
	}

	keys := cache.Keys()
	var result []*MemoryEntry
	for _, key := range keys {
		entry, ok := cache.Get(key)
		if !ok {
			continue
		}
		if entry.AccessCount >= minAccess && entry.Importance != "low" {
			result = append(result, entry)
		}
	}
	sortByAccess(result)
	return result
}

// Remove removes a specific entry from the bot's working memory.
func (w *WorkingMemory) Remove(botID, content string) {
	cache := w.getOrCreateCache(botID)
	key := hashContent(content)
	cache.Remove(key)
}

// Len returns the number of entries in a bot's working memory.
func (w *WorkingMemory) Len(botID string) int {
	cache := w.getOrCreateCache(botID)
	return cache.Len()
}

// Purge clears all entries for a bot.
func (w *WorkingMemory) Purge(botID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.caches, botID)
}

// hashContent creates a collision-resistant key from content for dedup
// within the LRU. Uses SHA-256 to avoid false collisions from prefix overlap.
func hashContent(content string) string {
	normalized := strings.TrimSpace(content)
	h := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(h[:16]) // first 16 bytes → 32 hex chars
}

// sortByAccess sorts entries by access count descending, ties broken by recency.
// Uses readSnapshot for thread-safe access to mutable fields.
func sortByAccess(entries []*MemoryEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0; j-- {
			acJ, laJ := entries[j].readSnapshot()
			acJ1, laJ1 := entries[j-1].readSnapshot()
			if acJ > acJ1 || (acJ == acJ1 && laJ.After(laJ1)) {
				entries[j], entries[j-1] = entries[j-1], entries[j]
			} else {
				break
			}
		}
	}
}
