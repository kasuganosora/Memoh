package flow

import (
	"encoding/json"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/conversation"
)

func TestStripNonTextParts(t *testing.T) {
	tests := []struct {
		name     string
		input    sdk.Message
		expected sdk.Message
	}{
		{
			name: "text-only message unchanged",
			input: sdk.Message{
				Role:    sdk.MessageRoleUser,
				Content: []sdk.MessagePart{sdk.TextPart{Text: "hello"}},
			},
			expected: sdk.Message{
				Role:    sdk.MessageRoleUser,
				Content: []sdk.MessagePart{sdk.TextPart{Text: "hello"}},
			},
		},
		{
			name: "image part replaced with placeholder",
			input: sdk.Message{
				Role: sdk.MessageRoleUser,
				Content: []sdk.MessagePart{
					sdk.TextPart{Text: "look at this"},
					sdk.ImagePart{Image: "data:image/png;base64,abc123", MediaType: "image/png"},
				},
			},
			expected: sdk.Message{
				Role: sdk.MessageRoleUser,
				Content: []sdk.MessagePart{
					sdk.TextPart{Text: "look at this"},
					sdk.TextPart{Text: "[image]"},
				},
			},
		},
		{
			name: "file part replaced with placeholder",
			input: sdk.Message{
				Role: sdk.MessageRoleUser,
				Content: []sdk.MessagePart{
					sdk.FilePart{Data: "base64data", MediaType: "application/pdf", Filename: "doc.pdf"},
				},
			},
			expected: sdk.Message{
				Role:    sdk.MessageRoleUser,
				Content: []sdk.MessagePart{sdk.TextPart{Text: "[file]"}},
			},
		},
		{
			name: "tool call parts preserved",
			input: sdk.Message{
				Role: sdk.MessageRoleAssistant,
				Content: []sdk.MessagePart{
					sdk.TextPart{Text: "let me check"},
					sdk.ToolCallPart{ToolCallID: "tc1", ToolName: "search", Input: map[string]any{"query": "test"}},
				},
			},
			expected: sdk.Message{
				Role: sdk.MessageRoleAssistant,
				Content: []sdk.MessagePart{
					sdk.TextPart{Text: "let me check"},
					sdk.ToolCallPart{ToolCallID: "tc1", ToolName: "search", Input: map[string]any{"query": "test"}},
				},
			},
		},
		{
			name: "mixed parts with image and file",
			input: sdk.Message{
				Role: sdk.MessageRoleUser,
				Content: []sdk.MessagePart{
					sdk.TextPart{Text: "here are things"},
					sdk.ImagePart{Image: "data:image/png;base64,abc123"},
					sdk.FilePart{Data: "base64data", Filename: "doc.pdf"},
					sdk.TextPart{Text: "and more text"},
				},
			},
			expected: sdk.Message{
				Role: sdk.MessageRoleUser,
				Content: []sdk.MessagePart{
					sdk.TextPart{Text: "here are things"},
					sdk.TextPart{Text: "[image]"},
					sdk.TextPart{Text: "[file]"},
					sdk.TextPart{Text: "and more text"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripNonTextParts(tt.input)
			if len(result.Content) != len(tt.expected.Content) {
				t.Fatalf("expected %d parts, got %d", len(tt.expected.Content), len(result.Content))
			}
			for i, exp := range tt.expected.Content {
				got := result.Content[i]
				if exp.PartType() != got.PartType() {
					t.Errorf("part %d: expected type %s, got %s", i, exp.PartType(), got.PartType())
				}
				if tp, ok := exp.(sdk.TextPart); ok {
					if gotTP, ok := got.(sdk.TextPart); !ok || tp.Text != gotTP.Text {
						t.Errorf("part %d: expected text %q, got %q", i, tp.Text, gotTP.Text)
					}
				}
			}
		})
	}
}

func TestModelMessagesToSDKMessagesWithVisionControl(t *testing.T) {
	// Create a ModelMessage with image content
	imageContent := []map[string]any{
		{"type": "text", "text": "describe this"},
		{"type": "image", "image": "data:image/png;base64,abc123", "mediaType": "image/png"},
	}
	contentJSON, _ := json.Marshal(imageContent)

	msgs := []conversation.ModelMessage{
		{Role: "user", Content: json.RawMessage(contentJSON)},
		{Role: "assistant", Content: json.RawMessage(`"I see a picture"`)},
	}

	t.Run("with vision support keeps image parts", func(t *testing.T) {
		result := modelMessagesToSDKMessagesWithVisionControl(msgs, true)
		if len(result) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(result))
		}
		// First message should have image part intact
		hasImage := false
		for _, p := range result[0].Content {
			if _, ok := p.(sdk.ImagePart); ok {
				hasImage = true
			}
		}
		if !hasImage {
			t.Error("expected image part to be preserved when vision is supported")
		}
	})

	t.Run("without vision support strips image parts", func(t *testing.T) {
		result := modelMessagesToSDKMessagesWithVisionControl(msgs, false)
		if len(result) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(result))
		}
		// First message should NOT have image part
		for _, p := range result[0].Content {
			if _, ok := p.(sdk.ImagePart); ok {
				t.Error("expected image part to be stripped when vision is not supported")
			}
		}
		// Should have text part and placeholder
		hasPlaceholder := false
		for _, p := range result[0].Content {
			if tp, ok := p.(sdk.TextPart); ok && tp.Text == "[image]" {
				hasPlaceholder = true
			}
		}
		if !hasPlaceholder {
			t.Error("expected [image] placeholder when vision is not supported")
		}
	})

	t.Run("plain text messages unaffected", func(t *testing.T) {
		textMsgs := []conversation.ModelMessage{
			{Role: "user", Content: json.RawMessage(`"hello"`)},
			{Role: "assistant", Content: json.RawMessage(`"hi there"`)},
		}
		withVision := modelMessagesToSDKMessagesWithVisionControl(textMsgs, true)
		withoutVision := modelMessagesToSDKMessagesWithVisionControl(textMsgs, false)
		if len(withVision) != len(withoutVision) {
			t.Error("text-only messages should be identical regardless of vision flag")
		}
	})
}
