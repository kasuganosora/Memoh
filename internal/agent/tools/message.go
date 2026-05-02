package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/channel"
	"github.com/memohai/memoh/internal/expression"
	"github.com/memohai/memoh/internal/messaging"
)

// ReplyerConfigProvider is called per-request to determine whether
// the replyer should replace the send tool. The function receives
// the bot ID and returns (enabled, replyerModelID).
// replyerModelID may be empty, in which case the chat model should be used.
type ReplyerConfigProvider func(ctx context.Context, botID string) (enabled bool, modelID string)

// TextGenerator is the interface for replyer LLM text generation.
// It takes a system prompt and user message and returns generated text.
type TextGenerator interface {
	GenerateText(ctx context.Context, systemPrompt string, messages []sdk.Message) (string, error)
}

// replyerSystemPrompt is the default system prompt for the replyer LLM.
// This is a built-in fallback; the embedded prompt file (
// internal/agent/prompts/system_replyer.md) is preferred when available.
const replyerSystemPrompt = `You are a **replyer** — your job is to turn the bot's internal reasoning into a
natural, conversational reply. Read the chat history and the reasoning below,
then produce ONLY the reply text.

Rules:
- Write as a casual human, not a bot
- NO markdown, NO bullet points, NO JSON, NO code blocks
- NO parentheses, colons, or @ mentions  
- Match the tone and energy of the conversation
- Keep it short unless context demands detail
- Output ONLY the message content — nothing else`

// MessageProvider supplies the send (or reply) and react tools for the agent.
// When replyer is enabled for a discuss-mode session, the reply tool replaces
// send — the LLM writes a reasoning summary, and the replyer (a small LLM call)
// polishes it into natural conversational language before delivery.
type MessageProvider struct {
	exec               *messaging.Executor
	textGen            TextGenerator         // LLM caller for replyer re-generation
	replyerConfigCheck ReplyerConfigProvider // per-request config lookup
	replyerPrompt      string                // system prompt for replyer LLM
	exprSelector       *expression.Selector  // optional: injects learned style into replyer
	logger             *slog.Logger
}

// NewMessageProvider creates a MessageProvider.
// Use SetTextGenerator and SetReplyerConfigCheck after construction to enable replyer.
func NewMessageProvider(log *slog.Logger, sender messaging.Sender, reactor messaging.Reactor, resolver messaging.ChannelTypeResolver, assetResolver messaging.AssetResolver) *MessageProvider {
	if log == nil {
		log = slog.Default()
	}
	return &MessageProvider{
		exec: &messaging.Executor{
			Sender:        sender,
			Reactor:       reactor,
			Resolver:      resolver,
			AssetResolver: assetResolver,
			Logger:        log.With(slog.String("tool", "message")),
		},
		logger: log.With(slog.String("tool", "message")),
	}
}

// SetTextGenerator injects the replyer LLM caller. When nil, replyer is disabled.
func (p *MessageProvider) SetTextGenerator(gen TextGenerator) {
	p.textGen = gen
}

// SetReplyerConfigCheck injects the per-request replyer config checker. When nil, replyer is disabled.
func (p *MessageProvider) SetReplyerConfigCheck(check ReplyerConfigProvider) {
	p.replyerConfigCheck = check
}

// SetReplyerSystemPrompt injects the system prompt for the replyer LLM.
// When empty, the built-in fallback constant (replyerSystemPrompt) is used.
func (p *MessageProvider) SetReplyerSystemPrompt(prompt string) {
	p.replyerPrompt = prompt
}

// SetExpressionSelector injects an optional expression selector for style injection
// into the replyer. When nil (default), style injection is skipped.
func (p *MessageProvider) SetExpressionSelector(sel *expression.Selector) {
	p.exprSelector = sel
}

func (p *MessageProvider) Tools(ctx context.Context, session SessionContext) ([]sdk.Tool, error) {
	if session.IsSubagent {
		return nil, nil
	}
	var tools []sdk.Tool
	sess := session

	useReplyer := p.shouldUseReplyer(ctx, session)

	if p.exec.CanSend() {
		if useReplyer {
			tools = append(tools, p.replyTool(sess))
		} else {
			tools = append(tools, p.sendTool(sess))
		}
	}
	if p.exec.CanReact() {
		tools = append(tools, p.reactTool(sess))
	}
	return tools, nil
}

func (p *MessageProvider) shouldUseReplyer(ctx context.Context, session SessionContext) bool {
	if p.textGen == nil || p.replyerConfigCheck == nil {
		return false
	}
	if session.SessionType != "discuss" {
		return false
	}
	enabled, _ := p.replyerConfigCheck(ctx, session.BotID)
	return enabled
}

