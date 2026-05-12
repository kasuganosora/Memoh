package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/db/sqlc"
	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/settings"
	"github.com/memohai/memoh/internal/workflow"
)

// PipelineAgent is the interface the pipeline tool uses to run pipeline node tasks.
// It is satisfied by *agent.Agent and avoids import cycles.
type PipelineAgent interface {
	Generate(ctx context.Context, cfg SpawnRunConfig) (*SpawnResult, error)
}

// PipelineSettingsProvider extracts the bot settings.
type PipelineSettingsProvider interface {
	GetBot(ctx context.Context, botID string) (settings.Settings, error)
}

// PipelineProvider exposes a "schedule_pipeline" tool that orchestrates
// long-running DAG-based task pipelines.
type PipelineProvider struct {
	agent          PipelineAgent
	settings       PipelineSettingsProvider
	models         *models.Service
	queries        *sqlc.Queries
	modelCreator   ModelCreator
	scheduler      *workflow.Scheduler
	systemPromptFn func(sessionType string) string
	logger         *slog.Logger
	// Hooks for testing
	plannerFn func(ctx context.Context, goal string) (*workflow.PlannerOutput, error)
}

// NewPipelineProvider creates a PipelineProvider.
func NewPipelineProvider(
	log *slog.Logger,
	settingsSvc PipelineSettingsProvider,
	modelsSvc *models.Service,
	queries *sqlc.Queries,
) *PipelineProvider {
	if log == nil {
		log = slog.Default()
	}
	return &PipelineProvider{
		settings: settingsSvc,
		models:   modelsSvc,
		queries:  queries,
		logger:   log.With(slog.String("tool", "pipeline")),
	}
}

// SetAgent injects the agent after construction.
func (p *PipelineProvider) SetAgent(a PipelineAgent) {
	p.agent = a
}

// SetModelCreator injects the model creator function.
func (p *PipelineProvider) SetModelCreator(fn ModelCreator) {
	p.modelCreator = fn
}

// SetSystemPromptFunc injects the prompt function.
func (p *PipelineProvider) SetSystemPromptFunc(fn func(sessionType string) string) {
	p.systemPromptFn = fn
}

// SetScheduler injects the scheduler.
func (p *PipelineProvider) SetScheduler(s *workflow.Scheduler) {
	p.scheduler = s
}

// SetPlannerFn injects a custom planner for testing.
func (p *PipelineProvider) SetPlannerFn(fn func(ctx context.Context, goal string) (*workflow.PlannerOutput, error)) {
	p.plannerFn = fn
}

// Tools returns the tools exposed by this provider.
// schedule_pipeline is not available to subagents to prevent recursive pipeline creation.
func (p *PipelineProvider) Tools(_ context.Context, session SessionContext) ([]sdk.Tool, error) {
	if session.IsSubagent {
		return nil, nil
	}
	return []sdk.Tool{
		{
			Name:        "schedule_pipeline",
			Description: "Schedule and execute a multi-step DAG pipeline for complex long-running tasks. Use this when a task requires multiple steps that can be planned and executed independently (e.g., research, analysis, coding, testing). The pipeline supports parallel execution of independent steps and automatic retry on failure.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"goal": map[string]any{
						"type":        "string",
						"description": "A clear goal description for the pipeline. Be specific about what needs to be accomplished.",
					},
					"nodes": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"name": map[string]any{
									"type":        "string",
									"description": "Name of the pipeline step",
								},
								"description": map[string]any{
									"type":        "string",
									"description": "What this step should do",
								},
								"depends_on": map[string]any{
									"type": "array",
									"items": map[string]any{
										"type": "integer",
									},
									"description": "Indices of nodes this depends on (0-based). Nodes without dependencies run in parallel.",
								},
								"model_tier": map[string]any{
									"type":        "string",
									"enum":        []string{"compact", "standard"},
									"description": "Model tier: compact for simple tasks, standard for complex reasoning",
								},
							},
							"required": []string{"name"},
						},
						"description": "Optional explicit node definitions. If omitted, the planner will generate them.",
					},
				},
				"required": []string{"goal"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				args, ok := input.(map[string]any)
				if !ok {
					return nil, errors.New("invalid input type")
				}
				return p.execute(ctx.Context, args, session)
			},
		},
	}, nil
}

