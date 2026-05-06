package workflow

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatusConstants(t *testing.T) {
	assert.Equal(t, Status("pending"), StatusPending)
	assert.Equal(t, Status("running"), StatusRunning)
	assert.Equal(t, Status("completed"), StatusCompleted)
	assert.Equal(t, Status("failed"), StatusFailed)
	assert.Equal(t, Status("cancelled"), StatusCancelled)
}

func TestModelTierConstants(t *testing.T) {
	assert.Equal(t, ModelTier("standard"), ModelTierStandard)
	assert.Equal(t, ModelTier("compact"), ModelTierCompact)
}

func TestCreatePipelineInput_Validation(t *testing.T) {
	botID := uuid.New()
	input := CreatePipelineInput{
		BotID: botID,
		Goal:  "Research and summarize LLM trends for 2025",
		Nodes: []CreateNodeInput{
			{
				Name:           "search",
				ModelTier:      ModelTierCompact,
				MaxRetries:     3,
				TimeoutSeconds: 300,
			},
			{
				Name:           "summarize",
				ModelTier:      ModelTierStandard,
				MaxRetries:     2,
				TimeoutSeconds: 600,
			},
		},
	}

	assert.Equal(t, botID, input.BotID)
	assert.Len(t, input.Nodes, 2)
	assert.Equal(t, "search", input.Nodes[0].Name)
	assert.Equal(t, ModelTierCompact, input.Nodes[0].ModelTier)
}

func TestPlannerOutput_JSONStructure(t *testing.T) {
	output := PlannerOutput{
		Nodes: []PlannerNode{
			{
				Name:           "research",
				Description:    "research the topic online",
				DependsOn:      []int{},
				ModelTier:      ModelTierCompact,
				MaxRetries:     3,
				TimeoutSeconds: 300,
				NeedsReview:    false,
			},
			{
				Name:           "write_report",
				Description:    "write a report based on research",
				DependsOn:      []int{0},
				ModelTier:      ModelTierStandard,
				MaxRetries:     2,
				TimeoutSeconds: 600,
				NeedsReview:    true,
			},
		},
	}

	require.Len(t, output.Nodes, 2)
	assert.Empty(t, output.Nodes[0].DependsOn)
	assert.Equal(t, []int{0}, output.Nodes[1].DependsOn)
	assert.True(t, output.Nodes[1].NeedsReview)
}
