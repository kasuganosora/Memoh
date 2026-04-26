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

func TestFilterToolCallXML(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no xml tags",
			input: "Hello, world!",
			want:  "Hello, world!",
		},
		{
			name:  "xai function call removed",
			input: `<xai:function_call>Hello</xai:function_call>`,
			want:  "",
		},
		{
			name:  "parameter tag removed",
			input: `<parameter name="attachments">["/data/img.jpg"]</parameter>`,
			want:  "",
		},
		{
			name:  "tool call with surrounding text",
			input: `Before<xai:function_call>internal</xai:function_call>After`,
			want:  "BeforeAfter",
		},
		{
			name:  "multiple xml tags mixed with text",
			input: `Hello <parameter name="text">world</parameter> and <xai:function_call>stuff</xai:function_call> done`,
			want:  "Hello  and  done",
		},
		{
			name:  "function_call without namespace",
			input: `<function_call>do_something()</function_call>Result`,
			want:  "Result",
		},
		{
			name:  "tool_call with broken closing tag not matched",
			input: `<tool_call id="call_123" name="send">{"text":"hi"}</tool_callVisible text`,
			want:  `<tool_call id="call_123" name="send">{"text":"hi"}</tool_callVisible text`,
		},
		{
			name:  "invoke and tool_result tags",
			input: `<invoke>call</invoke>Answer<tool_result>{"ok":true}</tool_result>`,
			want:  "Answer",
		},
		{
			name:  "self-closing tag",
			input: `Before<parameter name="x"/>After`,
			want:  "BeforeAfter",
		},
		{
			name:  "execute tag",
			input: `<execute>rm -rf /</execute>Clean!`,
			want:  "Clean!",
		},
		{
			name:  "regular html not stripped",
			input: `<b>bold</b> and <i>italic</i>`,
			want:  "<b>bold</b> and <i>italic</i>",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterToolCallXML(tt.input)
			if got != tt.want {
				t.Errorf("FilterToolCallXML(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
