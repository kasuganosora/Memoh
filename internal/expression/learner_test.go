package expression

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"
)

// fakeLLM implements LLMService for testing.
type fakeLLM struct {
	mu       sync.Mutex
	response string
	err      error
	calls    int
}

func (f *fakeLLM) GenerateText(_ context.Context, _, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.response, f.err
}

// fakeExprRepo implements ExpressionRepository for testing.
type fakeExprRepo struct {
	mu    sync.Mutex
	items []ExpressionEntry
}

func (r *fakeExprRepo) Upsert(_ context.Context, entry ExpressionEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, entry)
	return nil
}

func (*fakeExprRepo) SearchBySituation(_ context.Context, _ string, _ []float64, _ int) ([]ExpressionEntry, error) {
	return nil, nil
}

func (*fakeExprRepo) ListUnchecked(_ context.Context, _ string, _ int) ([]ExpressionEntry, error) {
	return nil, nil
}

func (*fakeExprRepo) MarkChecked(_ context.Context, _ string, _ bool) error {
	return nil
}

// fakeJargRepo implements JargonRepository for testing.
type fakeJargRepo struct {
	mu    sync.Mutex
	items []JargonEntry
}

func (r *fakeJargRepo) Upsert(_ context.Context, entry JargonEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, entry)
	return nil
}

func (*fakeJargRepo) Query(_ context.Context, _ string, _ []string) ([]JargonEntry, error) {
	return nil, nil
}

func (*fakeJargRepo) List(_ context.Context, _ string, _ int) ([]JargonEntry, error) {
	return nil, nil
}

// testMessages returns a set of messages large enough to trigger learning.
func testMessages(n int) []Message {
	msgs := make([]Message, 0, n)
	for i := 0; i < n; i++ {
		msgs = append(msgs, Message{
			Role:    "user",
			Content: fmt.Sprintf("This is test message %d with enough content for extraction", i),
		})
	}
	return msgs
}

func llmResponseWithExpressions(situations, styles []string) string {
	type exprResp struct {
		Expressions []struct {
			Situation string `json:"situation"`
			Style     string `json:"style"`
		} `json:"expressions"`
		Jargons []struct {
			Content string `json:"content"`
			Meaning string `json:"meaning"`
		} `json:"jargons"`
	}
	resp := exprResp{}
	for i := range situations {
		resp.Expressions = append(resp.Expressions, struct {
			Situation string `json:"situation"`
			Style     string `json:"style"`
		}{
			Situation: situations[i],
			Style:     styles[i],
		})
	}
	data, _ := json.Marshal(resp)
	return string(data)
}

func llmResponseWithJargons(contents, meanings []string) string {
	type exprResp struct {
		Expressions []struct {
			Situation string `json:"situation"`
			Style     string `json:"style"`
		} `json:"expressions"`
		Jargons []struct {
			Content string `json:"content"`
			Meaning string `json:"meaning"`
		} `json:"jargons"`
	}
	resp := exprResp{}
	for i := range contents {
		resp.Jargons = append(resp.Jargons, struct {
			Content string `json:"content"`
			Meaning string `json:"meaning"`
		}{
			Content: contents[i],
			Meaning: meanings[i],
		})
	}
	data, _ := json.Marshal(resp)
	return string(data)
}

func TestExtractExpressionsFromLLM_EmptySessionID(t *testing.T) {
	response := llmResponseWithExpressions(
		[]string{"Greeting someone"},
		[]string{"Yo what's up"},
	)
	exprs, _, err := extractExpressionsFromLLM("bot-1", "", response)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exprs) != 1 {
		t.Fatalf("expected 1 expression, got %d", len(exprs))
	}
	if exprs[0].SessionID != "" {
		t.Fatalf("expected empty session ID, got %q", exprs[0].SessionID)
	}
	if exprs[0].BotID != "bot-1" {
		t.Fatalf("expected bot-1, got %q", exprs[0].BotID)
	}
}

func TestExtractExpressionsFromLLM_WithSessionID(t *testing.T) {
	response := llmResponseWithExpressions(
		[]string{"Expressing surprise"},
		[]string{"我嘞个去"},
	)
	exprs, _, err := extractExpressionsFromLLM("bot-2", "session-123", response)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exprs) != 1 {
		t.Fatalf("expected 1 expression, got %d", len(exprs))
	}
	if exprs[0].SessionID != "session-123" {
		t.Fatalf("expected session-123, got %q", exprs[0].SessionID)
	}
	if exprs[0].BotID != "bot-2" {
		t.Fatalf("expected bot-2, got %q", exprs[0].BotID)
	}
}

