package misskey

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/memohai/memoh/internal/channel"
)

func TestParseTimelineConfig_AllTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		routing  map[string]any
		expected timelineConfig
	}{
		{
			name:     "nil routing",
			routing:  nil,
			expected: timelineConfig{},
		},
		{
			name:     "empty routing",
			routing:  map[string]any{},
			expected: timelineConfig{},
		},
		{
			name: "home only",
			routing: map[string]any{
				"timeline": map[string]any{"home": true},
			},
			expected: timelineConfig{Home: true},
		},
		{
			name: "local only",
			routing: map[string]any{
				"timeline": map[string]any{"local": true},
			},
			expected: timelineConfig{Local: true},
		},
		{
			name: "global only",
			routing: map[string]any{
				"timeline": map[string]any{"global": true},
			},
			expected: timelineConfig{Global: true},
		},
		{
			name: "hybrid only",
			routing: map[string]any{
				"timeline": map[string]any{"hybrid": true},
			},
			expected: timelineConfig{Hybrid: true},
		},
		{
			name: "all timelines enabled with discuss",
			routing: map[string]any{
				"timeline": map[string]any{
					"home":    true,
					"local":   true,
					"global":  true,
					"hybrid":  true,
					"discuss": true,
				},
			},
			expected: timelineConfig{Home: true, Local: true, Global: true, Hybrid: true, Discuss: true},
		},
		{
			name: "string booleans",
			routing: map[string]any{
				"timeline": map[string]any{
					"home":   "true",
					"global": "false",
					"hybrid": "1",
				},
			},
			expected: timelineConfig{Home: true, Hybrid: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTimelineConfig(tt.routing)
			if got != tt.expected {
				t.Errorf("parseTimelineConfig() = %+v, want %+v", got, tt.expected)
			}
		})
	}
}

