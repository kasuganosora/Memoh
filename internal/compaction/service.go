package compaction

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/db/sqlc"
	"github.com/memohai/memoh/internal/models"
)

const (
	// MaxConsecutiveFailures is the number of consecutive compaction failures
	// before auto-compaction is paused (circuit breaker).
	MaxConsecutiveFailures = 3

	// DefaultKeepRecentTurns is the number of recent complete conversation turns
	// to preserve during smart compaction.
	DefaultKeepRecentTurns = 3

	// LargeToolResultThreshold is the token threshold above which tool results
	// are given boosted compaction priority.
	LargeToolResultThreshold = 2000
)

// Service manages context compaction for bot conversations.
type Service struct {
	queries *sqlc.Queries
	logger  *slog.Logger
	failMu  sync.Mutex
	// per-bot consecutive compaction failures for circuit breaker.
	failCounts map[string]int
}

// NewService creates a new compaction Service.
func NewService(log *slog.Logger, queries *sqlc.Queries) *Service {
	return &Service{
		queries:    queries,
		logger:     log,
		failCounts: make(map[string]int),
	}
}

// ShouldCompact returns true if inputTokens exceeds the threshold.
func ShouldCompact(inputTokens, threshold int) bool {
	return threshold > 0 && inputTokens >= threshold
}

// TriggerCompaction runs compaction in the background.
// Implements a circuit breaker: after MaxConsecutiveFailures consecutive failures,
// auto-compaction is paused until a successful run resets the counter.
func (s *Service) TriggerCompaction(ctx context.Context, cfg TriggerConfig) {
	if s.isCircuitOpen(cfg.BotID) {
		s.logger.Warn("compaction circuit breaker open, skipping",
			slog.String("bot_id", cfg.BotID),
			slog.String("session_id", cfg.SessionID),
		)
		return
	}
	go func() {
		bgCtx := context.WithoutCancel(ctx)
		if err := s.runCompaction(bgCtx, cfg); err != nil {
			s.recordFailure(cfg.BotID)
			s.logger.Error("compaction failed",
				slog.String("bot_id", cfg.BotID),
				slog.String("session_id", cfg.SessionID),
				slog.String("error", err.Error()))
		} else {
			s.recordSuccess(cfg.BotID)
		}
	}()
}

func (s *Service) isCircuitOpen(botID string) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	return s.failCounts[botID] >= MaxConsecutiveFailures
}

func (s *Service) recordFailure(botID string) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	s.failCounts[botID]++
	if s.failCounts[botID] >= MaxConsecutiveFailures {
		s.logger.Warn("compaction circuit breaker opened",
			slog.String("bot_id", botID),
			slog.Int("consecutive_failures", s.failCounts[botID]),
		)
	}
}

func (s *Service) recordSuccess(botID string) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	if s.failCounts[botID] > 0 {
		s.logger.Info("compaction circuit breaker reset",
			slog.String("bot_id", botID),
			slog.Int("previous_failures", s.failCounts[botID]),
		)
		delete(s.failCounts, botID)
	}
}

func (s *Service) runCompaction(ctx context.Context, cfg TriggerConfig) error {
	botUUID, err := db.ParseUUID(cfg.BotID)
	if err != nil {
		return err
	}
	sessionUUID, err := db.ParseUUID(cfg.SessionID)
	if err != nil {
		return err
	}

	logRow, err := s.queries.CreateCompactionLog(ctx, sqlc.CreateCompactionLogParams{
		BotID:     botUUID,
		SessionID: sessionUUID,
	})
	if err != nil {
		return err
	}

	compactErr := s.doCompaction(ctx, logRow.ID, sessionUUID, cfg)
	if compactErr != nil {
		s.completeLog(ctx, logRow.ID, "error", "", compactErr.Error(), 0, nil, pgtype.UUID{})
	}
	return compactErr
}

