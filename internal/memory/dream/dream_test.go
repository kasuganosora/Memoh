package dream

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fake runtime ---

type fakeDreamRuntime struct {
	items   map[string]MemoryItem
	updates map[string]string // track what was updated
}

func newFakeDreamRuntime(items ...MemoryItem) *fakeDreamRuntime {
	rt := &fakeDreamRuntime{
		items:   make(map[string]MemoryItem),
		updates: make(map[string]string),
	}
	for _, m := range items {
		rt.items[m.ID] = m
	}
	return rt
}

func (r *fakeDreamRuntime) Search(_ context.Context, _ SearchRequest) (SearchResponse, error) {
	return SearchResponse{}, nil
}

func (r *fakeDreamRuntime) GetAll(_ context.Context, req GetAllRequest) (GetAllResponse, error) {
	// Return in deterministic order by ID so indices are stable.
	ids := make([]string, 0, len(r.items))
	for id := range r.items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var results []MemoryItem
	for _, id := range ids {
		results = append(results, r.items[id])
	}
	return GetAllResponse{Results: results}, nil
}

func (r *fakeDreamRuntime) Delete(_ context.Context, memoryID string) (DeleteResponse, error) {
	delete(r.items, memoryID)
	return DeleteResponse{OK: true}, nil
}

func (r *fakeDreamRuntime) Update(_ context.Context, memoryID, newText string) error {
	r.updates[memoryID] = newText
	if item, ok := r.items[memoryID]; ok {
		item.Memory = newText
		r.items[memoryID] = item
	}
	return nil
}

// --- fake LLM ---

type fakeDreamLLM struct {
	associations []MemoryAssociation
	shouldMerge  bool
	mergedText   string
	isHarmful    bool
}

func (f *fakeDreamLLM) ShouldMerge(_ context.Context, _, _ string) (bool, string, error) {
	return f.shouldMerge, f.mergedText, nil
}

func (f *fakeDreamLLM) IsHarmful(_ context.Context, _ string) (bool, error) {
	return f.isHarmful, nil
}

func (f *fakeDreamLLM) FindAssociations(_ context.Context, _ []string) ([]MemoryAssociation, error) {
	return f.associations, nil
}

// --- tests ---

func TestStrengthenAssociations_Success(t *testing.T) {
	rt := newFakeDreamRuntime(
		MemoryItem{ID: "1", Memory: "User prefers dark mode"},
		MemoryItem{ID: "2", Memory: "User wants high contrast UI"},
		MemoryItem{ID: "3", Memory: "User lives in Berlin"},
		MemoryItem{ID: "4", Memory: "User works remote from home"},
	)

	llm := &fakeDreamLLM{
		associations: []MemoryAssociation{
			{IndexA: 0, IndexB: 1, Label: "same_topic"},  // dark mode ↔ high contrast
			{IndexA: 2, IndexB: 3, Label: "related"},      // Berlin ↔ remote work
		},
	}

	svc := New(rt, llm, slog.Default())
	result := svc.Run(context.Background(), "bot-1")

	assert.Equal(t, 2, result.Associations)

	// Verify the memories were updated with cross-reference tags
	updated1 := rt.updates["1"]
	assert.Contains(t, updated1, "[↗ same_topic:")
	assert.Contains(t, updated1, "high contrast")

	updated3 := rt.updates["3"]
	assert.Contains(t, updated3, "[↗ related:")
}

func TestStrengthenAssociations_Idempotent(t *testing.T) {
	// Memory already has association tags — should NOT be updated again.
	rt := newFakeDreamRuntime(
		MemoryItem{ID: "1", Memory: "User prefers dark mode\n\n[↗ same_topic: User wants high contrast UI]"},
		MemoryItem{ID: "2", Memory: "User wants high contrast UI"},
	)

	llm := &fakeDreamLLM{
		associations: []MemoryAssociation{
			{IndexA: 0, IndexB: 1, Label: "same_topic"},
		},
	}

	svc := New(rt, llm, slog.Default())
	svc.Run(context.Background(), "bot-1")

	// Memory 1 should NOT have a second set of tags
	updated := rt.updates["1"]
	assert.Empty(t, updated, "should not update already-tagged memory")
}

