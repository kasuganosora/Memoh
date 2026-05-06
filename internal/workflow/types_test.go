package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStatus_IsTerminal(t *testing.T) {
	tests := []struct {
		name   string
		status Status
		want   bool
	}{
		{"pending is not terminal", StatusPending, false},
		{"running is not terminal", StatusRunning, false},
		{"completed is terminal", StatusCompleted, true},
		{"failed is terminal", StatusFailed, true},
		{"cancelled is terminal", StatusCancelled, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.status.IsTerminal())
		})
	}
}

func TestStatus_IsRunning(t *testing.T) {
	tests := []struct {
		name   string
		status Status
		want   bool
	}{
		{"pending is not running", StatusPending, false},
		{"running is running", StatusRunning, true},
		{"completed is not running", StatusCompleted, false},
		{"failed is not running", StatusFailed, false},
		{"cancelled is not running", StatusCancelled, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.status.IsRunning())
		})
	}
}

func TestModelTier_Values(t *testing.T) {
	assert.Equal(t, ModelTierStandard, ModelTier("standard"))
	assert.Equal(t, ModelTierCompact, ModelTier("compact"))
}

func TestReviewResult_Values(t *testing.T) {
	assert.Equal(t, ReviewPass, ReviewResult("pass"))
	assert.Equal(t, ReviewFail, ReviewResult("fail"))
	assert.Equal(t, ReviewNeedsRevision, ReviewResult("needs_revision"))
}

func TestCreateNodeInput_Defaults(t *testing.T) {
	input := CreateNodeInput{
		Name:           "test-node",
		ModelTier:      ModelTierStandard,
		MaxRetries:     0,
		TimeoutSeconds: 0,
		NeedsReview:    false,
	}
	assert.Equal(t, "test-node", input.Name)
	assert.Equal(t, ModelTierStandard, input.ModelTier)
	assert.Equal(t, int32(0), input.MaxRetries)
	assert.Equal(t, int32(0), input.TimeoutSeconds)
	assert.False(t, input.NeedsReview)
}