func (s *Service) doCompaction(ctx context.Context, logID pgtype.UUID, sessionUUID pgtype.UUID, cfg TriggerConfig) error {
	messages, err := s.queries.ListUncompactedMessagesBySession(ctx, sessionUUID)
	if err != nil {
		return err
	}
	if len(messages) == 0 {
		s.completeLog(ctx, logID, "ok", "", "", 0, nil, pgtype.UUID{})
		return nil
	}

	// Use smart strategy: preserve recent turns, compact expensive older content.
	// Falls back to ratio-based split when not enough turns are found.
	toCompact := splitBySmartStrategy(messages, cfg.TotalInputTokens, cfg.Ratio, DefaultKeepRecentTurns, LargeToolResultThreshold)
	if len(toCompact) == 0 {
		s.completeLog(ctx, logID, "ok", "", "", 0, nil, pgtype.UUID{})
		return nil
	}

	// Log pre-compaction statistics: role breakdown and estimated token count.
	var preUser, preAssistant, preTool, preTokens int
	for _, m := range toCompact {
		preTokens += estimateRowTokens(m)
		switch strings.ToLower(strings.TrimSpace(m.Role)) {
		case "user":
			preUser++
		case "assistant":
			preAssistant++
		case "tool":
			preTool++
		}
	}
	s.logger.Info("compaction starting",
		slog.String("bot_id", cfg.BotID),
		slog.String("session_id", cfg.SessionID),
		slog.Int("total_uncompacted", len(messages)),
		slog.Int("to_compact", len(toCompact)),
		slog.Int("pre_user_msgs", preUser),
		slog.Int("pre_assistant_msgs", preAssistant),
		slog.Int("pre_tool_msgs", preTool),
		slog.Int("pre_estimated_tokens", preTokens),
	)

	priorLogs, err := s.queries.ListCompactionLogsBySession(ctx, sessionUUID)
	if err != nil {
		return err
	}
	var priorSummaries []string
	for _, l := range priorLogs {
		if l.Summary != "" {
			priorSummaries = append(priorSummaries, l.Summary)
		}
	}

	entries := make([]messageEntry, 0, len(toCompact))
	messageIDs := make([]pgtype.UUID, 0, len(toCompact))
	for _, m := range toCompact {
		entries = append(entries, messageEntry{
			Role:    m.Role,
			Content: extractTextContent(m.Content),
		})
		messageIDs = append(messageIDs, m.ID)
	}

	userPrompt := buildUserPrompt(priorSummaries, entries)

	model := models.NewSDKChatModel(models.SDKModelConfig{
		ClientType:     cfg.ClientType,
		BaseURL:        cfg.BaseURL,
		APIKey:         cfg.APIKey,
		CodexAccountID: cfg.CodexAccountID,
		ModelID:        cfg.ModelID,
		HTTPClient:     cfg.HTTPClient,
	})

	result, err := sdk.GenerateTextResult(ctx,
		sdk.WithModel(model),
		sdk.WithSystem(systemPrompt),
		sdk.WithMessages([]sdk.Message{sdk.UserMessage(userPrompt)}),
	)
	if err != nil {
		return err
	}

	usageJSON, _ := json.Marshal(result.Usage)

	modelUUID := db.ParseUUIDOrEmpty(cfg.ModelID)

	if err := s.queries.MarkMessagesCompacted(ctx, sqlc.MarkMessagesCompactedParams{
		CompactID: logID,
		Column2:   messageIDs,
	}); err != nil {
		return err
	}

	s.completeLog(ctx, logID, "ok", result.Text, "", len(toCompact), usageJSON, modelUUID)

	// Log post-compaction statistics: summary size and token savings.
	summaryTokens := len(result.Text) / 4
	savedTokens := preTokens - summaryTokens
	if savedTokens < 0 {
		savedTokens = 0
	}
	s.logger.Info("compaction completed",
		slog.String("bot_id", cfg.BotID),
		slog.String("session_id", cfg.SessionID),
		slog.Int("compacted_msgs", len(toCompact)),
		slog.Int("pre_estimated_tokens", preTokens),
		slog.Int("summary_chars", len(result.Text)),
		slog.Int("summary_estimated_tokens", summaryTokens),
		slog.Int("saved_estimated_tokens", savedTokens),
	)

	return nil
}

func (s *Service) completeLog(ctx context.Context, logID pgtype.UUID, status, summary, errMsg string, msgCount int, usage []byte, modelID pgtype.UUID) {
	if _, err := s.queries.CompleteCompactionLog(ctx, sqlc.CompleteCompactionLogParams{
		ID:           logID,
		Status:       status,
		Summary:      summary,
		MessageCount: int32(msgCount), //nolint:gosec // msgCount is len() of a slice, always fits in int32
		ErrorMessage: errMsg,
		Usage:        usage,
		ModelID:      modelID,
	}); err != nil {
		s.logger.Error("failed to complete compaction log", slog.String("error", err.Error()))
	}
}

