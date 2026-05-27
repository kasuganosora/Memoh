package scene

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// mockBackend implements SceneBackend for testing.
type mockBackend struct {
	mu     sync.Mutex
	scenes map[string]map[string]any
}

func newMockBackend() *mockBackend {
	return &mockBackend{scenes: make(map[string]map[string]any)}
}

func (m *mockBackend) UpsertScene(_ context.Context, id string, payload map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scenes[id] = payload
	return nil
}

func (m *mockBackend) GetScene(_ context.Context, id string) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.scenes[id]
	if !ok {
		return nil, nil
	}
	return p, nil
}

func (m *mockBackend) ListScenes(_ context.Context, botID string) ([]map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []map[string]any
	for _, p := range m.scenes {
		if bid, _ := p["bot_id"].(string); bid == botID {
			result = append(result, p)
		}
	}
	return result, nil
}

func (m *mockBackend) DeleteScene(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.scenes, id)
	return nil
}

func TestVectorStore_CRUD(t *testing.T) {
	backend := newMockBackend()
	store := NewVectorStore(backend, nil)
	ctx := context.Background()

	// Create
	scene := Scene{
		BotID:     "bot1",
		Title:     "Test Scene",
		Summary:   "A test scene for unit testing",
		HeatScore: 5.0,
		TimeRange: TimeRange{
			Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC),
		},
		MemoryIDs: []string{"mem1", "mem2", "mem3"},
	}

	created, err := store.Create(ctx, scene)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if created.Title != "Test Scene" {
		t.Fatalf("expected title 'Test Scene', got '%s'", created.Title)
	}

	// Get
	got, err := store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.Title != "Test Scene" {
		t.Fatalf("expected title 'Test Scene', got '%s'", got.Title)
	}
	if len(got.MemoryIDs) != 3 {
		t.Fatalf("expected 3 memory IDs, got %d", len(got.MemoryIDs))
	}

	// Update
	got.Title = "Updated Scene"
	got.HeatScore = 10.0
	if err := store.Update(ctx, *got); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	updated, _ := store.Get(ctx, created.ID)
	if updated.Title != "Updated Scene" {
		t.Fatalf("expected updated title, got '%s'", updated.Title)
	}
	if updated.HeatScore != 10.0 {
		t.Fatalf("expected heat_score=10, got %f", updated.HeatScore)
	}

	// List
	scenes, err := store.List(ctx, "bot1")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(scenes) != 1 {
		t.Fatalf("expected 1 scene, got %d", len(scenes))
	}

	// Delete
	if err := store.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	scenes, _ = store.List(ctx, "bot1")
	if len(scenes) != 0 {
		t.Fatalf("expected 0 scenes after delete, got %d", len(scenes))
	}
}

func TestVectorStore_MergeLowHeat(t *testing.T) {
	backend := newMockBackend()
	store := NewVectorStore(backend, nil)
	ctx := context.Background()

	// Create MaxScenesPerBot + 1 scenes with varying heat scores.
	for i := 0; i <= MaxScenesPerBot; i++ {
		_, err := store.Create(ctx, Scene{
			BotID:     "bot1",
			Title:     fmt.Sprintf("Scene %d", i),
			Summary:   fmt.Sprintf("Summary for scene %d", i),
			HeatScore: float64(i),
			MemoryIDs: []string{fmt.Sprintf("mem_%d", i)},
		})
		if err != nil {
			t.Fatalf("Create scene %d failed: %v", i, err)
		}
	}

	// Verify we have MaxScenesPerBot + 1 scenes.
	scenes, _ := store.List(ctx, "bot1")
	if len(scenes) != MaxScenesPerBot+1 {
		t.Fatalf("expected %d scenes, got %d", MaxScenesPerBot+1, len(scenes))
	}

	// Merge should combine the two lowest-heat scenes.
	merged, err := store.MergeLowHeat(ctx, "bot1")
	if err != nil {
		t.Fatalf("MergeLowHeat failed: %v", err)
	}
	if merged == nil {
		t.Fatal("expected merged scene, got nil")
		return
	}

	// After merge, we should have MaxScenesPerBot scenes.
	scenes, _ = store.List(ctx, "bot1")
	if len(scenes) != MaxScenesPerBot {
		t.Fatalf("expected %d scenes after merge, got %d", MaxScenesPerBot, len(scenes))
	}

	// The merged scene should contain memory IDs from both source scenes.
	if len(merged.MemoryIDs) != 2 {
		t.Fatalf("expected 2 memory IDs in merged scene, got %d", len(merged.MemoryIDs))
	}
}

func TestVectorStore_MergeLowHeat_NoMergeNeeded(t *testing.T) {
	backend := newMockBackend()
	store := NewVectorStore(backend, nil)
	ctx := context.Background()

	// Create fewer than MaxScenesPerBot scenes.
	for i := 0; i < 5; i++ {
		_, _ = store.Create(ctx, Scene{
			BotID:     "bot1",
			Title:     fmt.Sprintf("Scene %d", i),
			HeatScore: float64(i),
		})
	}

	// MergeLowHeat should return nil (no merge needed).
	merged, err := store.MergeLowHeat(ctx, "bot1")
	if err != nil {
		t.Fatalf("MergeLowHeat failed: %v", err)
	}
	if merged != nil {
		t.Fatal("expected nil (no merge needed), got a scene")
	}
}

func TestMergeTimeRanges(t *testing.T) {
	a := TimeRange{
		Start: time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2024, 1, 20, 0, 0, 0, 0, time.UTC),
	}
	b := TimeRange{
		Start: time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2024, 1, 25, 0, 0, 0, 0, time.UTC),
	}

	result := mergeTimeRanges(a, b)
	if !result.Start.Equal(b.Start) {
		t.Fatalf("expected start=%v, got %v", b.Start, result.Start)
	}
	if !result.End.Equal(b.End) {
		t.Fatalf("expected end=%v, got %v", b.End, result.End)
	}
}

func TestMergeStringSlices(t *testing.T) {
	a := []string{"a", "b", "c"}
	b := []string{"b", "c", "d", "e"}

	result := mergeStringSlices(a, b)
	if len(result) != 5 {
		t.Fatalf("expected 5 unique items, got %d: %v", len(result), result)
	}
}
