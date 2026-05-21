package pipeline

import (
	"testing"
)

// TestReduce_PureFunction_EditDoesNotMutateOriginal verifies that applying an
// EditEvent via Reduce does not modify the original IC's message content.
func TestReduce_PureFunction_EditDoesNotMutateOriginal(t *testing.T) {
	originalContent := []ContentNode{{Type: "text", Text: "hello"}}
	ic := IntermediateContext{
		SessionID: "s1",
		Nodes: []ICNode{
			{Message: &ICMessage{
				MessageID:    "m1",
				ReceivedAtMs: 1000,
				Content:      originalContent,
			}},
		},
		Users: make(map[string]ICUserState),
	}

	editEvent := EditEvent{
		SessionID:    "s1",
		MessageID:    "m1",
		ReceivedAtMs: 2000,
		TimestampSec: 2,
		UTCOffsetMin: 0,
		Content:      []ContentNode{{Type: "text", Text: "edited"}},
	}

	newIC := Reduce(ic, editEvent)

	// The new IC should have the edited content.
	if newIC.Nodes[0].Message.Content[0].Text != "edited" {
		t.Errorf("expected new IC message content to be 'edited', got %q",
			newIC.Nodes[0].Message.Content[0].Text)
	}

	// The original IC must NOT be mutated.
	if ic.Nodes[0].Message.Content[0].Text != "hello" {
		t.Errorf("original IC was mutated: expected 'hello', got %q",
			ic.Nodes[0].Message.Content[0].Text)
	}
}

// TestReduce_PureFunction_DeleteDoesNotMutateOriginal verifies that applying a
// DeleteEvent via Reduce does not modify the original IC's message Deleted flag.
func TestReduce_PureFunction_DeleteDoesNotMutateOriginal(t *testing.T) {
	ic := IntermediateContext{
		SessionID: "s1",
		Nodes: []ICNode{
			{Message: &ICMessage{
				MessageID:    "m1",
				ReceivedAtMs: 1000,
				Content:      []ContentNode{{Type: "text", Text: "hello"}},
			}},
		},
		Users: make(map[string]ICUserState),
	}

	deleteEvent := DeleteEvent{
		SessionID:    "s1",
		MessageIDs:   []string{"m1"},
		ReceivedAtMs: 2000,
		TimestampSec: 2,
	}

	newIC := Reduce(ic, deleteEvent)

	// The new IC should have the message marked as deleted.
	if !newIC.Nodes[0].Message.Deleted {
		t.Error("expected new IC message to be marked as deleted")
	}

	// The original IC must NOT be mutated.
	if ic.Nodes[0].Message.Deleted {
		t.Error("original IC was mutated: message should not be marked as deleted")
	}
}

// TestReduce_PureFunction_MessageDoesNotMutateOriginalNodes verifies that
// appending a new message via Reduce does not affect the original IC's Nodes slice.
func TestReduce_PureFunction_MessageDoesNotMutateOriginalNodes(t *testing.T) {
	ic := IntermediateContext{
		SessionID: "s1",
		Nodes: []ICNode{
			{Message: &ICMessage{
				MessageID:    "m1",
				ReceivedAtMs: 1000,
				Content:      []ContentNode{{Type: "text", Text: "first"}},
			}},
		},
		Users: make(map[string]ICUserState),
	}

	msgEvent := MessageEvent{
		SessionID:    "s1",
		MessageID:    "m2",
		ReceivedAtMs: 2000,
		TimestampSec: 2,
		Content:      []ContentNode{{Type: "text", Text: "second"}},
	}

	newIC := Reduce(ic, msgEvent)

	if len(newIC.Nodes) != 2 {
		t.Fatalf("expected 2 nodes in new IC, got %d", len(newIC.Nodes))
	}
	if len(ic.Nodes) != 1 {
		t.Fatalf("original IC nodes were mutated: expected 1, got %d", len(ic.Nodes))
	}
}
