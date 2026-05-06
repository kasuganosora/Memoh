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
	assert.Equal(t, ModelTier("standard"), ModelTierStandard)
	assert.Equal(t, ModelTier("compact"), ModelTierCompact)
}

func TestReviewResult_Values(t *testing.T) {
	assert.Equal(t, ReviewResult("pass"), ReviewPass)
	assert.Equal(t, ReviewResult("fail"), ReviewFail)
	assert.Equal(t, ReviewResult("needs_revision"), ReviewNeedsRevision)
}

func TestCreateNodeInput_Defaults(t *testing.T) {
	input := CreateNodeInput{
		Name:      "test-node",
		ModelTier: ModelTierStandard,
	}
	assert.Equal(t, int32(0), input.MaxRetries)
	assert.Equal(t, int32(0), input.TimeoutSeconds)
	assert.False(t, input.NeedsReview)
}
