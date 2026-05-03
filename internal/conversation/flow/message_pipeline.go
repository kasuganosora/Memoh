package flow

import (
	"context"
	"log/slog"

	sdk "github.com/memohai/twilight-ai/sdk"
)

// MessageState holds the mutable state of a user message as it flows
// through the processing pipeline. Processors modify fields they care
// about and leave others unchanged.
type MessageState struct {
	// BotID identifies the bot this message belongs to.
	BotID string

	// Query is the user's text content. Processors may prepend
	// additional context (e.g. image descriptions) to this field.
	Query string

	// InlineImages are image parts attached to the message. Processors
	// that handle images (e.g. describing them for text-only models)
	// should clear this field after processing.
	InlineImages []sdk.ImagePart

	// Future fields for extensibility:
	//   InlineFiles   []FilePart    — file attachments
	//   InlineAudio   []AudioPart   — audio clips
	//   InlineVideo   []VideoPart   — video frames
}

// MessageProcessor transforms message content that the main model
// cannot handle natively (e.g. images for a text-only model).
// Each processor handles one content type and can query other models
// or services to produce text representations.
//
// Processors should be idempotent and should NOT modify state they
// don't recognize — this allows chaining multiple processors in a
// pipeline without ordering dependencies.
type MessageProcessor interface {
	// Name returns a human-readable name for logging.
	Name() string

	// Process transforms the message state. Returns an error only
	// when the processor itself fails (connection, timeout, etc.).
	// Content-level decisions (e.g. "no images to process") should
	// return nil without modifying state.
	Process(ctx context.Context, state *MessageState) error
}

// runMessagePipeline executes all processors in order against the
// given state, logging each stage. If a processor returns an error,
// the pipeline stops and returns that error (subsequent processors
// are skipped).
func runMessagePipeline(ctx context.Context, processors []MessageProcessor, state *MessageState, logger *slog.Logger) error {
	for _, p := range processors {
		if err := p.Process(ctx, state); err != nil {
			logger.Warn("message pipeline: processor failed",
				slog.String("processor", p.Name()),
				slog.String("bot_id", state.BotID),
				slog.Any("error", err),
			)
			return err
		}
	}
	return nil
}