func (p *MessageProvider) sendTool(session SessionContext) sdk.Tool {
	return sdk.Tool{
		Name:        "send",
		Description: "Send a message, file, or attachment. When target is omitted, delivers to the current conversation as an inline attachment/message. When target is specified, sends to that channel/person. Supported platforms: telegram, discord, qq, matrix, feishu, wecom, dingtalk, wechatoa, weixin, misskey, web.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"bot_id":      map[string]any{"type": "string", "description": "Bot ID, optional and defaults to current bot"},
				"platform":    map[string]any{"type": "string", "description": "Channel platform name (telegram, discord, qq, matrix, feishu, wecom, dingtalk, wechatoa, weixin, misskey, web). Defaults to current session platform."},
				"target":      map[string]any{"type": "string", "description": "Channel target (chat/group/thread ID). Optional — omit to send in the current conversation. Use get_contacts to find targets for other conversations."},
				"text":        map[string]any{"type": "string", "description": "Message text shortcut when message object is omitted"},
				"reply_to":    map[string]any{"type": "string", "description": "Message ID to reply to. The reply will reference this message on the platform."},
				"attachments": map[string]any{"type": "array", "description": "File paths or URLs to attach.", "items": map[string]any{"type": "string"}},
				"message":     map[string]any{"type": "object", "description": "Structured message payload with text/parts/attachments"},
			},
			"required": []string{},
		},
		Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
			return p.execSend(ctx.Context, session, inputAsMap(input))
		},
	}
}

func (p *MessageProvider) replyTool(session SessionContext) sdk.Tool {
	return sdk.Tool{
		Name:        "reply",
		Description: "Generate and send a visible reply. Provide your reasoning summary; it will be polished into natural conversational language by a replyer. Use this instead of send.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reasoning": map[string]any{"type": "string", "description": "Brief summary of your analysis and what you want to express"},
				"reply_to":  map[string]any{"type": "string", "description": "Message ID to reply to (optional)"},
			},
			"required": []string{"reasoning"},
		},
		Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
			return p.execReply(ctx.Context, session, inputAsMap(input))
		},
	}
}

func (p *MessageProvider) reactTool(session SessionContext) sdk.Tool {
	return sdk.Tool{
		Name:        "react",
		Description: "Add or remove an emoji reaction on a message. When target/platform are omitted, reacts in the current conversation.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"bot_id":     map[string]any{"type": "string", "description": "Bot ID, optional and defaults to current bot"},
				"platform":   map[string]any{"type": "string", "description": "Channel platform name. Defaults to current session platform."},
				"target":     map[string]any{"type": "string", "description": "Channel target (chat/group ID). Defaults to current session reply target."},
				"message_id": map[string]any{"type": "string", "description": "The message ID to react to"},
				"emoji":      map[string]any{"type": "string", "description": "Emoji to react with (e.g. 👍, ❤️). Required when adding a reaction."},
				"remove":     map[string]any{"type": "boolean", "description": "If true, remove the reaction instead of adding it. Default false."},
			},
			"required": []string{"message_id"},
		},
		Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
			return p.execReact(ctx.Context, session, inputAsMap(input))
		},
	}
}

func (p *MessageProvider) execSend(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	// Per-turn send limit: prevent the LLM from calling send/reply/speak excessively.
	if err := CheckSendLimit(session); err != nil {
		return nil, err
	}

	result, err := p.exec.Send(ctx, toMessagingSession(session), args)
	if err != nil {
		return nil, err
	}
	// Discuss mode: same-conversation sends must go through the channel adapter
	// directly — there is no active stream to emit into.
	if result.Local && session.SessionType == "discuss" {
		sendResult, err := p.exec.SendDirect(ctx, toMessagingSession(session), result.Target, args)
		if err != nil {
			return p.handleSendError(err)
		}
		resp := map[string]any{
			"ok": true, "bot_id": sendResult.BotID, "platform": sendResult.Platform, "target": sendResult.Target,
			"delivered": "current_conversation",
		}
		if sendResult.MessageID != "" {
			resp["message_id"] = sendResult.MessageID
		}
		return resp, nil
	}
	if result.Local && session.Emitter != nil {
		atts := channelAttachmentsToToolAttachments(result.LocalAttachments)
		if len(atts) > 0 {
			session.Emitter(ToolStreamEvent{
				Type:        StreamEventAttachment,
				Attachments: atts,
			})
		}
		resp := map[string]any{
			"ok":          true,
			"delivered":   "current_conversation",
			"attachments": len(atts),
		}
		if result.MessageID != "" {
			resp["message_id"] = result.MessageID
		}
		return resp, nil
	}
	resp := map[string]any{
		"ok": true, "bot_id": result.BotID, "platform": result.Platform, "target": result.Target,
	}
	if result.MessageID != "" {
		resp["message_id"] = result.MessageID
	}
	return resp, nil
}

