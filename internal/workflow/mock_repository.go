package workflow

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

// MockRepository implements Repository for testing.
type MockRepository struct {
	Pipelines map[uuid.UUID]Pipeline
	Nodes     map[uuid.UUID]Node
}

// NewMockRepository creates a ready-to-use mock repository.
func NewMockRepository() *MockRepository {
	return &MockRepository{
		Pipelines: make(map[uuid.UUID]Pipeline),
		Nodes:     make(map[uuid.UUID]Node),
	}
}

func (m *MockRepository) CreatePipeline(_ context.Context, botID uuid.UUID, goal string, status Status) (Pipeline, error) {
	p := Pipeline{
		ID:     uuid.New(),
		BotID:  botID,
		Goal:   goal,
		Status: status,
	}
	now := p.CreatedAt
	p.UpdatedAt = now
	// Copy time zero value (actual time set by DB)
	m.Pipelines[p.ID] = p
	return p, nil
}

var errNotFound = errors.New("not found")

func (m *MockRepository) GetPipeline(_ context.Context, id uuid.UUID) (Pipeline, error) {
	p, ok := m.Pipelines[id]
	if !ok {
		return Pipeline{}, errNotFound
	}
	return p, nil
}

func (m *MockRepository) ListPipelinesByBot(_ context.Context, botID uuid.UUID, _, _ int32) ([]Pipeline, error) {
	var result []Pipeline
	for _, p := range m.Pipelines {
		if p.BotID == botID {
			result = append(result, p)
		}
	}
	return result, nil
}

func (m *MockRepository) UpdatePipelineStatus(_ context.Context, id uuid.UUID, status Status) (Pipeline, error) {
	p := m.Pipelines[id]
	p.Status = status
	m.Pipelines[id] = p
	return p, nil
}

func (m *MockRepository) DeletePipeline(_ context.Context, id uuid.UUID) error {
	delete(m.Pipelines, id)
	return nil
}

func (m *MockRepository) CreateNode(_ context.Context, pipelineID uuid.UUID, input CreateNodeInput) (Node, error) {
	n := Node{
		ID:             uuid.New(),
		PipelineID:     pipelineID,
		Name:           input.Name,
		Description:    input.Description,
		DependsOn:      input.DependsOn,
		ModelTier:      input.ModelTier,
		Status:         StatusPending,
		Input:          input.Input,
		MaxRetries:     input.MaxRetries,
		TimeoutSeconds: input.TimeoutSeconds,
		NeedsReview:    input.NeedsReview,
	}
	m.Nodes[n.ID] = n
	return n, nil
}

func (m *MockRepository) GetNode(_ context.Context, id uuid.UUID) (Node, error) {
	n, ok := m.Nodes[id]
	if !ok {
		return Node{}, errNotFound
	}
	return n, nil
}

func (m *MockRepository) ListNodesByPipeline(_ context.Context, pipelineID uuid.UUID) ([]Node, error) {
	var result []Node
	for _, n := range m.Nodes {
		if n.PipelineID == pipelineID {
			result = append(result, n)
		}
	}
	return result, nil
}

func (m *MockRepository) UpdateNodeStatus(_ context.Context, id uuid.UUID, status Status) (Node, error) {
	n := m.Nodes[id]
	n.Status = status
	m.Nodes[id] = n
	return n, nil
}

func (m *MockRepository) UpdateNodeOutput(_ context.Context, id uuid.UUID, output json.RawMessage, status Status) (Node, error) {
	n := m.Nodes[id]
	n.Output = output
	n.Status = status
	m.Nodes[id] = n
	return n, nil
}

func (m *MockRepository) UpdateNodeError(_ context.Context, id uuid.UUID, errMsg string) (Node, error) {
	n := m.Nodes[id]
	n.Error = &errMsg
	n.Status = StatusFailed
	m.Nodes[id] = n
	return n, nil
}

func (m *MockRepository) IncrementNodeRetry(_ context.Context, id uuid.UUID) (Node, error) {
	n := m.Nodes[id]
	n.RetryCount++
	n.Status = StatusPending
	m.Nodes[id] = n
	return n, nil
}

func (m *MockRepository) UpdateNodeReview(_ context.Context, id uuid.UUID, result, feedback string) (Node, error) {
	n := m.Nodes[id]
	n.ReviewResult = &result
	n.ReviewFeedback = &feedback
	m.Nodes[id] = n
	return n, nil
}

func (m *MockRepository) DeleteNodesByPipeline(_ context.Context, pipelineID uuid.UUID) error {
	for id, n := range m.Nodes {
		if n.PipelineID == pipelineID {
			delete(m.Nodes, id)
		}
	}
	return nil
}
