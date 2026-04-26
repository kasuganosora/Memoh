package expression

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
)

// LLMService is the interface for LLM-based extraction of expressions.
type LLMService interface {
	GenerateText(ctx context.Context, systemPrompt string, userMessage string) (string, error)
}

// Learner is a domain service that asynchronously learns expression patterns
// and jargon terms from accumulated chat messages.
type Learner struct {
	botID    string
	llm      LLMService
	exprRepo ExpressionRepository
	jargRepo JargonRepository
	pending  int32      // atomic — accumulated message count
	buffer   []Message  // guarded by mu
	mu       sync.Mutex // prevents concurrent learning runs
	logger   *slog.Logger
}

const (
	minMessagesToLearn = 10
	maxBufferMessages  = 200 // prevent unbounded memory growth
)

// NewLearner creates a new Learner for the given bot.
func NewLearner(botID string, llm LLMService, exprRepo ExpressionRepository, jargRepo JargonRepository, logger *slog.Logger) *Learner {
	return &Learner{
		botID:    botID,
		llm:      llm,
		exprRepo: exprRepo,
		jargRepo: jargRepo,
		logger:   logger.With(slog.String("component", "expression.learner")),
	}
}

// Accumulate adds messages to the pending count and buffer. When the threshold
// is reached, it attempts to start a non-blocking learning run.
func (l *Learner) Accumulate(ctx context.Context, messages []Message) {
	if len(messages) == 0 {
		return
	}

	// Buffer messages for later processing (user messages only).
	l.mu.Lock()
	for _, msg := range messages {
		if msg.Role == "user" && strings.TrimSpace(msg.Content) != "" {
			l.buffer = append(l.buffer, msg)
		}
	}
	// Trim buffer if it exceeds the max.
	if len(l.buffer) > maxBufferMessages {
		l.buffer = l.buffer[len(l.buffer)-maxBufferMessages:]
	}
	l.mu.Unlock()

	// Atomic increment — no lock needed for counting
	msgCount := len(messages)
	if msgCount > 1<<31-1 {
		msgCount = 1<<31 - 1 // max int32
	}
	newPending := atomic.AddInt32(&l.pending, int32(msgCount))

	if newPending >= minMessagesToLearn && l.mu.TryLock() {
		go func() {
			defer l.mu.Unlock()
			defer func() {
				if r := recover(); r != nil {
					if l.logger != nil {
						l.logger.Error("learner goroutine panic", slog.Any("panic", r))
					}
				}
			}()
			// Reset counter regardless of outcome
			atomic.StoreInt32(&l.pending, 0)
			// Snapshot buffer and clear it under lock.
			l.mu.Lock()
			bufCopy := make([]Message, len(l.buffer))
			copy(bufCopy, l.buffer)
			l.buffer = l.buffer[:0]
			l.mu.Unlock()
			if len(bufCopy) == 0 {
				return
			}
			if err := l.LearnFromHistory(ctx, bufCopy); err != nil {
				if l.logger != nil {
					l.logger.Warn("expression learn failed", slog.Any("error", err))
				}
			}
		}()
	}
}

// LearnFromHistory triggers a learning pass using the LLM to extract
// expressions and jargon from the provided chat messages.
func (l *Learner) LearnFromHistory(ctx context.Context, messages []Message) error {
	if l.llm == nil || len(messages) == 0 {
		return nil
	}

	// Build user prompt from recent messages.
	var sb strings.Builder
	sb.WriteString("Recent chat messages:\n\n")
	for _, msg := range messages {
		fmt.Fprintf(&sb, "[%s]: %s\n", msg.Role, strings.TrimSpace(msg.Content))
	}

	response, err := l.llm.GenerateText(ctx, learnPromptTemplate, sb.String())
	if err != nil {
		return fmt.Errorf("llm extraction: %w", err)
	}

	// Remove Markdown code fences if present.
	response = stripMarkdownFences(response)

	expressions, jargons, err := extractExpressionsFromLLM(l.botID, "", response)
	if err != nil {
		return fmt.Errorf("parse extraction: %w", err)
	}

	// Store extracted expressions.
	for _, expr := range expressions {
		if l.exprRepo != nil {
			if err := l.exprRepo.Upsert(ctx, expr); err != nil {
				if l.logger != nil {
					l.logger.Warn("upsert expression failed", slog.Any("error", err))
				}
			}
		}
	}

	// Store extracted jargons.
	for _, jargon := range jargons {
		if l.jargRepo != nil {
			if err := l.jargRepo.Upsert(ctx, jargon); err != nil {
				if l.logger != nil {
					l.logger.Warn("upsert jargon failed", slog.Any("error", err))
				}
			}
		}
	}

	if l.logger != nil {
		l.logger.Info("expression learning completed",
			slog.String("bot_id", l.botID),
			slog.Int("expressions", len(expressions)),
			slog.Int("jargons", len(jargons)),
		)
	}

	return nil
}

// stripMarkdownFences removes ```json ... ``` wrapping from LLM output.
func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	// Strip ```json prefix and ``` suffix if present.
	if strings.HasPrefix(s, "```") {
		// Find first newline to skip the fence marker line.
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if lastIdx := strings.LastIndex(s, "```"); lastIdx != -1 {
			s = s[:lastIdx]
		}
	}
	return strings.TrimSpace(s)
}

// learnPromptTemplate is the system prompt for expression/jargon extraction.
const learnPromptTemplate = `You are a linguistic analyzer. Analyze the provided chat messages and extract:

1. **Expressions** — Situation-to-style mappings. For each:
   - "situation": A brief description of the conversational situation
   - "style": The characteristic phrase or style the bot used in response

2. **Jargon** — Slang, abbreviations, or inside jokes used in the chat. For each:
   - "content": The word or phrase
   - "meaning": What it means

Return ONLY a JSON object with "expressions" and "jargons" arrays.`

// extractRequest is the LLM response format for expression extraction.
type extractRequest struct {
	Expressions []struct {
		Situation string `json:"situation"`
		Style     string `json:"style"`
	} `json:"expressions"`
	Jargons []struct {
		Content string `json:"content"`
		Meaning string `json:"meaning"`
	} `json:"jargons"`
}

// extractExpressionsFromLLM parses the LLM response into expression/jargon entries.
func extractExpressionsFromLLM(botID, sessionID, response string) ([]ExpressionEntry, []JargonEntry, error) {
	var result extractRequest
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return nil, nil, fmt.Errorf("parse expression extraction: %w", err)
	}

	var expressions []ExpressionEntry
	for _, e := range result.Expressions {
		if strings.TrimSpace(e.Situation) == "" || strings.TrimSpace(e.Style) == "" {
			continue
		}
		expressions = append(expressions, ExpressionEntry{
			BotID:     botID,
			SessionID: sessionID,
			Situation: strings.TrimSpace(e.Situation),
			Style:     strings.TrimSpace(e.Style),
			Count:     1,
		})
	}

	var jargons []JargonEntry
	for _, j := range result.Jargons {
		if strings.TrimSpace(j.Content) == "" {
			continue
		}
		jargons = append(jargons, JargonEntry{
			BotID:     botID,
			SessionID: sessionID,
			Content:   strings.TrimSpace(j.Content),
			Meaning:   strings.TrimSpace(j.Meaning),
			Count:     1,
		})
	}

	return expressions, jargons, nil
}