// ListLogs returns paginated compaction logs for a bot.
func (s *Service) ListLogs(ctx context.Context, botID string, limit, offset int) ([]Log, int64, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, 0, err
	}

	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	total, err := s.queries.CountCompactionLogsByBot(ctx, botUUID)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.queries.ListCompactionLogsByBot(ctx, sqlc.ListCompactionLogsByBotParams{
		BotID:  botUUID,
		Limit:  int32(limit),  //nolint:gosec // clamped above
		Offset: int32(offset), //nolint:gosec // validated above
	})
	if err != nil {
		return nil, 0, err
	}

	logs := make([]Log, len(rows))
	for i, r := range rows {
		logs[i] = toLog(r)
	}
	return logs, total, nil
}

// DeleteLogs deletes all compaction logs for a bot.
func (s *Service) DeleteLogs(ctx context.Context, botID string) error {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	return s.queries.DeleteCompactionLogsByBot(ctx, botUUID)
}

func toLog(r sqlc.BotHistoryMessageCompact) Log {
	l := Log{
		ID:           formatUUID(r.ID),
		BotID:        formatUUID(r.BotID),
		SessionID:    formatUUID(r.SessionID),
		Status:       r.Status,
		Summary:      r.Summary,
		MessageCount: int(r.MessageCount),
		ErrorMessage: r.ErrorMessage,
		ModelID:      formatUUID(r.ModelID),
		StartedAt:    r.StartedAt.Time,
	}
	if r.CompletedAt.Valid {
		t := r.CompletedAt.Time
		l.CompletedAt = &t
	}
	if len(r.Usage) > 0 {
		var u any
		if json.Unmarshal(r.Usage, &u) == nil {
			l.Usage = u
		}
	}
	return l
}

func formatUUID(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return uuid.UUID(id.Bytes).String()
}

// extractTextContent extracts plain text from a message content JSONB field.
// The content may be a JSON string, an array of content parts, or raw bytes.
func extractTextContent(content []byte) string {
	if len(content) == 0 {
		return ""
	}

	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}

	var parts []map[string]any
	if json.Unmarshal(content, &parts) == nil {
		var texts []string
		for _, p := range parts {
			if t, ok := p["type"].(string); ok && t == "text" {
				if text, ok := p["text"].(string); ok {
					texts = append(texts, text)
				}
			}
		}
		if len(texts) > 0 {
			return joinTexts(texts)
		}
	}

	return string(content)
}

func joinTexts(parts []string) string {
	return strings.Join(parts, " ")
}

// --- Token estimation ---

type usagePayload struct {
	InputTokens  *int `json:"input_tokens"`
	OutputTokens *int `json:"output_tokens"`
}

// estimateRowTokens estimates the token count for a message row.
// Uses input + output tokens from stored usage data when available,
// falling back to a character-based heuristic.
func estimateRowTokens(m sqlc.ListUncompactedMessagesBySessionRow) int {
	if len(m.Usage) > 0 {
		var u usagePayload
		if json.Unmarshal(m.Usage, &u) == nil {
			total := 0
			if u.InputTokens != nil && *u.InputTokens > 0 {
				total += *u.InputTokens
			}
			if u.OutputTokens != nil && *u.OutputTokens > 0 {
				total += *u.OutputTokens
			}
			if total > 0 {
				return total
			}
		}
	}
	return len(m.Content) / 4
}

// --- Message classification ---

// messageCostCategory classifies messages by compaction priority.
// Higher values are compacted sooner.
type messageCostCategory int

const (
	costKeep      messageCostCategory = iota // user messages — keep as long as possible
	costNormal                               // assistant text responses
	costHigh                                 // assistant tool calls
	costExpensive                            // tool results (often large JSON)
	costHuge                                 // very large tool results (above threshold)
)