func TestExtractJargonsFromLLM_WithSessionID(t *testing.T) {
	response := llmResponseWithJargons(
		[]string{"yyds"},
		[]string{"永远的神"},
	)
	_, jargons, err := extractExpressionsFromLLM("bot-3", "session-456", response)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jargons) != 1 {
		t.Fatalf("expected 1 jargon, got %d", len(jargons))
	}
	if jargons[0].SessionID != "session-456" {
		t.Fatalf("expected session-456, got %q", jargons[0].SessionID)
	}
	if jargons[0].Content != "yyds" {
		t.Fatalf("expected yyds, got %q", jargons[0].Content)
	}
}

func TestExtractExpressionsFromLLM_InvalidJSON(t *testing.T) {
	_, _, err := extractExpressionsFromLLM("bot-1", "", "not json at all")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestExtractExpressionsFromLLM_EmptyFields(t *testing.T) {
	response := llmResponseWithExpressions(
		[]string{""},
		[]string{""},
	)
	exprs, _, err := extractExpressionsFromLLM("bot-1", "", response)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exprs) != 0 {
		t.Fatalf("expected 0 expressions (empty fields filtered), got %d", len(exprs))
	}
}

func TestLearner_AccumulateSessionID(t *testing.T) {
	llm := &fakeLLM{
		response: llmResponseWithExpressions(
			[]string{"Testing something"},
			[]string{"test style"},
		),
	}
	exprRepo := &fakeExprRepo{}
	jargRepo := &fakeJargRepo{}
	learner := NewLearner("bot-learn", llm, exprRepo, jargRepo, slog.Default())
	if learner == nil {
		t.Fatal("expected non-nil learner")
	}

	// Accumulate enough messages to trigger learning with a session ID.
	msgs := testMessages(minMessagesToLearn)
	learner.Accumulate(context.Background(), msgs, "misskey-timeline-1")

	// sessionID is set inside Accumulate under lock, so it's safe to read
	// as long as we don't concurrently call Accumulate again.
	if learner.lastSessionID != "misskey-timeline-1" {
		t.Fatalf("expected lastSessionID to be misskey-timeline-1, got %q", learner.lastSessionID)
	}
}

func TestLearner_AccumulateEmptySessionID(t *testing.T) {
	llm := &fakeLLM{
		response: llmResponseWithExpressions(
			[]string{"Testing something"},
			[]string{"test style"},
		),
	}
	exprRepo := &fakeExprRepo{}
	jargRepo := &fakeJargRepo{}
	learner := NewLearner("bot-empty", llm, exprRepo, jargRepo, slog.Default())

	// Accumulate with empty session ID should not change lastSessionID.
	msgs := testMessages(minMessagesToLearn)
	learner.Accumulate(context.Background(), msgs, "")

	if learner.lastSessionID != "" {
		t.Fatalf("expected empty lastSessionID, got %q", learner.lastSessionID)
	}
}