func TestStrengthenAssociations_NoLLM(t *testing.T) {
	rt := newFakeDreamRuntime(
		MemoryItem{ID: "1", Memory: "fact a"},
		MemoryItem{ID: "2", Memory: "fact b"},
	)

	svc := New(rt, nil, slog.Default())
	result := svc.Run(context.Background(), "bot-1")

	assert.Equal(t, 0, result.Associations)
}

func TestStrengthenAssociations_TooFewMemories(t *testing.T) {
	rt := newFakeDreamRuntime(
		MemoryItem{ID: "1", Memory: "only one fact"},
	)

	llm := &fakeDreamLLM{}
	svc := New(rt, llm, slog.Default())
	result := svc.Run(context.Background(), "bot-1")

	assert.Equal(t, 0, result.Associations)
}

func TestStrengthenAssociations_CrossReferenceContent(t *testing.T) {
	rt := newFakeDreamRuntime(
		MemoryItem{ID: "1", Memory: "User likes oolong tea"},
		MemoryItem{ID: "2", Memory: "User prefers Asian beverages"},
		MemoryItem{ID: "3", Memory: "User visits tea shops on weekends"},
	)

	llm := &fakeDreamLLM{
		associations: []MemoryAssociation{
			{IndexA: 0, IndexB: 1, Label: "example_of"},
			{IndexA: 0, IndexB: 2, Label: "supports"},
		},
	}

	svc := New(rt, llm, slog.Default())
	result := svc.Run(context.Background(), "bot-1")

	assert.Equal(t, 2, result.Associations)

	// Memory 0 should have TWO cross-references
	updated0 := rt.updates["1"]
	assert.Contains(t, updated0, "[↗ example_of:")
	assert.Contains(t, updated0, "[↗ supports:")
	assert.Contains(t, updated0, "prefers Asian") // preview of memory 1
	assert.Contains(t, updated0, "visits tea")     // preview of memory 2
}

func TestStrengthenAssociations_PreviewLength(t *testing.T) {
	rt := newFakeDreamRuntime(
		MemoryItem{ID: "1", Memory: "short fact"},
		MemoryItem{ID: "2", Memory: strings.Repeat("very long memory content that exceeds forty characters ", 3)},
	)

	llm := &fakeDreamLLM{
		associations: []MemoryAssociation{
			{IndexA: 0, IndexB: 1, Label: "related"},
		},
	}

	svc := New(rt, llm, slog.Default())
	svc.Run(context.Background(), "bot-1")

	updated := rt.updates["1"]
	// Preview should be truncated to 40 chars + "…"
	assert.True(t, strings.HasSuffix(updated, "…]\n") || strings.Contains(updated, "…]"),
		"preview should be truncated with …")
}

func TestMergeResult_IncludesAssociations(t *testing.T) {
	rt := newFakeDreamRuntime(
		MemoryItem{ID: "1", Memory: "fact a"},
		MemoryItem{ID: "2", Memory: "fact b"},
	)

	llm := &fakeDreamLLM{
		associations: []MemoryAssociation{
			{IndexA: 0, IndexB: 1, Label: "same_topic"},
		},
	}

	svc := New(rt, llm, slog.Default())
	result := svc.Run(context.Background(), "bot-1")

	require.NotNil(t, result)
	assert.Greater(t, result.Associations, 0)
}

func TestNoOpDreamLLM(t *testing.T) {
	noop := NoOpDreamLLM{}

	should, text, err := noop.ShouldMerge(context.Background(), "a", "b")
	assert.NoError(t, err)
	assert.False(t, should)
	assert.Empty(t, text)

	harmful, err := noop.IsHarmful(context.Background(), "test")
	assert.NoError(t, err)
	assert.False(t, harmful)

	assocs, err := noop.FindAssociations(context.Background(), []string{"a", "b"})
	assert.Error(t, err)
	assert.Nil(t, assocs)
}
