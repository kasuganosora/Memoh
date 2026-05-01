package compaction

import (
	"encoding/json"
	"testing"
)

func TestStripMultimodalContent(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string unchanged",
			input:    "",
			expected: "",
		},
		{
			name:     "plain text string unchanged",
			input:    `"hello world"`,
			expected: `"hello world"`,
		},
		{
			name:     "text-only parts unchanged",
			input:    `[{"type":"text","text":"hello"}]`,
			expected: `[{"type":"text","text":"hello"}]`,
		},
		{
			name:  "image part replaced with placeholder",
			input: `[{"type":"text","text":"look at this"},{"type":"image","image":"data:image/png;base64,abc123","mediaType":"image/png"}]`,
			expected: func() string {
				parts := []map[string]any{
					{"type": "text", "text": "look at this"},
					{"type": "text", "text": "[image]"},
				}
				b, _ := json.Marshal(parts)
				return string(b)
			}(),
		},
		{
			name:  "file part replaced with placeholder",
			input: `[{"type":"file","data":"base64data","mediaType":"application/pdf","filename":"doc.pdf"}]`,
			expected: func() string {
				parts := []map[string]any{
					{"type": "text", "text": "[file]"},
				}
				b, _ := json.Marshal(parts)
				return string(b)
			}(),
		},
		{
			name:     "non-JSON content returned as-is",
			input:    "just some plain text content",
			expected: "just some plain text content",
		},
		{
			name:  "mixed parts with image and file",
			input: `[{"type":"text","text":"here"},{"type":"image","image":"data:image/png;base64,abc"},{"type":"file","data":"base64","filename":"f.pdf"},{"type":"text","text":"end"}]`,
			expected: func() string {
				parts := []map[string]any{
					{"type": "text", "text": "here"},
					{"type": "text", "text": "[image]"},
					{"type": "text", "text": "[file]"},
					{"type": "text", "text": "end"},
				}
				b, _ := json.Marshal(parts)
				return string(b)
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripMultimodalContent(tt.input)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}