func (p *PipelineProvider) execute(ctx context.Context, args map[string]any, session SessionContext) (string, error) {
	goal := StringArg(args, "goal")
	if strings.TrimSpace(goal) == "" {
		return "", errors.New("goal is required")
	}

	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return "", errors.New("bot ID not found in session")
	}

	botUUID, err := uuid.Parse(botID)
	if err != nil {
		return "", fmt.Errorf("invalid bot ID: %w", err)
	}

	if p.scheduler == nil {
		return "", errors.New("pipeline scheduler not configured")
	}

	// Plan the pipeline — use user-provided nodes if present, otherwise call LLM planner.
	var plannerOutput *workflow.PlannerOutput

	if userNodes, ok := args["nodes"]; ok && userNodes != nil {
		plannerOutput, err = p.parseUserNodes(userNodes)
	} else if p.plannerFn != nil {
		plannerOutput, err = p.plannerFn(ctx, goal)
	} else {
		plannerOutput, err = p.planWithLLM(ctx, goal)
	}
	if err != nil {
		p.logger.Error("pipeline planning failed", slog.String("goal", goal), slog.Any("error", err))
		return "", fmt.Errorf("pipeline planning failed: %w", err)
	}

	// Create pipeline in DB
	pwn, err := p.scheduler.PlanAndCreatePipeline(ctx, botUUID, goal, *plannerOutput)
	if err != nil {
		p.logger.Error("pipeline creation failed", slog.Any("error", err))
		return "", fmt.Errorf("pipeline creation failed: %w", err)
	}

	p.logger.Info("pipeline created",
		slog.String("pipeline_id", pwn.Pipeline.ID.String()),
		slog.Int("nodes", len(pwn.Nodes)),
	)

	// Execute pipeline (synchronous)
	if err := p.scheduler.Resume(ctx, pwn); err != nil {
		p.logger.Error("pipeline execution failed", slog.Any("error", err))
		return "", fmt.Errorf("pipeline execution failed: %w", err)
	}

	// Build result summary
	var summary strings.Builder
	fmt.Fprintf(&summary, "Pipeline %s: %s\n", pwn.Pipeline.Status, goal)
	for _, node := range pwn.Nodes {
		var statusIcon string
		switch node.Status {
		case workflow.StatusFailed:
			statusIcon = "❌"
		case workflow.StatusRunning:
			statusIcon = "🔄"
		default:
			statusIcon = "✅"
		}
		fmt.Fprintf(&summary, "  %s %s: %s", statusIcon, node.Name, node.Status)
		if node.Error != nil {
			fmt.Fprintf(&summary, " (error: %s)", *node.Error)
		}
		summary.WriteString("\n")
		if node.Output != nil {
			fmt.Fprintf(&summary, "    Output: %s\n", string(node.Output))
		}
	}

	return summary.String(), nil
}

// parseUserNodes converts user-provided node definitions into a PlannerOutput.
func (*PipelineProvider) parseUserNodes(raw any) (*workflow.PlannerOutput, error) {
	nodeSlice, ok := raw.([]any)
	if !ok {
		return nil, errors.New("nodes must be an array")
	}
	if len(nodeSlice) == 0 {
		return nil, errors.New("nodes array is empty")
	}

	var nodes []workflow.PlannerNode
	for _, item := range nodeSlice {
		nodeMap, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("each node must be an object")
		}
		pn := workflow.PlannerNode{
			Name:        StringArg(nodeMap, "name"),
			Description: StringArg(nodeMap, "description"),
			ModelTier:   workflow.ModelTier(StringArg(nodeMap, "model_tier")),
		}
		if pn.Name == "" {
			return nil, errors.New("node name is required")
		}
		// Parse depends_on (array of integers)
		if deps, ok := nodeMap["depends_on"]; ok && deps != nil {
			if depSlice, ok := deps.([]any); ok {
				for _, d := range depSlice {
					if num, ok := d.(float64); ok {
						pn.DependsOn = append(pn.DependsOn, int(num))
					}
				}
			}
		}
		nodes = append(nodes, pn)
	}

	return &workflow.PlannerOutput{Nodes: nodes}, nil
}

