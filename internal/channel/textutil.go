package channel

import (
	"encoding/json"
	"regexp"
	"strings"
)

// filterThinkingRe matches <thinking>...</thinking> and <think\>...</think\> blocks
// (including multiline content). Some LLMs (e.g. GLM) emit these as plain text
// instead of structured reasoning events.
var filterThinkingRe = regexp.MustCompile(`(?is)<think(?:ing)?>\s*.*?\s*</think(?:ing)?>`)

// filterToolCallXMLRe matches raw XML tool-call blocks that some models (e.g.
// xAI/Grok) emit as plain text instead of structured API tool_call responses.
// Matches paired tags with their content, and self-closing tags:
//
//	<xai:function_call>...</xai:function_call>
//	<parameter name="...">...</parameter>
//	<function_call>...</function_call>
//	<tool_call id="...">...</tool_call (including broken/unclosed tags)
var filterToolCallXMLRe = regexp.MustCompile(`(?is)<(?:[a-z]+:)?(?:function_call|parameter|invoke|tool_call|tool_result|execute)(?:\s[^>]*)?\s*>.*?(?:</(?:[a-z]+:)?(?:function_call|parameter|invoke|tool_call|tool_result|execute)(?:\s[^>]*)?\s*>|<(?:[a-z]+:)?(?:function_call|parameter|invoke|tool_call|tool_result|execute)(?:\s[^>]*)?\s*/>)`)

// filterToolCallXMLSelfClosing matches self-closing tool-call XML tags.
var filterToolCallXMLSelfClosing = regexp.MustCompile(`(?is)<(?:[a-z]+:)?(?:function_call|parameter|invoke|tool_call|tool_result|execute)(?:\s[^>]*)?\s*/>`)

// FilterThinkingTags strips <thinking>...</thinking> and <think\>...</think\> blocks
// from LLM output text. These tags may appear when a model does not use structured
// reasoning output and instead embeds thinking as raw text in the content.
func FilterThinkingTags(text string) string {
	return strings.TrimSpace(filterThinkingRe.ReplaceAllString(text, ""))
}

// FilterToolCallXML strips raw XML tool-call tags from LLM output text.
// Some models (e.g. xAI/Grok) emit function calls and parameters as raw XML in
// the text stream instead of using structured API responses. This removes such
// artifacts so they don't leak to the end user.
func FilterToolCallXML(text string) string {
	text = filterToolCallXMLRe.ReplaceAllString(text, "")
	text = filterToolCallXMLSelfClosing.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

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
