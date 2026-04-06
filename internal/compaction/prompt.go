package compaction

import (
	"fmt"
	"strings"
)

const systemPrompt = `You are a conversation summarizer. Given a conversation history, produce a concise summary.

## What to Preserve (Priority Order)
1. Key decisions and outcomes
2. User preferences, requests, and constraints
3. Important context: file paths, variable names, API endpoints, configuration values
4. Tool outcomes: tool name + brief result (not full output)
5. Errors: what went wrong and how it was resolved
6. Specific identifiers: names, dates, numbers, URLs

## What to Compress
- Verbose tool outputs (large JSON, file contents) → key findings only
- Repeated exploration → final state only
- Debugging steps → root cause and fix only
- Code snippets → only the final/important version

## Format
- Use bullet points
- Prefix tool results with [Tool: name]
- Preserve exact values for paths, URLs, IDs, error messages
- Keep summaries concise (under 200 words when possible)

If <prior_context> is provided, it contains summaries of earlier conversation segments. Use them ONLY to understand the conversation flow. Do NOT repeat content from <prior_context>.

Output ONLY the summary. No preamble, no headers.`

type messageEntry struct {
	Role    string
	Content string
}

func buildUserPrompt(priorSummaries []string, messages []messageEntry) string {
	var sb strings.Builder
	if len(priorSummaries) > 0 {
		sb.WriteString("<prior_context>\n")
		sb.WriteString("The following are summaries of earlier parts of this conversation. They are provided ONLY as reference context. Do NOT include or repeat any of this content in your output.\n\n")
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
