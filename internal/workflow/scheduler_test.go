package workflow

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTopologicalSort_LinearDAG(t *testing.T) {
	nodeA := Node{ID: uuid.New(), Name: "A", DependsOn: nil}
	nodeB := Node{ID: uuid.New(), Name: "B", DependsOn: []uuid.UUID{nodeA.ID}}
	nodeC := Node{ID: uuid.New(), Name: "C", DependsOn: []uuid.UUID{nodeB.ID}}

	levels, err := topologicalSort([]Node{nodeA, nodeB, nodeC})

	require.NoError(t, err)
	require.Len(t, levels, 3)
	assert.Equal(t, nodeA.ID, levels[0][0])
	assert.Equal(t, nodeB.ID, levels[1][0])
	assert.Equal(t, nodeC.ID, levels[2][0])
}

func TestTopologicalSort_ParallelDAG(t *testing.T) {
	nodeA := Node{ID: uuid.New(), Name: "A", DependsOn: nil}
	nodeB := Node{ID: uuid.New(), Name: "B", DependsOn: []uuid.UUID{nodeA.ID}}
	nodeC := Node{ID: uuid.New(), Name: "C", DependsOn: []uuid.UUID{nodeA.ID}}
	nodeD := Node{ID: uuid.New(), Name: "D", DependsOn: []uuid.UUID{nodeB.ID, nodeC.ID}}

	levels, err := topologicalSort([]Node{nodeA, nodeB, nodeC, nodeD})

	require.NoError(t, err)
	require.Len(t, levels, 3)
	assert.Len(t, levels[0], 1)
	assert.Equal(t, nodeA.ID, levels[0][0])
	assert.Len(t, levels[1], 2)
	assert.Contains(t, levels[1], nodeB.ID)
	assert.Contains(t, levels[1], nodeC.ID)
	assert.Len(t, levels[2], 1)
	assert.Equal(t, nodeD.ID, levels[2][0])
}

func TestTopologicalSort_EmptyNodes(t *testing.T) {
	levels, err := topologicalSort([]Node{})
	require.NoError(t, err)
	assert.Nil(t, levels)
}

func TestTopologicalSort_SingleNode(t *testing.T) {
	node := Node{ID: uuid.New(), Name: "only", DependsOn: nil}
	levels, err := topologicalSort([]Node{node})
	require.NoError(t, err)
	require.Len(t, levels, 1)
	assert.Len(t, levels[0], 1)
	assert.Equal(t, node.ID, levels[0][0])
}

func TestTopologicalSort_DiamondDAG(t *testing.T) {
	nodeA := Node{ID: uuid.New(), Name: "A", DependsOn: nil}
	nodeB := Node{ID: uuid.New(), Name: "B", DependsOn: []uuid.UUID{nodeA.ID}}
	nodeC := Node{ID: uuid.New(), Name: "C", DependsOn: []uuid.UUID{nodeA.ID}}
	nodeD := Node{ID: uuid.New(), Name: "D", DependsOn: []uuid.UUID{nodeB.ID, nodeC.ID}}

	levels, err := topologicalSort([]Node{nodeA, nodeB, nodeC, nodeD})

	require.NoError(t, err)
	require.Len(t, levels, 3)
	assert.Len(t, levels[1], 2)
}

func TestTopologicalSort_CycleDetection(t *testing.T) {
	// A -> B -> C -> A (cycle)
	nodeA := Node{ID: uuid.New(), Name: "A", DependsOn: nil}
	nodeB := Node{ID: uuid.New(), Name: "B", DependsOn: []uuid.UUID{nodeA.ID}}
	nodeC := Node{ID: uuid.New(), Name: "C", DependsOn: []uuid.UUID{nodeB.ID}}
	nodeA.DependsOn = []uuid.UUID{nodeC.ID} // creates cycle

	levels, err := topologicalSort([]Node{nodeA, nodeB, nodeC})
	require.Error(t, err)
	assert.Nil(t, levels)
	assert.Contains(t, err.Error(), "cycle")
}

// newTestScheduler creates a scheduler with a mock repo and executor.
func newTestScheduler(repo Repository, executor NodeExecutor) *Scheduler {
	s := NewScheduler(repo, slog.Default())
	s.SetExecutor(executor)
	return s
}

func TestScheduler_FullPipelineExecution(t *testing.T) {
	repo := NewMockRepository()
	executor := NewMockNodeExecutor()
	sched := newTestScheduler(repo, executor)

	ctx := context.Background()
	botID := uuid.New()

	output := PlannerOutput{
		Nodes: []PlannerNode{
			{Name: "search", Description: "search the web", ModelTier: ModelTierCompact, MaxRetries: 2, TimeoutSeconds: 60},
			{Name: "analyze", Description: "analyze results", DependsOn: []int{0}, ModelTier: ModelTierStandard, MaxRetries: 2, TimeoutSeconds: 120},
			{Name: "report", Description: "write report", DependsOn: []int{1}, ModelTier: ModelTierStandard, MaxRetries: 2, TimeoutSeconds: 180},
		},
	}

	pn, err := sched.PlanAndCreatePipeline(ctx, botID, "research topic", output)
	require.NoError(t, err)
	require.Len(t, pn.Nodes, 3)

	err = sched.Resume(ctx, pn)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, pn.Pipeline.Status)

	assert.Equal(t, 1, executor.GetCallCount("search"))
	assert.Equal(t, 1, executor.GetCallCount("analyze"))
	assert.Equal(t, 1, executor.GetCallCount("report"))
}