func classifyMessage(m sqlc.ListUncompactedMessagesBySessionRow) messageCostCategory {
	role := strings.TrimSpace(m.Role)
	switch strings.ToLower(role) {
	case "user":
		return costKeep
	case "tool":
		return costExpensive
	case "assistant":
		var parsed []map[string]any
		if json.Unmarshal(m.Content, &parsed) == nil {
			for _, part := range parsed {
				if t, ok := part["type"].(string); ok && (t == "tool_use" || t == "function") {
					return costHigh
				}
			}
		}
		return costNormal
	default:
		return costNormal
	}
}

// --- Splitting strategies ---

// splitByRatio splits messages so that roughly the first ratio% (by token weight)
// are returned for compaction, and the rest are kept as-is.
// When ratio >= 100 or totalInputTokens <= 0, all messages are returned.
func splitByRatio(messages []sqlc.ListUncompactedMessagesBySessionRow, totalInputTokens, ratio int) []sqlc.ListUncompactedMessagesBySessionRow {
	if ratio >= 100 || ratio <= 0 || totalInputTokens <= 0 || len(messages) == 0 {
		return messages
	}

	keepTokens := totalInputTokens * (100 - ratio) / 100
	if keepTokens <= 0 {
		return messages
	}

	accumulated := 0
	cutoff := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		accumulated += estimateRowTokens(messages[i])
		if accumulated >= keepTokens {
			cutoff = i + 1
			break
		}
	}

	if cutoff <= 0 {
		return nil
	}
	if cutoff >= len(messages) {
		return messages
	}
	return messages[:cutoff]
}

// splitBySmartStrategy compacts messages using a priority-based approach:
//  1. Always preserve the most recent keepRecentTurns complete conversation turns
//     (a turn starts with a user message).
//  2. Among older messages, prefer compacting expensive content
//     (tool results > tool calls > assistant text > user messages).
//  3. Tool results larger than largeToolResultThreshold tokens get boosted priority.
//  4. Falls back to splitByRatio when fewer than 2 distinct turns are found.
func splitBySmartStrategy(messages []sqlc.ListUncompactedMessagesBySessionRow, totalInputTokens, ratio, keepRecentTurns, largeToolResultThreshold int) []sqlc.ListUncompactedMessagesBySessionRow {
	if len(messages) == 0 || totalInputTokens <= 0 {
		return messages
	}

	// Step 1: Find the boundary of the most recent keepRecentTurns complete turns.
	recentBoundary := len(messages)
	turnsFound := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(strings.TrimSpace(messages[i].Role), "user") {
			turnsFound++
			recentBoundary = i
			if turnsFound >= keepRecentTurns {
				break
			}
		}
	}

	// Not enough distinct turns found — fall back to ratio-based splitting.
	if turnsFound < 2 {
		return splitByRatio(messages, totalInputTokens, ratio)
	}

	// All messages are recent enough — nothing to compact.
	if recentBoundary <= 0 {
		return nil
	}

	// Step 2: Classify older messages and sort by compaction priority.
	type indexedMsg struct {
		idx      int
		category messageCostCategory
		tokens   int
	}

	older := make([]indexedMsg, 0, recentBoundary)
	for i := 0; i < recentBoundary; i++ {
		tokens := estimateRowTokens(messages[i])
		cat := classifyMessage(messages[i])
		if cat == costExpensive && tokens > largeToolResultThreshold {
			cat = costHuge // boost very large tool results
		}
		older = append(older, indexedMsg{idx: i, category: cat, tokens: tokens})
	}

	// Sort: highest category first (most expensive), then oldest first.
	sort.Slice(older, func(i, j int) bool {
		if older[i].category != older[j].category {
			return older[i].category > older[j].category
		}
		return older[i].idx < older[j].idx
	})

	// Step 3: Select messages to compact, targeting ~50% of older portion tokens.
	olderTokens := 0
	for _, m := range older {
		olderTokens += m.tokens
	}
	targetCompactTokens := olderTokens / 2

	compactIndices := make(map[int]bool)
	accumulated := 0
	for _, m := range older {
		if accumulated >= targetCompactTokens {
			break
		}
		compactIndices[m.idx] = true
		accumulated += m.tokens
	}

	// Collect in original order.
	var result []sqlc.ListUncompactedMessagesBySessionRow
	for i := 0; i < recentBoundary; i++ {
		if compactIndices[i] {
			result = append(result, messages[i])
		}
	}

	return result
}