func TestClassifyNoteType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		note     misskeyNote
		expected string
	}{
		{
			name:     "original note",
			note:     misskeyNote{ID: "1", Text: "hello world"},
			expected: "note",
		},
		{
			name:     "reply",
			note:     misskeyNote{ID: "2", Text: "replying", ReplyID: "1"},
			expected: "reply",
		},
		{
			name:     "pure renote (boost)",
			note:     misskeyNote{ID: "3", RenoteID: "1", Renote: &misskeyNote{ID: "1", Text: "original"}},
			expected: "renote",
		},
		{
			name:     "quote (renote with text)",
			note:     misskeyNote{ID: "4", Text: "my commentary", RenoteID: "1", Renote: &misskeyNote{ID: "1", Text: "original"}},
			expected: "quote",
		},
		{
			name:     "renote with files but no text is quote",
			note:     misskeyNote{ID: "5", RenoteID: "1", Files: []misskeyFile{{ID: "f1", URL: "http://example.com/img.png", Type: "image/png"}}},
			expected: "quote",
		},
		{
			name:     "empty note with renoteId and no text/files is renote",
			note:     misskeyNote{ID: "6", RenoteID: "1"},
			expected: "renote",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyNoteType(tt.note)
			if got != tt.expected {
				t.Errorf("classifyNoteType() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestSend_WithRenoteID_Quote(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if strings.HasSuffix(r.URL.Path, "/api/notes/create") {
			resp := map[string]any{
				"createdNote": map[string]any{
					"id":   "new-note-id",
					"text": capturedBody["text"],
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	adapter := NewMisskeyAdapter(slog.Default())

	cfg := channel.ChannelConfig{
		ID: "test-config",
		Credentials: map[string]any{
			"instanceURL": server.URL,
			"accessToken": "test-token",
		},
	}

	// Send a quote note (renote with text).
	msg := channel.PreparedOutboundMessage{
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Text: "my commentary on this note",
				Metadata: map[string]any{
					"renote_id": "original-note-id",
				},
			},
		},
	}

	err := adapter.Send(context.Background(), cfg, msg)
	if err != nil {
		t.Fatalf("Send should succeed, got error: %v", err)
	}

	// Verify renoteId was sent in the request.
	if rid, ok := capturedBody["renoteId"].(string); !ok || rid != "original-note-id" {
		t.Errorf("expected renoteId='original-note-id', got %v", capturedBody["renoteId"])
	}
	// Verify text was also sent (quote, not pure renote).
	if text, ok := capturedBody["text"].(string); !ok || text != "my commentary on this note" {
		t.Errorf("expected text='my commentary on this note', got %v", capturedBody["text"])
	}
}

func TestSend_WithRenoteID_PureRenote(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if strings.HasSuffix(r.URL.Path, "/api/notes/create") {
			resp := map[string]any{
				"createdNote": map[string]any{
					"id":   "new-note-id",
					"text": "",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	adapter := NewMisskeyAdapter(slog.Default())

	cfg := channel.ChannelConfig{
		ID: "test-config",
		Credentials: map[string]any{
			"instanceURL": server.URL,
			"accessToken": "test-token",
		},
	}

	// Send a pure renote (boost) — renoteId set but no text.
	// Note: the Send method requires text or attachments, so for a pure renote
	// we need to handle the case where text is empty but renoteId is set.
	// Currently Send returns error if text is empty, so we test with minimal text.
	msg := channel.PreparedOutboundMessage{
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Text: "🔁", // minimal indicator for renote
				Metadata: map[string]any{
					"renote_id": "boosted-note-id",
				},
			},
		},
	}

	err := adapter.Send(context.Background(), cfg, msg)
	if err != nil {
		t.Fatalf("Send should succeed, got error: %v", err)
	}

	// Verify renoteId was sent.
	if rid, ok := capturedBody["renoteId"].(string); !ok || rid != "boosted-note-id" {
		t.Errorf("expected renoteId='boosted-note-id', got %v", capturedBody["renoteId"])
	}
}

func TestSend_WithReplyAndRenote(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if strings.HasSuffix(r.URL.Path, "/api/notes/create") {
			resp := map[string]any{
				"createdNote": map[string]any{
					"id":   "new-note-id",
					"text": capturedBody["text"],
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	adapter := NewMisskeyAdapter(slog.Default())

	cfg := channel.ChannelConfig{
		ID: "test-config",
		Credentials: map[string]any{
			"instanceURL": server.URL,
			"accessToken": "test-token",
		},
	}

	// Send a reply that also quotes another note.
	msg := channel.PreparedOutboundMessage{
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Text: "replying with a quote",
				Reply: &channel.ReplyRef{
					MessageID: "reply-target-id",
				},
				Metadata: map[string]any{
					"renote_id": "quoted-note-id",
				},
			},
		},
	}

	err := adapter.Send(context.Background(), cfg, msg)
	if err != nil {
		t.Fatalf("Send should succeed, got error: %v", err)
	}

	// Verify both replyId and renoteId were sent.
	if rid, ok := capturedBody["replyId"].(string); !ok || rid != "reply-target-id" {
		t.Errorf("expected replyId='reply-target-id', got %v", capturedBody["replyId"])
	}
	if rnid, ok := capturedBody["renoteId"].(string); !ok || rnid != "quoted-note-id" {
		t.Errorf("expected renoteId='quoted-note-id', got %v", capturedBody["renoteId"])
	}
}

func TestBuildInboundMessage_NoteTypeMetadata(t *testing.T) {
	t.Parallel()

	me := &meResponse{ID: "bot-id", Username: "bot"}
	adapter := &MisskeyAdapter{}
	_ = adapter // suppress unused warning

	tests := []struct {
		name         string
		note         misskeyNote
		wantType     string
		wantRenoteID string
		wantReplyID  string
	}{
		{
			name:     "original note",
			note:     misskeyNote{ID: "n1", Text: "@bot hello", UserID: "u1", User: misskeyUser{Username: "alice"}, Mentions: []string{"bot-id"}},
			wantType: "note",
		},
		{
			name:        "reply note",
			note:        misskeyNote{ID: "n2", Text: "@bot replying", UserID: "u1", User: misskeyUser{Username: "alice"}, ReplyID: "n0", Mentions: []string{"bot-id"}},
			wantType:    "reply",
			wantReplyID: "n0",
		},
		{
			name:         "pure renote",
			note:         misskeyNote{ID: "n3", UserID: "u1", User: misskeyUser{Username: "alice"}, RenoteID: "n1", Renote: &misskeyNote{ID: "n1", Text: "original content", User: misskeyUser{Username: "bob"}}, Mentions: []string{"bot-id"}},
			wantType:     "renote",
			wantRenoteID: "n1",
		},
		{
			name:         "quote note",
			note:         misskeyNote{ID: "n4", Text: "@bot check this out", UserID: "u1", User: misskeyUser{Username: "alice"}, RenoteID: "n1", Renote: &misskeyNote{ID: "n1", Text: "original content", User: misskeyUser{Username: "bob"}}, Mentions: []string{"bot-id"}},
			wantType:     "quote",
			wantRenoteID: "n1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, ok := (*MisskeyAdapter).buildInboundMessage(nil, me, tt.note)
			if !ok {
				t.Fatal("buildInboundMessage returned false")
			}

			noteType, _ := msg.Metadata["note_type"].(string)
			if noteType != tt.wantType {
				t.Errorf("note_type = %q, want %q", noteType, tt.wantType)
			}

			renoteID, _ := msg.Metadata["renote_id"].(string)
			if renoteID != tt.wantRenoteID {
				t.Errorf("renote_id = %q, want %q", renoteID, tt.wantRenoteID)
			}

			replyID, _ := msg.Metadata["reply_id"].(string)
			if replyID != tt.wantReplyID {
				t.Errorf("reply_id = %q, want %q", replyID, tt.wantReplyID)
			}
		})
	}
}

func TestSend_NoRenoteID_NotIncluded(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]any
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		callCount.Add(1)

		if strings.HasSuffix(r.URL.Path, "/api/notes/create") {
			resp := map[string]any{
				"createdNote": map[string]any{
					"id":   "new-note-id",
					"text": capturedBody["text"],
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	adapter := NewMisskeyAdapter(slog.Default())

	cfg := channel.ChannelConfig{
		ID: "test-config",
		Credentials: map[string]any{
			"instanceURL": server.URL,
			"accessToken": "test-token",
		},
	}

	// Send a normal note without renoteId.
	msg := channel.PreparedOutboundMessage{
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Text: "just a normal note",
			},
		},
	}

	err := adapter.Send(context.Background(), cfg, msg)
	if err != nil {
		t.Fatalf("Send should succeed, got error: %v", err)
	}

	// Verify renoteId is empty/not set.
	if rid, ok := capturedBody["renoteId"].(string); ok && rid != "" {
		t.Errorf("expected no renoteId, got %q", rid)
	}

	if got := callCount.Load(); got != 1 {
		t.Fatalf("expected 1 API call, got %d", got)
	}
}
