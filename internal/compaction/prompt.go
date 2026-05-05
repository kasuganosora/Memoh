package compaction

import (
	"encoding/json"
	"fmt"
	"strings"
)

const systemPrompt = `You are a conversation summarizer for an AI assistant. Given a conversation history, produce a concise summary that preserves:
- Key facts, decisions, and agreements
- User preferences and requests
- Important context needed for continuing the conversation
- Names, dates, numbers, and specific details
- Tool usage outcomes and their results

The assistant has a defined identity and personality. If <assistant_identity> is provided in the user prompt, use it to understand who the assistant is — this context must be preserved so the assistant can maintain its consistent personality and communication style when continuing the conversation. Include relevant identity cues (name, personality traits, communication style) in your summary alongside the factual content.

If <prior_context> is provided, it contains summaries of earlier conversation segments. Use them ONLY to understand the conversation flow and maintain continuity. Do NOT include, repeat, or rephrase any content from <prior_context> in your output.

For tool results, only include key outcomes; ignore intermediate steps or errors.

Output ONLY the summary of the new conversation segment. No preamble, no headers.`

type messageEntry struct {
	Role    string
	Content string
}

func buildUserPrompt(priorSummaries []string, messages []messageEntry, identityDescription string) string {
	var sb strings.Builder

	if identityDescription != "" {
		sb.WriteString("<assistant_identity>\n")
		sb.WriteString(identityDescription)
		sb.WriteString("\n</assistant_identity>\n\n")
	}

	if len(priorSummaries) > 0 {
		sb.WriteString("<prior_context>\n")
		sb.WriteString("The following are summaries of earlier parts of this conversation. They are provided ONLY as reference context to help you understand the conversation flow. Do NOT include or repeat any of this content in your output summary.\n\n")
		sb.WriteString(strings.Join(priorSummaries, "\n---\n"))
		sb.WriteString("\n</prior_context>\n\n")
		sb.WriteString("Now summarize the following conversation segment:\n")
	} else {
		sb.WriteString("Summarize the following conversation:\n")
	}
	for _, m := range messages {
		fmt.Fprintf(&sb, "%s: %s\n", m.Role, m.Content)
	}
	return sb.String()
}

// stripMultimodalContent removes image and file parts from raw message content
// JSON before it is included in compaction prompts. These parts contain large
// base64 payloads that waste tokens and are not useful for summarization.
// If the content is a simple string, it is returned as-is. If it is a structured
// array of parts, image/file parts are replaced with "[image]"/"[file]" placeholders.
func stripMultimodalContent(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return content
	}
	// Fast path: plain string content (most common for assistant/tool messages)
	if content[0] == '"' {
		return content
	}
	// Structured content: array of typed parts
	var parts []map[string]any
	if err := json.Unmarshal([]byte(content), &parts); err != nil {
		return content // not structured JSON, return as-is
	}
	changed := false
	for i, p := range parts {
		partType, _ := p["type"].(string)
		switch strings.ToLower(strings.TrimSpace(partType)) {
		case "image":
			parts[i] = map[string]any{"type": "text", "text": "[image]"}
			changed = true
		case "file":
			parts[i] = map[string]any{"type": "text", "text": "[file]"}
			changed = true
		}
	}
	if !changed {
		return content
	}
	out, err := json.Marshal(parts)
	if err != nil {
		return content
	}
	return string(out)
}
