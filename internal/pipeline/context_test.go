package pipeline

import (
	"testing"
)

func TestMergeContext_ImageOnlySegment_NoEmptyUserMessage(t *testing.T) {
	// A segment with only image pieces should not produce a user message.
	rc := RenderedContext{
		{
			ReceivedAtMs: 1000,
			Content: []RenderedContentPiece{
				{Type: "image", URL: "https://example.com/img.png"},
			},
		},
	}

	messages := MergeContext(rc, nil)

	if len(messages) != 0 {
		t.Errorf("expected 0 messages for image-only RC, got %d: %+v", len(messages), messages)
	}
}

func TestMergeContext_MixedTextAndImage_OnlyTextIncluded(t *testing.T) {
	rc := RenderedContext{
		{
			ReceivedAtMs: 1000,
			Content: []RenderedContentPiece{
				{Type: "image", URL: "https://example.com/img.png"},
				{Type: "text", Text: "hello world"},
			},
		},
	}

	messages := MergeContext(rc, nil)

	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0].Content != "hello world" {
		t.Errorf("expected 'hello world', got %q", messages[0].Content)
	}
}

func TestMergeContext_EmptyTextPiece_Skipped(t *testing.T) {
	rc := RenderedContext{
		{
			ReceivedAtMs: 1000,
			Content: []RenderedContentPiece{
				{Type: "text", Text: ""},
				{Type: "text", Text: "actual content"},
			},
		},
	}

	messages := MergeContext(rc, nil)

	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0].Content != "actual content" {
		t.Errorf("expected 'actual content', got %q", messages[0].Content)
	}
}

func TestMergeContext_EmptyRC_EmptyTRs(t *testing.T) {
	messages := MergeContext(nil, nil)
	if len(messages) != 0 {
		t.Errorf("expected 0 messages for nil RC and nil TRs, got %d", len(messages))
	}

	messages = MergeContext(RenderedContext{}, []TurnResponseEntry{})
	if len(messages) != 0 {
		t.Errorf("expected 0 messages for empty RC and empty TRs, got %d", len(messages))
	}
}

func TestMergeContext_InterleavingOrder(t *testing.T) {
	rc := RenderedContext{
		{
			ReceivedAtMs: 1000,
			Content:      []RenderedContentPiece{{Type: "text", Text: "user msg 1"}},
		},
		{
			ReceivedAtMs: 3000,
			Content:      []RenderedContentPiece{{Type: "text", Text: "user msg 2"}},
		},
	}
	trs := []TurnResponseEntry{
		{RequestedAtMs: 2000, Role: "assistant", Content: "bot reply"},
	}

	messages := MergeContext(rc, trs)

	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}
	if messages[0].Role != "user" || messages[0].Content != "user msg 1" {
		t.Errorf("msg[0] unexpected: %+v", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].Content != "bot reply" {
		t.Errorf("msg[1] unexpected: %+v", messages[1])
	}
	if messages[2].Role != "user" || messages[2].Content != "user msg 2" {
		t.Errorf("msg[2] unexpected: %+v", messages[2])
	}
}
