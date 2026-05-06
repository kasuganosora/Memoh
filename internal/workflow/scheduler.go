package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// NodeExecutor abstracts the LLM call for executing a single node.
// This interface allows testing without a real LLM.
type NodeExecutor interface {
	// Execute runs the LLM for a node, returning structured output.
	Execute(ctx context.Context, node Node) (json.RawMessage, error)
}

// Scheduler orchestrates DAG pipeline execution.
// It is NOT a standalone service — it runs in-process, callback-driven.
type Scheduler struct {
	repo     Repository
	executor NodeExecutor
	logger   *slog.Logger
}

// NewScheduler creates a new pipeline scheduler.
// The executor must be set via SetExecutor before Resume is called.
func NewScheduler(repo Repository, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		repo:   repo,
		logger: logger.With(slog.String("component", "workflow.scheduler")),
	}
}

// SetExecutor injects the node executor after construction (to break circular DI).
func (s *Scheduler) SetExecutor(executor NodeExecutor) {
	s.executor = executor
}

// PlanAndCreatePipeline plans a DAG from the planner output and saves it to the DB.
// Returns the created pipeline with nodes, or an error if the node list is empty.
func (s *Scheduler) PlanAndCreatePipeline(ctx context.Context, botID uuid.UUID, goal string, plannerOutput PlannerOutput) (*PipelineWithNodes, error) {
	if len(plannerOutput.Nodes) == 0 {
		return nil, fmt.Errorf("planner produced an empty node list for goal: %s", goal)
	}

	p, err := s.repo.CreatePipeline(ctx, botID, goal, StatusPending)
	if err != nil {
		return nil, err
	}

	// Build a UUID -> index map for resolving depends_on indices
	nodeMap := make(map[int]uuid.UUID)
	nodes := make([]Node, 0, len(plannerOutput.Nodes))

	for i, pn := range plannerOutput.Nodes {
		dependsOn := make([]uuid.UUID, 0, len(pn.DependsOn))
		for _, depIdx := range pn.DependsOn {
			if depID, ok := nodeMap[depIdx]; ok {
				dependsOn = append(dependsOn, depID)
			}
		}

		maxRetries := pn.MaxRetries
		if maxRetries <= 0 {
			maxRetries = 3
		}
		timeoutSeconds := pn.TimeoutSeconds
		if timeoutSeconds <= 0 {
			timeoutSeconds = 300
		}
		modelTier := pn.ModelTier
		if modelTier == "" {
			modelTier = ModelTierStandard
		}

		node, err := s.repo.CreateNode(ctx, p.ID, CreateNodeInput{
			Name:           pn.Name,
			Description:    strPtr(pn.Description),
			DependsOn:      dependsOn,
			ModelTier:      modelTier,
			MaxRetries:     maxRetries,
			TimeoutSeconds: timeoutSeconds,
			NeedsReview:    pn.NeedsReview,
		})
		if err != nil {
			return nil, err
		}
		nodeMap[i] = node.ID
		nodes = append(nodes, node)
	}

	return &PipelineWithNodes{
		Pipeline: p,
		Nodes:    nodes,
	}, nil
}

// Resume continues or starts execution of a pipeline.
// It topologically sorts nodes and executes them in dependency-driven batches.
func (s *Scheduler) Resume(ctx context.Context, pipeline *PipelineWithNodes) error {
	if pipeline.Pipeline.Status.IsTerminal() {
		return nil
	}

	// Mark pipeline as running
	if _, err := s.repo.UpdatePipelineStatus(ctx, pipeline.Pipeline.ID, StatusRunning); err != nil {
		return err
	}
	pipeline.Pipeline.Status = StatusRunning

	// Build node lookup
	nodeByID := make(map[uuid.UUID]*Node)
	for i := range pipeline.Nodes {
		nodeByID[pipeline.Nodes[i].ID] = &pipeline.Nodes[i]
	}

	// Topological sort to find execution levels
	levels, err := topologicalSort(pipeline.Nodes)
	if err != nil {
		s.logger.Warn("pipeline has cycles, cannot execute",
			slog.String("pipeline_id", pipeline.Pipeline.ID.String()),
			slog.String("error", err.Error()),
		)
		return err
	}
	s.logger.Info("pipeline topological sort complete",
		slog.String("pipeline_id", pipeline.Pipeline.ID.String()),
		slog.Int("levels", len(levels)),
		slog.Int("total_nodes", len(pipeline.Nodes)),
	)

	// Execute level by level. Within each level, nodes run in parallel.
	for _, level := range levels {
		if err := ctx.Err(); err != nil {
			return err
		}

		levelNodes := make([]*Node, 0, len(level))
		for _, nodeID := range level {
			if n, ok := nodeByID[nodeID]; ok && !n.Status.IsTerminal() {
				levelNodes = append(levelNodes, n)
			}
		}

		if len(levelNodes) == 0 {
			continue
		}

		// Execute all nodes in this level concurrently
		if err := s.executeLevel(ctx, levelNodes); err != nil {
			return err
		}

		// Check if any node failed
		for _, n := range levelNodes {
			if n.Status == StatusFailed {
				s.logger.Warn("node failed, marking pipeline as failed",
					slog.String("pipeline_id", pipeline.Pipeline.ID.String()),
					slog.String("node_id", n.ID.String()),
					slog.String("node_name", n.Name),
				)
				if _, err := s.repo.UpdatePipelineStatus(ctx, pipeline.Pipeline.ID, StatusFailed); err != nil {
					return err
				}
				pipeline.Pipeline.Status = StatusFailed
				return nil
			}
		}
	}

	// All nodes completed successfully
	if _, err := s.repo.UpdatePipelineStatus(ctx, pipeline.Pipeline.ID, StatusCompleted); err != nil {
		return err
	}
	pipeline.Pipeline.Status = StatusCompleted
	return nil
}

