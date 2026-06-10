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

func TestSend_FallbackWhenReplyTargetDeleted(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Parse the request body to check if replyId is set.
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		count := callCount.Add(1)

		if strings.HasSuffix(r.URL.Path, "/api/notes/create") {
			replyID, _ := body["replyId"].(string)
			if count == 1 && replyID != "" {
				// First call with replyId: simulate deleted note error.
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"No such note.","code":"NO_SUCH_NOTE"}}`))
				return
			}
			// Second call (retry without replyId) or call without replyId: success.
			resp := map[string]any{
				"createdNote": map[string]any{
					"id":   "new-note-id",
					"text": body["text"],
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

	msg := channel.PreparedOutboundMessage{
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Text: "hello world",
				Reply: &channel.ReplyRef{
					MessageID: "deleted-note-id",
				},
			},
		},
	}

	err := adapter.Send(context.Background(), cfg, msg)
	if err != nil {
		t.Fatalf("Send should succeed with fallback, got error: %v", err)
	}

	// Should have been called twice: first with replyId (fails), then without (succeeds).
	if got := callCount.Load(); got != 2 {
		t.Fatalf("expected 2 API calls (retry fallback), got %d", got)
	}
}

func TestSend_SuccessWithReplyTarget(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		callCount.Add(1)

		if strings.HasSuffix(r.URL.Path, "/api/notes/create") {
			// Success on first try.
			resp := map[string]any{
				"createdNote": map[string]any{
					"id":   "new-note-id",
					"text": body["text"],
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

	msg := channel.PreparedOutboundMessage{
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Text: "hello world",
				Reply: &channel.ReplyRef{
					MessageID: "valid-note-id",
				},
			},
		},
	}

	err := adapter.Send(context.Background(), cfg, msg)
	if err != nil {
		t.Fatalf("Send should succeed, got error: %v", err)
	}

	// Should only be called once (no retry needed).
	if got := callCount.Load(); got != 1 {
		t.Fatalf("expected 1 API call (no retry), got %d", got)
	}
}

func TestSend_NoReplyTarget_NoFallback(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		callCount.Add(1)

		if strings.HasSuffix(r.URL.Path, "/api/notes/create") {
			// Fail the request.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"Internal error"}}`))
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

	// No Reply reference — should not retry.
	msg := channel.PreparedOutboundMessage{
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Text: "hello world",
			},
		},
	}

	err := adapter.Send(context.Background(), cfg, msg)
	if err == nil {
		t.Fatal("Send should fail when no reply target and API errors")
	}

	// Should only be called once (no retry since no replyId).
	if got := callCount.Load(); got != 1 {
		t.Fatalf("expected 1 API call (no retry without replyId), got %d", got)
	}
}

func TestSend_TimelineNote_AutoMention(t *testing.T) {
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
					"id":   "reply-note-id",
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

	// Simulate receiving a timeline note and caching its mentions.
	// This mimics what handleTimelineNote does after the fix.
	me := &meResponse{ID: "bot-id", Username: "kanna"}
	timelineNote := misskeyNote{
		ID:     "timeline-note-123",
		Text:   "today is a nice day",
		UserID: "user-abc",
		User:   misskeyUser{ID: "user-abc", Username: "luna"},
	}
	adapter.storeMentions(timelineNote, me)

	// Now send a reply to this timeline note.
	cfg := channel.ChannelConfig{
		ID: "test-config",
		Credentials: map[string]any{
			"instanceURL": server.URL,
			"accessToken": "test-token",
		},
	}

	msg := channel.PreparedOutboundMessage{
		Target: "timeline-note-123",
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Text: "nice weather indeed!",
				Reply: &channel.ReplyRef{
					MessageID: "timeline-note-123",
				},
			},
		},
	}

	err := adapter.Send(context.Background(), cfg, msg)
	if err != nil {
		t.Fatalf("Send should succeed, got error: %v", err)
	}

	// Verify that the mention prefix @luna was prepended to the text.
	text, _ := capturedBody["text"].(string)
	if !strings.Contains(text, "@luna") {
		t.Errorf("expected text to contain '@luna' auto-mention, got %q", text)
	}
	if !strings.Contains(text, "nice weather indeed!") {
		t.Errorf("expected text to contain original message, got %q", text)
	}
}

func TestSend_TimelineNote_NoMentionWithoutStoreMentions(t *testing.T) {
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
					"id":   "reply-note-id",
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

	// Do NOT call storeMentions — simulating the bug before the fix.
	cfg := channel.ChannelConfig{
		ID: "test-config",
		Credentials: map[string]any{
			"instanceURL": server.URL,
			"accessToken": "test-token",
		},
	}

	msg := channel.PreparedOutboundMessage{
		Target: "unknown-note-456",
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Text: "nice weather indeed!",
				Reply: &channel.ReplyRef{
					MessageID: "unknown-note-456",
				},
			},
		},
	}

	err := adapter.Send(context.Background(), cfg, msg)
	if err != nil {
		t.Fatalf("Send should succeed, got error: %v", err)
	}

	// Without storeMentions, no auto-mention should be prepended.
	text, _ := capturedBody["text"].(string)
	if strings.HasPrefix(text, "@") {
		t.Errorf("expected no @ prefix without storeMentions, got %q", text)
	}
}

func TestSend_BothAttemptsFail(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)

		if strings.HasSuffix(r.URL.Path, "/api/notes/create") {
			// Always fail.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Some error"}}`))
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

	msg := channel.PreparedOutboundMessage{
		Message: channel.PreparedMessage{
			Message: channel.Message{
				Text: "hello world",
				Reply: &channel.ReplyRef{
					MessageID: "deleted-note-id",
				},
			},
		},
	}

	err := adapter.Send(context.Background(), cfg, msg)
	if err == nil {
		t.Fatal("Send should fail when both attempts fail")
	}

	// Should be called twice: first with replyId (fails), then without (also fails).
	if got := callCount.Load(); got != 2 {
		t.Fatalf("expected 2 API calls (both failed), got %d", got)
	}
}
