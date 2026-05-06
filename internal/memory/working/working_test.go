package working

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_DefaultCapacity(t *testing.T) {
	wm := New()
	assert.NotNil(t, wm)
	assert.Equal(t, defaultCapacity, wm.capacity)
}

func TestNewWithCapacity(t *testing.T) {
	wm := NewWithCapacity(500)
	assert.NotNil(t, wm)
	assert.Equal(t, 500, wm.capacity)
}

func TestAddAndSearch(t *testing.T) {
	wm := NewWithCapacity(100)
	botID := "bot-1"

	wm.Add(botID, "User prefers dark mode", "medium", nil)
	wm.Add(botID, "User lives in Berlin", "high", nil)
	wm.Add(botID, "User favorite food is pizza", "low", nil)

	results := wm.Search(botID, "dark", 10)
	require.Len(t, results, 1)
	assert.Equal(t, "User prefers dark mode", results[0].Content)
	assert.Equal(t, "medium", results[0].Importance)

	results = wm.Search(botID, "Berlin", 10)
	require.Len(t, results, 1)
	assert.Equal(t, "User lives in Berlin", results[0].Content)
	assert.Equal(t, "high", results[0].Importance)

	results = wm.Search(botID, "User", 10)
	require.Len(t, results, 3)
}

func TestSearch_CaseInsensitive(t *testing.T) {
	wm := NewWithCapacity(100)
	wm.Add("bot-1", "Hello WORLD", "medium", nil)

	results := wm.Search("bot-1", "world", 10)
	require.Len(t, results, 1)
	assert.Equal(t, "Hello WORLD", results[0].Content)
}

func TestSearch_EmptyQueryReturnsAll(t *testing.T) {
	wm := NewWithCapacity(100)
	wm.Add("bot-1", "fact1", "medium", nil)
	wm.Add("bot-1", "fact2", "medium", nil)
	wm.Add("bot-1", "fact3", "medium", nil)

	results := wm.Search("bot-1", "", 10)
	require.Len(t, results, 3)
}

func TestSearch_Limit(t *testing.T) {
	wm := NewWithCapacity(100)
	wm.Add("bot-1", "a", "medium", nil)
	wm.Add("bot-1", "b", "medium", nil)
	wm.Add("bot-1", "c", "medium", nil)

	results := wm.Search("bot-1", "", 2)
	require.Len(t, results, 2)
}

func TestAdd_DedupByContent(t *testing.T) {
	wm := NewWithCapacity(100)
	wm.Add("bot-1", "User likes tea", "medium", nil)
	wm.Add("bot-1", "User likes tea", "high", nil)

	assert.Equal(t, 1, wm.Len("bot-1"))

	// The importance should be updated to "high"
	// AccessCount = 1 (first Add) + 1 (second Add updates) + 1 (Search) = 3
	results := wm.Search("bot-1", "tea", 10)
	require.Len(t, results, 1)
	assert.Equal(t, "high", results[0].Importance)
	assert.Equal(t, 3, results[0].AccessCount)
}

func TestAdd_EmptyContent(t *testing.T) {
	wm := NewWithCapacity(100)
	wm.Add("bot-1", "", "medium", nil)
	assert.Equal(t, 0, wm.Len("bot-1"))
}

func TestPerBotIsolation(t *testing.T) {
	wm := NewWithCapacity(100)
	wm.Add("bot-1", "fact about bot1", "medium", nil)
	wm.Add("bot-2", "fact about bot2", "medium", nil)

	assert.Equal(t, 1, wm.Len("bot-1"))
	assert.Equal(t, 1, wm.Len("bot-2"))

	results := wm.Search("bot-1", "bot2", 10)
	assert.Empty(t, results)

	results = wm.Search("bot-2", "bot1", 10)
	assert.Empty(t, results)
}

func TestGetHighAccessEntries(t *testing.T) {
	wm := NewWithCapacity(100)
	wm.Add("bot-1", "important fact", "high", nil)

	// Access the entry multiple times
	for i := 0; i < 5; i++ {
		wm.Search("bot-1", "important", 10)
	}

	entries := wm.GetHighAccessEntries("bot-1", 3)
	require.Len(t, entries, 1)
	assert.GreaterOrEqual(t, entries[0].AccessCount, 3)
}

func TestGetHighAccessEntries_ExcludesLowImportance(t *testing.T) {
	wm := NewWithCapacity(100)
	wm.Add("bot-1", "trivial fact", "low", nil)

	// Access it many times
	for i := 0; i < 10; i++ {
		wm.Search("bot-1", "trivial", 10)
	}

	entries := wm.GetHighAccessEntries("bot-1", 3)
	// Low importance + high access should NOT be promoted
	assert.Empty(t, entries)
}

func TestRemove(t *testing.T) {
	wm := NewWithCapacity(100)
	wm.Add("bot-1", "to be removed", "medium", nil)
	assert.Equal(t, 1, wm.Len("bot-1"))

	wm.Remove("bot-1", "to be removed")
	assert.Equal(t, 0, wm.Len("bot-1"))
}

func TestPurge(t *testing.T) {
	wm := NewWithCapacity(100)
	wm.Add("bot-1", "fact1", "medium", nil)
	wm.Add("bot-1", "fact2", "medium", nil)
	assert.Equal(t, 2, wm.Len("bot-1"))

	wm.Purge("bot-1")
	assert.Equal(t, 0, wm.Len("bot-1"))
}

func TestLRUEviction(t *testing.T) {
	wm := NewWithCapacity(3)
	wm.Add("bot-1", "a", "medium", nil)
	wm.Add("bot-1", "b", "medium", nil)
	wm.Add("bot-1", "c", "medium", nil)
	wm.Add("bot-1", "d", "medium", nil) // should evict "a"

	assert.Equal(t, 3, wm.Len("bot-1"))
	results := wm.Search("bot-1", "a", 10)
	assert.Empty(t, results)
}

func TestConcurrentAccess(t *testing.T) {
	wm := NewWithCapacity(1000)
	done := make(chan bool)

	for i := 0; i < 10; i++ {
		go func(id int) {
			botID := "bot-concurrent"
			wm.Add(botID, string(rune('A'+id)), "medium", nil)
			wm.Search(botID, "", 10)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	assert.GreaterOrEqual(t, wm.Len("bot-concurrent"), 1)
}