// handleSendError translates outbound pipeline errors into LLM-friendly
// tool results. Dedup and rate-limit hits are returned as successful tool
// calls with explicit "do not retry" instructions so the LLM stops sending.
func (*MessageProvider) handleSendError(err error) (any, error) {
	switch {
	case errors.Is(err, channel.ErrOutboundDedup):
		return map[string]any{
			"ok":    false,
			"error": "message suppressed as duplicate — identical content was just sent to this target, do not retry",
		}, nil
	case errors.Is(err, channel.ErrOutboundRateLimit):
		return map[string]any{
			"ok":    false,
			"error": "outbound rate limit exceeded — stop sending and wait",
		}, nil
	default:
		return nil, err
	}
}

func channelAttachmentsToToolAttachments(atts []channel.Attachment) []Attachment {
	if len(atts) == 0 {
		return nil
	}
	result := make([]Attachment, 0, len(atts))
	for _, a := range atts {
		result = append(result, Attachment{
			Type:        string(a.Type),
			URL:         a.URL,
			Mime:        a.Mime,
			Name:        a.Name,
			ContentHash: a.ContentHash,
			Size:        a.Size,
			Metadata:    a.Metadata,
		})
	}
	return result
}

func (p *MessageProvider) execReact(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	// Check same-conversation before delegating to executor.
	platform := FirstStringArg(args, "platform")
	if platform == "" {
		platform = strings.TrimSpace(session.CurrentPlatform)
	}
	target := FirstStringArg(args, "target")
	if target == "" {
		target = strings.TrimSpace(session.ReplyTarget)
	}
	if session.IsSameConversation(platform, target) && session.Emitter != nil {
		messageID := FirstStringArg(args, "message_id")
		emoji := FirstStringArg(args, "emoji")
		remove, _, _ := BoolArg(args, "remove")
		if messageID == "" {
			return nil, nil
		}
		session.Emitter(ToolStreamEvent{
			Type: StreamEventReaction,
			Reactions: []Reaction{{
				Emoji:     emoji,
				MessageID: messageID,
				Remove:    remove,
			}},
		})
		action := "added"
		if remove {
			action = "removed"
		}
		return map[string]any{
			"ok": true, "emoji": emoji, "action": action,
			"delivered": "current_conversation",
		}, nil
	}
	result, err := p.exec.React(ctx, toMessagingSession(session), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "bot_id": result.BotID, "platform": result.Platform,
		"target": result.Target, "message_id": result.MessageID, "emoji": result.Emoji, "action": result.Action,
	}, nil
}

// execReply is the reply tool handler — it calls the replyer LLM to
// re-generate reasoning into natural conversational language, then sends it.
func (p *MessageProvider) execReply(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	reasoning := StringArg(args, "reasoning")
	replyTo := StringArg(args, "reply_to")

	replyText, err := p.generateReply(ctx, session, reasoning)
	if err != nil {
		// Fallback: send the raw reasoning text directly
		replyText = reasoning
		p.logger.Warn("replyer generation failed, falling back to raw reasoning",
			slog.String("bot_id", session.BotID),
			slog.Any("error", err),
		)
	}

	// Use the existing send path with the reply text
	sendArgs := map[string]any{"text": replyText}
	if replyTo != "" {
		sendArgs["reply_to"] = replyTo
	}
	return p.execSend(ctx, session, sendArgs)
}

// generateReply calls the replyer LLM to turn reasoning into conversational text.
func (p *MessageProvider) generateReply(ctx context.Context, session SessionContext, reasoning string) (string, error) {
	if p.textGen == nil {
		return "", errors.New("replyer text generator not available")
	}

	// Prefer the injected prompt (from embedded prompts/), fall back to built-in constant.
	systemPrompt := p.replyerPrompt
	if systemPrompt == "" {
		systemPrompt = replyerSystemPrompt
	}

	// Inject learned expression style reference into the system prompt.
	if p.exprSelector != nil {
		entries, err := p.exprSelector.Select(ctx, session.BotID, reasoning, 3)
		if err == nil && len(entries) > 0 {
			styleRef := expression.FormatStyleReference(entries)
			systemPrompt += styleRef
		}
	}

	result, err := p.textGen.GenerateText(ctx, systemPrompt, []sdk.Message{
		sdk.UserMessage(reasoning),
	})
	if err != nil {
		return "", fmt.Errorf("replyer generate: %w", err)
	}
	return strings.TrimSpace(result), nil
}

func toMessagingSession(s SessionContext) messaging.SessionContext {
	return messaging.SessionContext{
		BotID:           s.BotID,
		ChatID:          s.ChatID,
		CurrentPlatform: s.CurrentPlatform,
		ReplyTarget:     s.ReplyTarget,
	}
}