func TestScheduler_ParallelExecution(t *testing.T) {
	repo := NewMockRepository()
	executor := NewMockNodeExecutor()
	sched := newTestScheduler(repo, executor)

	ctx := context.Background()
	botID := uuid.New()

	output := PlannerOutput{
		Nodes: []PlannerNode{
			{Name: "source1", Description: "fetch from source 1", ModelTier: ModelTierCompact, MaxRetries: 2, TimeoutSeconds: 60},
			{Name: "source2", Description: "fetch from source 2", ModelTier: ModelTierCompact, MaxRetries: 2, TimeoutSeconds: 60},
		},
	}

	pn, err := sched.PlanAndCreatePipeline(ctx, botID, "parallel fetch", output)
	require.NoError(t, err)
	require.Len(t, pn.Nodes, 2)

	err = sched.Resume(ctx, pn)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, pn.Pipeline.Status)

	assert.Equal(t, 1, executor.GetCallCount("source1"))
	assert.Equal(t, 1, executor.GetCallCount("source2"))
}

func TestScheduler_NodeFailureRetry(t *testing.T) {
	repo := NewMockRepository()
	executor := NewMockNodeExecutor()
	executor.SetError("flaky", assert.AnError)

	sched := newTestScheduler(repo, executor)

	ctx := context.Background()
	botID := uuid.New()

	output := PlannerOutput{
		Nodes: []PlannerNode{
			{Name: "flaky", Description: "flaky node", ModelTier: ModelTierCompact, MaxRetries: 2, TimeoutSeconds: 30},
		},
	}

	pn, err := sched.PlanAndCreatePipeline(ctx, botID, "flaky test", output)
	require.NoError(t, err)

	err = sched.Resume(ctx, pn)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, pn.Pipeline.Status)

	assert.Equal(t, 2, executor.GetCallCount("flaky"))
}

func TestScheduler_NodeFailureExhaustsRetries(t *testing.T) {
	repo := NewMockRepository()
	executor := newErrNodeExecutor("permanent failure")
	sched := newTestScheduler(repo, executor)

	ctx := context.Background()
	botID := uuid.New()

	output := PlannerOutput{
		Nodes: []PlannerNode{
			{Name: "doomed", Description: "always fails", ModelTier: ModelTierCompact, MaxRetries: 1, TimeoutSeconds: 30},
		},
	}

	pn, err := sched.PlanAndCreatePipeline(ctx, botID, "failure test", output)
	require.NoError(t, err)

	err = sched.Resume(ctx, pn)
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, pn.Pipeline.Status)
}

func TestScheduler_GetPipelineStatus(t *testing.T) {
	repo := NewMockRepository()
	executor := NewMockNodeExecutor()
	sched := newTestScheduler(repo, executor)

	ctx := context.Background()
	botID := uuid.New()

	output := PlannerOutput{
		Nodes: []PlannerNode{
			{Name: "step1", ModelTier: ModelTierCompact, MaxRetries: 1, TimeoutSeconds: 30},
			{Name: "step2", DependsOn: []int{0}, ModelTier: ModelTierStandard, MaxRetries: 1, TimeoutSeconds: 30},
		},
	}

	pn, err := sched.PlanAndCreatePipeline(ctx, botID, "status test", output)
	require.NoError(t, err)

	status, err := sched.GetPipelineStatus(ctx, pn.Pipeline.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusPending, status.Pipeline.Status)
	assert.Len(t, status.Nodes, 2)
	require.NotNil(t, status.Graph)
	assert.Len(t, status.Graph.Levels, 2)
}

func TestScheduler_PlanAndCreate_EmptyNodes_Rejected(t *testing.T) {
	repo := NewMockRepository()
	executor := NewMockNodeExecutor()
	sched := newTestScheduler(repo, executor)

	ctx := context.Background()
	botID := uuid.New()

	output := PlannerOutput{
		Nodes: []PlannerNode{},
	}

	_, err := sched.PlanAndCreatePipeline(ctx, botID, "empty test", output)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty node list")
}

func TestMockRepository_CRUD(t *testing.T) {
	repo := NewMockRepository()
	ctx := context.Background()
	botID := uuid.New()

	p, err := repo.CreatePipeline(ctx, botID, "test goal", StatusPending)
	require.NoError(t, err)
	assert.Equal(t, botID, p.BotID)
	assert.Equal(t, "test goal", p.Goal)
	assert.Equal(t, StatusPending, p.Status)

	got, err := repo.GetPipeline(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)

	input := CreateNodeInput{
		Name:           "test-node",
		ModelTier:      ModelTierCompact,
		MaxRetries:     3,
		TimeoutSeconds: 60,
	}
	n, err := repo.CreateNode(ctx, p.ID, input)
	require.NoError(t, err)
	assert.Equal(t, p.ID, n.PipelineID)
	assert.Equal(t, "test-node", n.Name)

	nodes, err := repo.ListNodesByPipeline(ctx, p.ID)
	require.NoError(t, err)
	assert.Len(t, nodes, 1)
}

func TestMockNodeExecutor_ErrorThenSuccess(t *testing.T) {
	executor := NewMockNodeExecutor()
	executor.SetError("test-node", assert.AnError)

	node := Node{ID: uuid.New(), Name: "test-node", PipelineID: uuid.New()}

	_, err := executor.Execute(context.Background(), node)
	require.Error(t, err)

	output, err := executor.Execute(context.Background(), node)
	require.NoError(t, err)
	assert.NotNil(t, output)
	assert.Equal(t, 2, executor.GetCallCount("test-node"))
}