func TestLearner_LearnFromHistory_TracksSessionID(t *testing.T) {
	t.Parallel()

	llm := &fakeLLM{
		response: llmResponseWithExpressions(
			[]string{"Saying hello"},
			[]string{"hey there"},
		),
	}
	exprRepo := &fakeExprRepo{}
	jargRepo := &fakeJargRepo{}
	learner := NewLearner("bot-track", llm, exprRepo, jargRepo, slog.Default())

	// Set the session ID manually.
	learner.lastSessionID = "telegram-group-42"

	msgs := testMessages(5)
	err := learner.LearnFromHistory(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Wait a bit for async storage, then check.
	exprRepo.mu.Lock()
	items := make([]ExpressionEntry, len(exprRepo.items))
	copy(items, exprRepo.items)
	exprRepo.mu.Unlock()

	if len(items) == 0 {
		t.Fatal("expected at least 1 expression to be stored")
	}
	for _, item := range items {
		if item.SessionID != "telegram-group-42" {
			t.Fatalf("expected session ID telegram-group-42, got %q", item.SessionID)
		}
	}
}

func TestLearner_LearnFromHistory_NoLLM(t *testing.T) {
	exprRepo := &fakeExprRepo{}
	jargRepo := &fakeJargRepo{}
	learner := NewLearner("bot-nollm", nil, exprRepo, jargRepo, slog.Default())

	msgs := testMessages(5)
	err := learner.LearnFromHistory(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify nothing was stored.
	if len(exprRepo.items) != 0 {
		t.Fatalf("expected 0 expressions (no LLM), got %d", len(exprRepo.items))
	}
}

func TestLearner_LearnFromHistory_NoMessages(t *testing.T) {
	llm := &fakeLLM{
		response: "{}",
	}
	exprRepo := &fakeExprRepo{}
	jargRepo := &fakeJargRepo{}
	learner := NewLearner("bot-empty-msgs", llm, exprRepo, jargRepo, slog.Default())

	err := learner.LearnFromHistory(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exprRepo.items) != 0 {
		t.Fatalf("expected 0 expressions (no messages), got %d", len(exprRepo.items))
	}
}

func TestStripMarkdownFences(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no fences", `{"key": "value"}`, `{"key": "value"}`},
		{"with fences", "```\n{\"key\": \"value\"}\n```", `{"key": "value"}`},
		{"with json tag", "```json\n{\"key\": \"value\"}\n```", `{"key": "value"}`},
		{"whitespace only", "  ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripMarkdownFences(tt.input)
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestAccumulate_EmptyMessages(_ *testing.T) {
	learner := NewLearner("bot", &fakeLLM{}, &fakeExprRepo{}, &fakeJargRepo{}, slog.Default())
	// Should not panic.
	learner.Accumulate(context.Background(), nil, "any-session")
	learner.Accumulate(context.Background(), []Message{}, "any-session")
}

func TestAccumulate_IgnoresNonUserMessages(t *testing.T) {
	learner := NewLearner("bot", &fakeLLM{}, &fakeExprRepo{}, &fakeJargRepo{}, slog.Default())
	// Below threshold, so learner will buffer but not trigger learning.
	msgs := []Message{
		{Role: "system", Content: "system message"},
		{Role: "assistant", Content: "assistant response"},
	}
	learner.Accumulate(context.Background(), msgs, "")

	learner.mu.Lock()
	bufLen := len(learner.buffer)
	learner.mu.Unlock()
	if bufLen != 0 {
		t.Fatalf("expected 0 buffered messages (non-user filtered), got %d", bufLen)
	}
}

func TestAccumulate_OnlyUserMessagesBuffered(t *testing.T) {
	learner := NewLearner("bot", &fakeLLM{}, &fakeExprRepo{}, &fakeJargRepo{}, slog.Default())
	msgs := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
		{Role: "user", Content: "how are you"},
	}
	learner.Accumulate(context.Background(), msgs, "sess-1")

	learner.mu.Lock()
	bufLen := len(learner.buffer)
	learner.mu.Unlock()
	if bufLen != 2 {
		t.Fatalf("expected 2 buffered messages (user only), got %d", bufLen)
	}
}

func TestExtractExpressionsFromLLM_SessionIDOnBoth(t *testing.T) {
	resp := struct {
		Expressions []struct {
			Situation string `json:"situation"`
			Style     string `json:"style"`
		} `json:"expressions"`
		Jargons []struct {
			Content string `json:"content"`
			Meaning string `json:"meaning"`
		} `json:"jargons"`
	}{
		Expressions: []struct {
			Situation string `json:"situation"`
			Style     string `json:"style"`
		}{
			{Situation: "situation A", Style: "style A"},
		},
		Jargons: []struct {
			Content string `json:"content"`
			Meaning string `json:"meaning"`
		}{
			{Content: "lol", Meaning: "laugh out loud"},
		},
	}
	data, _ := json.Marshal(resp)

	exprs, jargons, err := extractExpressionsFromLLM("bot-10", "misskey-public", string(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exprs[0].SessionID != "misskey-public" {
		t.Fatalf("expression session ID mismatch: got %q", exprs[0].SessionID)
	}
	if jargons[0].SessionID != "misskey-public" {
		t.Fatalf("jargon session ID mismatch: got %q", jargons[0].SessionID)
	}
}

func TestLearner_JargonsStored(t *testing.T) {
	t.Parallel()

	llm := &fakeLLM{
		response: llmResponseWithJargons(
			[]string{"yygq"},
			[]string{"阴阳怪气"},
		),
	}
	exprRepo := &fakeExprRepo{}
	jargRepo := &fakeJargRepo{}
	learner := NewLearner("bot-jargon", llm, exprRepo, jargRepo, slog.Default())
	learner.lastSessionID = "discord-dev-group"

	err := learner.LearnFromHistory(context.Background(), testMessages(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	jargRepo.mu.Lock()
	items := make([]JargonEntry, len(jargRepo.items))
	copy(items, jargRepo.items)
	jargRepo.mu.Unlock()

	if len(items) != 1 {
		t.Fatalf("expected 1 jargon entry, got %d", len(items))
	}
	if items[0].Content != "yygq" {
		t.Fatalf("expected content yygq, got %q", items[0].Content)
	}
	if items[0].SessionID != "discord-dev-group" {
		t.Fatalf("expected session ID discord-dev-group, got %q", items[0].SessionID)
	}
}
