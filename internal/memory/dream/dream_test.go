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
	items    map[string]MemoryItem
	updates  map[string]string         // track text updates
	metadata map[string]map[string]any // track metadata updates
}

func newFakeDreamRuntime(items ...MemoryItem) *fakeDreamRuntime {
	rt := &fakeDreamRuntime{
		items:    make(map[string]MemoryItem),
		updates:  make(map[string]string),
		metadata: make(map[string]map[string]any),
	}
	for _, m := range items {
		rt.items[m.ID] = m
	}
	return rt
}

func (*fakeDreamRuntime) Search(_ context.Context, _ SearchRequest) (SearchResponse, error) {
	return SearchResponse{}, nil
}

func (r *fakeDreamRuntime) GetAll(_ context.Context, _ GetAllRequest) (GetAllResponse, error) {
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

func (r *fakeDreamRuntime) UpdateMetadata(_ context.Context, memoryID string, meta map[string]any) error {
	r.metadata[memoryID] = meta
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

func (*fakeDreamLLM) AggregateScenes(_ context.Context, _ []string) ([]SceneCandidate, error) {
	return nil, nil
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
			{IndexA: 0, IndexB: 1, Label: "same_topic"}, // dark mode ↔ high contrast
			{IndexA: 2, IndexB: 3, Label: "related"},    // Berlin ↔ remote work
		},
	}

	svc := New(rt, llm, slog.Default())
	result := svc.Run(context.Background(), "bot-1", RunOptions{})

	assert.Equal(t, 2, result.Associations)

	// Verify associations are stored in metadata, not text body
	meta1 := rt.metadata["1"]
	require.NotNil(t, meta1, "memory 1 should have metadata update")
	assocs1, ok := meta1["associations"]
	require.True(t, ok, "metadata should contain 'associations' key")
	assocSlice1, ok := assocs1.([]map[string]string)
	require.True(t, ok)
	assert.Equal(t, "2", assocSlice1[0]["related_id"])
	assert.Equal(t, "same_topic", assocSlice1[0]["label"])

	meta3 := rt.metadata["3"]
	require.NotNil(t, meta3, "memory 3 should have metadata update")
	assocs3 := meta3["associations"].([]map[string]string)
	assert.Equal(t, "4", assocs3[0]["related_id"])

	// Memory text should NOT be modified
	_, textUpdated := rt.updates["1"]
	assert.False(t, textUpdated, "memory text should not be modified by associations")
}

func TestStrengthenAssociations_Idempotent(t *testing.T) {
	// Memory already has old-style association tags in text — new code should
	// NOT modify the text and should write associations to metadata instead.
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
	result := svc.Run(context.Background(), "bot-1", RunOptions{})

	// Associations should be written to metadata
	assert.Equal(t, 1, result.Associations)
	meta1 := rt.metadata["1"]
	require.NotNil(t, meta1)

	// Memory text should NOT be modified
	_, textUpdated := rt.updates["1"]
	assert.False(t, textUpdated, "memory text should not be modified")
}

func TestStrengthenAssociations_NoLLM(t *testing.T) {
	rt := newFakeDreamRuntime(
		MemoryItem{ID: "1", Memory: "fact a"},
		MemoryItem{ID: "2", Memory: "fact b"},
	)

	svc := New(rt, nil, slog.Default())
	result := svc.Run(context.Background(), "bot-1", RunOptions{})

	assert.Equal(t, 0, result.Associations)
}

func TestStrengthenAssociations_TooFewMemories(t *testing.T) {
	rt := newFakeDreamRuntime(
		MemoryItem{ID: "1", Memory: "only one fact"},
	)

	llm := &fakeDreamLLM{}
	svc := New(rt, llm, slog.Default())
	result := svc.Run(context.Background(), "bot-1", RunOptions{})

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
	result := svc.Run(context.Background(), "bot-1", RunOptions{})

	assert.Equal(t, 2, result.Associations)

	// Memory 0 should have TWO cross-references in metadata
	meta1 := rt.metadata["1"]
	require.NotNil(t, meta1, "memory 1 should have metadata update")
	assocs := meta1["associations"].([]map[string]string)
	assert.Len(t, assocs, 2)

	// Verify both associations are present
	labels := map[string]string{}
	for _, a := range assocs {
		labels[a["label"]] = a["related_id"]
	}
	assert.Equal(t, "2", labels["example_of"])
	assert.Equal(t, "3", labels["supports"])
}

func TestStrengthenAssociations_MetadataContainsRelatedIDs(t *testing.T) {
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
	result := svc.Run(context.Background(), "bot-1", RunOptions{})

	assert.Equal(t, 1, result.Associations)

	// Metadata should contain the actual related memory ID
	meta1 := rt.metadata["1"]
	require.NotNil(t, meta1)
	assocs := meta1["associations"].([]map[string]string)
	assert.Equal(t, "2", assocs[0]["related_id"])
	assert.Equal(t, "related", assocs[0]["label"])
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
	result := svc.Run(context.Background(), "bot-1", RunOptions{})

	require.NotNil(t, result)
	assert.Positive(t, result.Associations)
}

func TestNoOpDreamLLM(t *testing.T) {
	noop := NoOpDreamLLM{}

	should, text, err := noop.ShouldMerge(context.Background(), "a", "b")
	require.NoError(t, err)
	assert.False(t, should)
	assert.Empty(t, text)

	harmful, err := noop.IsHarmful(context.Background(), "test")
	require.NoError(t, err)
	assert.False(t, harmful)

	assocs, err := noop.FindAssociations(context.Background(), []string{"a", "b"})
	require.Error(t, err)
	assert.Nil(t, assocs)
}
