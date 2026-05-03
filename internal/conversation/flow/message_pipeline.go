package flow

import (
	"context"
	"log/slog"
	"net/http"

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

// PipelineDeps holds all external dependencies needed to build message
// processors. Extracted from Resolver so processors can be constructed
// and tested independently.
type PipelineDeps struct {
	// FetchModel resolves a model by ID (UUID or slug) and returns
	// its metadata and provider information.
	FetchModel func(ctx context.Context, modelID string) (ModelProvider, error)

	// ResolveCredentials returns provider API credentials given a provider.
	ResolveCredentials func(ctx context.Context, provider Provider) (Credentials, error)

	// HTTPClient is used for LLM provider HTTP calls. Inject a custom
	// *http.Client with mock transport for testing.
	HTTPClient *http.Client

	Logger *slog.Logger
}

// ModelProvider pairs model metadata with its provider for LLM calls.
type ModelProvider struct {
	ModelID    string // The model identifier (e.g. "gpt-4o")
	ClientType string // Provider client type (e.g. "openai-completions")
	ProviderID string // Provider record UUID for credential lookup
	BaseURL    string // Provider base URL for custom endpoints (empty = SDK default)
}

// Provider holds provider identifier needed for credential resolution.
type Provider struct {
	ID         string
	ClientType string
}

// Credentials holds the API key and optional account identifier for
// authenticating LLM provider calls.
type Credentials struct {
	APIKey         string
	CodexAccountID string
}
