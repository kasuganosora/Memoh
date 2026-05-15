package flow

import (
	"context"
	"log/slog"
	"strings"

	"github.com/memohai/memoh/internal/conversation"
	memprovider "github.com/memohai/memoh/internal/memory/adapters"
)

func (r *Resolver) resolveMemoryProvider(ctx context.Context, botID string) memprovider.Provider {
	if r.memoryRegistry == nil {
		return nil
	}
	if r.settingsService == nil {
		return nil
	}
	botSettings, err := r.settingsService.GetBot(ctx, botID)
	if err != nil {
		return nil
	}
	providerID := strings.TrimSpace(botSettings.MemoryProviderID)
	if providerID == "" {
		return nil
	}
	p, err := r.memoryRegistry.Get(providerID)
	if err != nil {
		r.logger.Error("memory provider lookup failed", slog.String("provider_id", providerID), slog.Any("error", err))
		return nil
	}
	return p
}

// memoryContextResult holds the partitioned memory context returned by
// loadMemoryContext. AppendSystemContext is stable content (profile, scene
// index) that should be appended to the system prompt for prompt-cache
// friendliness. UserMessage is dynamic per-turn content (recalled memories)
// injected as a user message.
type memoryContextResult struct {
	AppendSystemContext string
	UserMessage         *conversation.ModelMessage
}

// loadMemoryContext retrieves memory context from the configured provider and
// returns it partitioned into system-level and user-level parts.
// It is backward-compatible: when the provider only populates the deprecated
// ContextText field, the result falls back to the old behavior.
func (r *Resolver) loadMemoryContext(ctx context.Context, req conversation.ChatRequest) memoryContextResult {
	p := r.resolveMemoryProvider(ctx, req.BotID)
	if p == nil {
		return memoryContextResult{}
	}
	result, err := p.OnBeforeChat(ctx, memprovider.BeforeChatRequest{
		Query:  req.Query,
		BotID:  req.BotID,
		ChatID: req.ChatID,
		UserID: req.UserID,
	})
	if err != nil {
		r.logger.Error("memory provider OnBeforeChat failed", slog.String("bot_id", req.BotID), slog.Any("error", err))
		return memoryContextResult{}
	}
	if result == nil {
		return memoryContextResult{}
	}

	var out memoryContextResult

	// Prefer new partitioned fields; fall back to deprecated ContextText.
	userCtx := strings.TrimSpace(result.PrependUserContext)
	if userCtx == "" {
		userCtx = strings.TrimSpace(result.ContextText) //nolint:staticcheck // SA1019: intentional fallback for backward compat
	}
	if userCtx != "" {
		out.UserMessage = &conversation.ModelMessage{
			Role:    "user",
			Content: conversation.NewTextContent(userCtx),
		}
	}

	out.AppendSystemContext = strings.TrimSpace(result.AppendSystemContext)
	return out
}

// loadMemoryContextMessage is a backward-compatible wrapper that returns only
// the user-message part of the memory context. Callers that also need the
// system-level context should use loadMemoryContext directly.
func (r *Resolver) loadMemoryContextMessage(ctx context.Context, req conversation.ChatRequest) *conversation.ModelMessage {
	return r.loadMemoryContext(ctx, req).UserMessage
}

// storeMemoryWithProvider is a variant of storeMemory that accepts a pre-resolved
// Provider. Used from storeRound where the provider is resolved once and shared
// between memory storage and profile update.
func (r *Resolver) storeMemoryWithProvider(ctx context.Context, req conversation.ChatRequest, messages []conversation.ModelMessage, p memprovider.Provider) {
	botID := strings.TrimSpace(req.BotID)
	if botID == "" || p == nil {
		return
	}
	_, tzLoc := r.resolveTimezone(ctx, req.BotID, req.UserID)
	if err := p.OnAfterChat(ctx, memprovider.AfterChatRequest{
		BotID:             botID,
		Messages:          toProviderMessages(messages),
		UserID:            strings.TrimSpace(req.UserID),
		ChannelIdentityID: strings.TrimSpace(req.SourceChannelIdentityID),
		DisplayName:       r.resolveDisplayName(ctx, req),
		TimezoneLocation:  tzLoc,
		SourcePlatform:    strings.TrimSpace(req.CurrentChannel),
		SourceSessionID:   strings.TrimSpace(req.SessionID),
	}); err != nil {
		r.logger.Error("memory provider OnAfterChat failed", slog.String("bot_id", botID), slog.Any("error", err))
	}
}

// updateProfile runs asynchronous user profile extraction from the current round
// of messages. It reuses the pre-resolved memory provider for search queries.
func (r *Resolver) updateProfile(ctx context.Context, req conversation.ChatRequest, messages []conversation.ModelMessage, p memprovider.Provider) {
	if r.profileService == nil || p == nil {
		return
	}
	userID := strings.TrimSpace(req.UserID)
	if userID == "" {
		return
	}
	providerMsgs := toProviderMessages(messages)
	if len(providerMsgs) == 0 {
		return
	}
	if err := r.profileService.UpdateFromMessages(ctx, req.BotID, userID, providerMsgs); err != nil {
		r.logger.Warn("profile update failed", slog.String("bot_id", req.BotID), slog.Any("error", err))
	}
}

func toProviderMessages(messages []conversation.ModelMessage) []memprovider.Message {
	out := make([]memprovider.Message, 0, len(messages))
	for _, msg := range messages {
		text := strings.TrimSpace(msg.TextContent())
		if text == "" {
			continue
		}
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "assistant"
		}
		out = append(out, memprovider.Message{Role: role, Content: text})
	}
	return out
}
