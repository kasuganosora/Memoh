package channel

import "testing"

func TestFilterThinkingTags(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no thinking tags",
			input: "Hello, world!",
			want:  "Hello, world!",
		},
		{
			name:  "thinking block removed",
			input: `<thinking>Let me think about this</thinking>Hello!`,
			want:  "Hello!",
		},
		{
			name:  "think block removed",
			input: `<think` + `>Some reasoning</think` + `>The answer is 42`,
			want:  "The answer is 42",
		},
		{
			name:  "multiline thinking block",
			input: "<thinking>\nLine 1\nLine 2\n</thinking>\nActual response",
			want:  "Actual response",
		},
		{
			name:  "only thinking block",
			input: `<thinking>Just thinking</thinking>`,
			want:  "",
		},
		{
			name:  "multiple thinking blocks",
			input: `<think` + `>First</think` + `>Middle<thinking>Second</thinking>End`,
			want:  "MiddleEnd",
		},
		{
			name:  "thinking with whitespace",
			input: "  <thinking>  thoughts  </thinking>  response  ",
			want:  "response",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterThinkingTags(tt.input)
			if got != tt.want {
				t.Errorf("FilterThinkingTags(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
