package channel

import (
	"encoding/json"
	"strings"
)

// FilterReasoningArray detects and extracts text from raw JSON reasoning arrays
// that some APIs (e.g. Zhipu/GLM) incorrectly emit inside the content field
// when context overflows.
//
// Input:  [{"text":"...","type":"reasoning"},{"text":"...","type":"text"}]
// Output: just the text-typed parts joined by newlines, or the original if not a reasoning array.
func FilterReasoningArray(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "[{") || !strings.HasSuffix(trimmed, "}]") {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parts); err != nil {
		return text
	}
	if len(parts) == 0 {
		return text
	}
	hasReasoning := false
	var texts []string
	for _, p := range parts {
		switch p.Type {
		case "text":
			texts = append(texts, p.Text)
		case "reasoning":
			hasReasoning = true
		default:
			return text // unknown type, not a reasoning array
		}
	}
	if !hasReasoning {
		return text
	}
	return strings.Join(texts, "\n")
}