// executeLevel runs all nodes in a level concurrently.
func (s *Scheduler) executeLevel(ctx context.Context, nodes []*Node) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(nodes))

	for _, n := range nodes {
		wg.Add(1)
		go func(node *Node) {
			defer wg.Done()
			if err := s.executeNode(ctx, node); err != nil {
				errCh <- err
			}
		}(n)
	}

	wg.Wait()
	close(errCh)

	// Collect errors but don't fail the entire level — individual node failures are
	// already recorded in the node status.
	for err := range errCh {
		s.logger.Warn("node execution error", slog.String("error", err.Error()))
	}
	return nil
}

// executeNode runs a single node with retry logic.
func (s *Scheduler) executeNode(ctx context.Context, node *Node) error {
	s.logger.Info("executing node",
		slog.String("node_id", node.ID.String()),
		slog.String("node_name", node.Name),
		slog.String("model_tier", string(node.ModelTier)),
	)

	if _, err := s.repo.UpdateNodeStatus(ctx, node.ID, StatusRunning); err != nil {
		return err
	}
	node.Status = StatusRunning

	maxRetries := int(node.MaxRetries)
	if maxRetries <= 0 {
		maxRetries = 3
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			s.logger.Info("retrying node",
				slog.String("node_id", node.ID.String()),
				slog.Int("attempt", attempt),
			)
			if _, err := s.repo.IncrementNodeRetry(ctx, node.ID); err != nil {
				return err
			}
			node.RetryCount = int32(attempt)
		}

		// Create a timeout context for this attempt
		timeout := node.TimeoutSeconds
		if timeout <= 0 {
			timeout = 300
		}
		execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)

		output, err := s.executor.Execute(execCtx, *node)
		cancel()

		if err == nil {
			// Success — write output
			updated, err := s.repo.UpdateNodeOutput(ctx, node.ID, output, StatusCompleted)
			if err != nil {
				return err
			}
			node.Output = updated.Output
			node.Status = updated.Status
			s.logger.Info("node completed successfully",
				slog.String("node_id", node.ID.String()),
				slog.String("node_name", node.Name),
			)
			return nil
		}

		lastErr = err
		s.logger.Warn("node execution failed",
			slog.String("node_id", node.ID.String()),
			slog.Int("attempt", attempt),
			slog.String("error", err.Error()),
		)
	}

	// All retries exhausted — mark the node as failed in memory FIRST
	// so that the pipeline detects the failure even if the DB write fails.
	errMsg := lastErr.Error()
	node.Status = StatusFailed
	node.Error = &errMsg
	if _, err := s.repo.UpdateNodeError(ctx, node.ID, errMsg); err != nil {
		s.logger.Warn("failed to persist node error to DB",
			slog.String("node_id", node.ID.String()),
			slog.Any("db_error", err),
		)
	}
	return lastErr
}

// ListPipelines returns pipelines for a bot with pagination.
func (s *Scheduler) ListPipelines(ctx context.Context, botID uuid.UUID, limit, offset int32) ([]Pipeline, error) {
	return s.repo.ListPipelinesByBot(ctx, botID, limit, offset)
}

// GetPipelineStatus returns the full status of a pipeline.
func (s *Scheduler) GetPipelineStatus(ctx context.Context, pipelineID uuid.UUID) (*PipelineStatus, error) {
	p, err := s.repo.GetPipeline(ctx, pipelineID)
	if err != nil {
		return nil, err
	}
	nodes, err := s.repo.ListNodesByPipeline(ctx, pipelineID)
	if err != nil {
		return nil, err
	}
	levels, _ := topologicalSort(nodes) // ignore cycle error for status display
	return &PipelineStatus{
		Pipeline: p,
		Nodes:    nodes,
		Graph:    &DAGTopology{Levels: levels},
	}, nil
}

// topologicalSort performs a Kahn-based topological sort and returns batches
// of node IDs where each batch can run in parallel.
// Returns an error when a cycle is detected (some nodes have unmet dependencies).
func topologicalSort(nodes []Node) ([][]uuid.UUID, error) {
	if len(nodes) == 0 {
		return nil, nil
	}

	inDegree := make(map[uuid.UUID]int)
	dependents := make(map[uuid.UUID][]uuid.UUID)

	for _, n := range nodes {
		if _, ok := inDegree[n.ID]; !ok {
			inDegree[n.ID] = 0
		}
		for _, dep := range n.DependsOn {
			inDegree[n.ID]++
			dependents[dep] = append(dependents[dep], n.ID)
		}
	}

	var levels [][]uuid.UUID
	for {
		var currentLevel []uuid.UUID
		for _, n := range nodes {
			if inDegree[n.ID] == 0 {
				currentLevel = append(currentLevel, n.ID)
			}
		}
		if len(currentLevel) == 0 {
			remaining := 0
			for _, n := range nodes {
				if inDegree[n.ID] > 0 {
					remaining++
				}
			}
			if remaining > 0 {
				return nil, fmt.Errorf("cycle detected: %d nodes with unmet dependencies", remaining)
			}
			break
		}
		levels = append(levels, currentLevel)

		for _, id := range currentLevel {
			inDegree[id] = -1
			for _, dep := range dependents[id] {
				inDegree[dep]--
			}
		}
	}

	return levels, nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
