package pipeline

import (
	"context"
	"log/slog"

	"github.com/memohai/memoh/internal/channel"
)

// truncateStr truncates a string to n characters with ellipsis.
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// deliverMessage sends a text message through the channel adapter.
func (d *DiscussTrigger) deliverMessage(
	ctx context.Context,
	cfg DiscussSessionConfig,
	chType channel.ChannelType,
	text, replyTo string,
	log *slog.Logger,
) bool {
	if d.deps.ChannelSender == nil {
		log.Warn("discuss fallback: no channel sender configured")
		return false
	}
	sender, err := d.deps.ChannelSender.GetReplySender(cfg.BotID, chType)
	if err != nil || sender == nil {
		log.Warn("discuss fallback: failed to get reply sender",
			slog.Any("error", err))
		return false
	}

	msg := channel.Message{
		Text:   text,
		Format: channel.MessageFormatPlain,
	}
	if replyTo != "" {
		msg.Reply = &channel.ReplyRef{MessageID: replyTo}
	}

	target := cfg.ReplyTarget
	if err := sender.Send(ctx, channel.OutboundMessage{
		Target:  target,
		Message: msg,
	}); err != nil {
		log.Error("discuss fallback: send failed",
			slog.String("bot_id", cfg.BotID),
			slog.String("target", target),
			slog.Any("error", err))
		return false
	}

	log.Info("discuss fallback: message delivered",
		slog.String("bot_id", cfg.BotID),
		slog.String("platform", cfg.CurrentPlatform),
		slog.String("target", target),
		slog.Int("text_len", len(text)))
	return true
}
