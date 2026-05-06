// Package workflow provides long-task pipeline orchestration
// with DAG-based scheduling, parallel execution, and model-tier routing.
package workflow

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Status represents the lifecycle status of a pipeline or node.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// ModelTier represents the compute tier for node execution.
type ModelTier string

const (
	ModelTierStandard ModelTier = "standard"
	ModelTierCompact  ModelTier = "compact"
)

// ReviewResult represents the outcome of an optional reviewer pass.
type ReviewResult string

const (
	ReviewPass          ReviewResult = "pass"
	ReviewFail          ReviewResult = "fail"
	ReviewNeedsRevision ReviewResult = "needs_revision"
)

// Pipeline is the top-level DAG orchestration entity.
type Pipeline struct {
	ID        uuid.UUID `json:"id"`
	BotID     uuid.UUID `json:"bot_id"`
	Goal      string    `json:"goal"`
	Status    Status    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Node is a single unit of work within a pipeline DAG.
type Node struct {
	ID             uuid.UUID       `json:"id"`
	PipelineID     uuid.UUID       `json:"pipeline_id"`
	Name           string          `json:"name"`
	Description    *string         `json:"description,omitempty"`
	DependsOn      []uuid.UUID     `json:"depends_on"`
	ModelTier      ModelTier       `json:"model_tier"`
	Status         Status          `json:"status"`
	Input          json.RawMessage `json:"input,omitempty"`
	Output         json.RawMessage `json:"output,omitempty"`
	Error          *string         `json:"error,omitempty"`
	RetryCount     int32           `json:"retry_count"`
	MaxRetries     int32           `json:"max_retries"`
	TimeoutSeconds int32           `json:"timeout_seconds"`
	NeedsReview    bool            `json:"needs_review"`
	ReviewResult   *string         `json:"review_result,omitempty"`
	ReviewFeedback *string         `json:"review_feedback,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	StartedAt      *time.Time      `json:"started_at,omitempty"`
	CompletedAt    *time.Time      `json:"completed_at,omitempty"`
}

// PipelineWithNodes is a pipeline together with its node graph.
type PipelineWithNodes struct {
	Pipeline Pipeline `json:"pipeline"`
	Nodes    []Node   `json:"nodes"`
}

// PipelineStatus is the enriched status view returned by the observability API.
type PipelineStatus struct {
	Pipeline Pipeline     `json:"pipeline"`
	Nodes    []Node       `json:"nodes"`
	Graph    *DAGTopology `json:"graph"`
}

// DAGTopology provides a topologically-sorted structure for visualization.
type DAGTopology struct {
	Levels [][]uuid.UUID `json:"levels"` // each level is a batch of parallel-executable nodes
}

// CreatePipelineInput is used when a pipeline is first created.
type CreatePipelineInput struct {
	BotID uuid.UUID          `json:"bot_id"`
	Goal  string             `json:"goal"`
	Nodes []CreateNodeInput  `json:"nodes"`
}

// CreateNodeInput is a single node definition during pipeline creation.
type CreateNodeInput struct {
	Name           string      `json:"name"`
	Description    *string     `json:"description,omitempty"`
	DependsOn      []uuid.UUID `json:"depends_on"`
	ModelTier      ModelTier   `json:"model_tier"`
	Input          json.RawMessage `json:"input,omitempty"`
	MaxRetries     int32          `json:"max_retries"`
	TimeoutSeconds int32          `json:"timeout_seconds"`
	NeedsReview    bool           `json:"needs_review"`
}

// PlannerOutput is the LLM-generated DAG structure.
type PlannerOutput struct {
	Nodes []PlannerNode `json:"nodes"`
}

// PlannerNode is a single node from the LLM planner.
type PlannerNode struct {
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	DependsOn      []int     `json:"depends_on"`       // indices into the planner output node list
	ModelTier      ModelTier `json:"model_tier"`
	MaxRetries     int32     `json:"max_retries"`
	TimeoutSeconds int32     `json:"timeout_seconds"`
	NeedsReview    bool      `json:"needs_review"`
}

// IsTerminal reports whether the status is a final state.
func (s Status) IsTerminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}

// IsRunning reports whether the node or pipeline is actively executing.
func (s Status) IsRunning() bool {
	return s == StatusRunning
}
