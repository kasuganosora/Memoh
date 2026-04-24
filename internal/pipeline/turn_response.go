package pipeline

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/memohai/memoh/internal/conversation"
	messagepkg "github.com/memohai/memoh/internal/message"
)

// DecodeTurnResponseEntry converts a persisted bot message into a TR entry for
// pipeline context composition.
//
// Unlike the old implementation (which only kept plain text and dropped all
// tool-call / tool-result payloads), this version renders the full turn —
// including tool calls and their results — into a single structured string
// so the LLM can observe its own prior tool usage when the conversation is
// later replayed or summarised.
//
// The rendering is intentionally compact and XML-flavoured so it survives
// round-trips through the merge/compose pipeline without being confused with
// the user-facing XML used by Rendering.
func DecodeTurnResponseEntry(msg messagepkg.Message) (TurnResponseEntry, bool) {
	role := strings.TrimSpace(msg.Role)
	if role != "assistant" && role != "tool" {
		return TurnResponseEntry{}, false
	}

	var modelMsg conversation.ModelMessage
	if err := json.Unmarshal(msg.Content, &modelMsg); err != nil {
		return TurnResponseEntry{}, false
	}

	var rendered string
	switch role {
	case "tool":
		rendered = renderToolRoleMessage(modelMsg)
	default:
		rendered = renderAssistantMessage(modelMsg)
	}

	if strings.TrimSpace(rendered) == "" {
		return TurnResponseEntry{}, false
	}

	return TurnResponseEntry{
		RequestedAtMs: msg.CreatedAt.UnixMilli(),
		Role:          role,
		Content:       rendered,
	}, true
}

// turnResponsePart is a permissive view of a persisted content part. It
// purposefully uses json.RawMessage for tool input/output to avoid losing
// structure while keeping the type declaration local to this package.
type turnResponsePart struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
}

func renderAssistantMessage(msg conversation.ModelMessage) string {
	var b strings.Builder

	// 1) Plain-string content (legacy format).
	if len(msg.Content) > 0 {
		var plain string
		if err := json.Unmarshal(msg.Content, &plain); err == nil {
			plain = strings.TrimSpace(plain)
			if plain != "" {
				b.WriteString(plain)
			}
		}
	}

	// 2) Array-of-parts content (Vercel AI SDK uiMessage format).
	var parts []turnResponsePart
	if len(msg.Content) > 0 {
		_ = json.Unmarshal(msg.Content, &parts)
	}

	for _, p := range parts {
		switch strings.ToLower(strings.TrimSpace(p.Type)) {
		case "text":
			text := strings.TrimSpace(p.Text)
			if text == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(text)
		case "reasoning":
			// Intentionally omitted: reasoning is model-internal and must not
			// leak back into subsequent prompts verbatim.
			continue
		case "tool-call":
			writeToolCallTag(&b, p.ToolCallID, p.ToolName, p.Input)
		case "tool-result":
			payload := p.Output
			if len(payload) == 0 {
				payload = p.Result
			}
			writeToolResultTag(&b, p.ToolCallID, p.ToolName, payload)
		}
	}

	// 3) Top-level ToolCalls field (older OpenAI-style wire format).
	for _, call := range msg.ToolCalls {
		id := strings.TrimSpace(call.ID)
		name := strings.TrimSpace(call.Function.Name)
		args := strings.TrimSpace(call.Function.Arguments)
		var input json.RawMessage
		if args != "" {
			// Arguments is a string containing JSON; try to keep it raw so
			// the downstream renderer doesn't double-escape.
			if json.Valid([]byte(args)) {
				input = json.RawMessage(args)
			} else {
				encoded, _ := json.Marshal(args)
				input = encoded
			}
		}
		writeToolCallTag(&b, id, name, input)
	}

	return b.String()
}

func renderToolRoleMessage(msg conversation.ModelMessage) string {
	// Two possible persistence shapes:
	//   a) Content is a JSON array of parts with type="tool-result".
	//   b) Content is the tool result itself, and ToolCallID is set on the
	//      ModelMessage envelope (older OpenAI-style format).
	var b strings.Builder

	var parts []turnResponsePart
	if len(msg.Content) > 0 {
		_ = json.Unmarshal(msg.Content, &parts)
	}
	for _, p := range parts {
		if strings.ToLower(strings.TrimSpace(p.Type)) != "tool-result" {
			continue
		}
		payload := p.Output
		if len(payload) == 0 {
			payload = p.Result
		}
		writeToolResultTag(&b, p.ToolCallID, p.ToolName, payload)
	}
	if b.Len() > 0 {
		return b.String()
	}

	if strings.TrimSpace(msg.ToolCallID) != "" {
		writeToolResultTag(&b, msg.ToolCallID, msg.Name, msg.Content)
	}
	return b.String()
}

func writeToolCallTag(b *strings.Builder, id, name string, input json.RawMessage) {
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	fmt.Fprintf(b, `<tool_call id=%q name=%q>`, escapeXMLAttrValue(strings.TrimSpace(id)), escapeXMLAttrValue(strings.TrimSpace(name)))
	if payload := formatToolPayload(input); payload != "" {
		b.WriteString(payload)
	}
	b.WriteString("</tool_call>")
}

func writeToolResultTag(b *strings.Builder, id, name string, payload json.RawMessage) {
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	fmt.Fprintf(b, `<tool_result id=%q name=%q>`, escapeXMLAttrValue(strings.TrimSpace(id)), escapeXMLAttrValue(strings.TrimSpace(name)))
	if rendered := formatToolPayload(payload); rendered != "" {
		b.WriteString(rendered)
	}
	b.WriteString("</tool_result>")
}

// formatToolPayload returns a compact textual representation of a tool
// input/output payload safe to embed inside a tag body.
func formatToolPayload(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}

	// If the payload is a JSON string, unquote it so the body reads naturally.
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		s := strings.TrimSpace(asString)
		if s == "" {
			return ""
		}
		return escapeXMLText(s)
	}

	// Otherwise, re-encode as compact JSON so whitespace is normalised and
	// any nested structured content round-trips losslessly.
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		encoded, err := json.Marshal(v)
		if err == nil {
			return escapeXMLText(string(encoded))
		}
	}
	return escapeXMLText(trimmed)
}