func (p *PipelineProvider) planWithLLM(ctx context.Context, goal string) (*workflow.PlannerOutput, error) {
	if p.agent == nil {
		return nil, errors.New("agent not configured for pipeline planning")
	}

	planningPrompt := fmt.Sprintf(`You are a pipeline planner. Given a goal, create a DAG (Directed Acyclic Graph) of steps to accomplish it.

Rules:
1. Each step has a unique name, optional description, and a model_tier (compact or standard).
2. Use "depends_on" (array of 0-based step indices) to express dependencies.
3. Steps with no dependency or whose dependencies are all met can run in PARALLEL.
4. Use "compact" for simple tasks (search, read, fetch) and "standard" for complex reasoning.
5. Default max_retries=3, timeout_seconds=300 unless stated otherwise.
6. Set "needs_review": true for steps that produce critical output needing verification.

Goal: %s

Output ONLY a JSON object with a "nodes" array. Each node: {"name": "...", "description": "...", "depends_on": [...], "model_tier": "compact|standard", "max_retries": 3, "timeout_seconds": 300, "needs_review": false}

Example for "Research and summarize AI trends":
{
  "nodes": [
    {"name": "search_trends", "description": "Search the web for current AI trends", "depends_on": [], "model_tier": "compact", "max_retries": 2, "timeout_seconds": 120, "needs_review": false},
    {"name": "search_papers", "description": "Search for recent AI research papers", "depends_on": [], "model_tier": "compact", "max_retries": 2, "timeout_seconds": 120, "needs_review": false},
    {"name": "analyze_findings", "description": "Analyze and synthesize findings from searches", "depends_on": [0, 1], "model_tier": "standard", "max_retries": 2, "timeout_seconds": 300, "needs_review": false},
    {"name": "write_summary", "description": "Write a comprehensive summary report", "depends_on": [2], "model_tier": "standard", "max_retries": 2, "timeout_seconds": 300, "needs_review": true}
  ]
}`, goal)

	cfg := SpawnRunConfig{
		System: "You are a pipeline planner that outputs JSON DAG definitions.",
		Query:  planningPrompt,
	}

	result, err := p.agent.Generate(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("planner LLM call failed: %w", err)
	}

	// Parse the JSON from the result text — handle various markdown wrapping.
	text := strings.TrimSpace(result.Text)
	text = extractJSON(text)

	var output workflow.PlannerOutput
	if err := json.Unmarshal([]byte(text), &output); err != nil {
		p.logger.Error("failed to parse planner output", slog.String("text", text), slog.Any("error", err))
		return nil, fmt.Errorf("failed to parse planner JSON output: %w", err)
	}

	if len(output.Nodes) == 0 {
		return nil, errors.New("planner produced empty node list")
	}

	return &output, nil
}

// --- NodeExecutor implementation ---

// Execute runs a single pipeline node using the LLM.
// Implements the workflow.NodeExecutor interface for the scheduler.
func (p *PipelineProvider) Execute(ctx context.Context, node workflow.Node) (json.RawMessage, error) {
	if p.agent == nil {
		return nil, errors.New("agent not configured")
	}

	// Build a context-rich prompt that includes upstream node outputs.
	var promptBuilder strings.Builder
	fmt.Fprintf(&promptBuilder, "Execute the following pipeline step:\n\n")
	fmt.Fprintf(&promptBuilder, "Step Name: %s\n", node.Name)
	fmt.Fprintf(&promptBuilder, "Description: %s\n", strPtrVal(node.Description))

	// Include upstream input context if available.
	if len(node.Input) > 0 && string(node.Input) != "null" {
		fmt.Fprintf(&promptBuilder, "\n## Input from upstream steps\n```json\n%s\n```\n", string(node.Input))
	}

	fmt.Fprintf(&promptBuilder, "\nProduce your output as a JSON object with a \"result\" field and any additional relevant fields.")
	fmt.Fprintf(&promptBuilder, "\nKeep your response concise and focused on completing this specific step.")

	attemptStart := time.Now()
	p.logger.Info("executing pipeline node via LLM",
		slog.String("node_name", node.Name),
		slog.String("model_tier", string(node.ModelTier)),
		slog.Bool("has_input", len(node.Input) > 0 && string(node.Input) != "null"),
	)

	cfg := SpawnRunConfig{
		System:      "You are a pipeline worker executing a single step in a multi-step workflow. You have access to tools. Output a JSON result object.",
		Query:       promptBuilder.String(),
		SessionType: "pipeline",
	}

	result, err := p.agent.Generate(ctx, cfg)
	if err != nil {
		p.logger.Error("node execution failed",
			slog.String("node_name", node.Name),
			slog.Duration("duration", time.Since(attemptStart)),
			slog.Any("error", err),
		)
		return nil, fmt.Errorf("node %s execution failed: %w", node.Name, err)
	}

	text := strings.TrimSpace(result.Text)
	text = extractJSON(text)

	if !json.Valid([]byte(text)) {
		wrapped := map[string]string{"result": text}
		raw, _ := json.Marshal(wrapped)
		return json.RawMessage(raw), nil
	}

	p.logger.Info("node execution completed",
		slog.String("node_name", node.Name),
		slog.Duration("duration", time.Since(attemptStart)),
	)

	return json.RawMessage(text), nil
}

var _ workflow.NodeExecutor = (*PipelineProvider)(nil)

// extractJSON strips markdown code fences from LLM output to extract raw JSON.
func extractJSON(text string) string {
	text = strings.TrimSpace(text)

	// Handle ```json ... ``` wrapping (possibly with language tag variations).
	if idx := strings.Index(text, "```"); idx >= 0 {
		// Find the opening fence end (after the language tag line).
		start := strings.Index(text[idx:], "\n")
		if start >= 0 {
			inner := text[idx+start+1:]
			// Find the closing fence.
			if end := strings.LastIndex(inner, "```"); end >= 0 {
				return strings.TrimSpace(inner[:end])
			}
		}
	}

	// Fallback: simple prefix/suffix strip.
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	return strings.TrimSpace(text)
}

func strPtrVal(s *string) string {
	if s == nil {
		return "n/a"
	}
	return *s
}
