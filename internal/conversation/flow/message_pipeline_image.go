package flow

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/providers"
)

// ---------------------------------------------------------------------------
// Processor
// ---------------------------------------------------------------------------

// ImageProcessorDeps holds the dependencies for the image description
// processor. All deps are explicit so the processor can be tested in
// isolation without a Resolver.
type ImageProcessorDeps struct {
	VisionModelID      string
	FetchModel         func(ctx context.Context, modelID string) (ModelProvider, error)
	ResolveCredentials func(ctx context.Context, provider Provider) (Credentials, error)
	HTTPClient         *http.Client
	Logger             *slog.Logger
}

// imageDescriptionProcessor describes images through a vision-capable
// model when the main model does not support images natively.
type imageDescriptionProcessor struct {
	deps ImageProcessorDeps
}

// NewImageDescriptionProcessor creates a processor that describes images
// using a vision-capable fallback model. All dependencies are injected
// via deps so the processor can be independently tested.
func NewImageDescriptionProcessor(deps ImageProcessorDeps) MessageProcessor {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &imageDescriptionProcessor{deps: deps}
}

func (*imageDescriptionProcessor) Name() string { return "image_description" }

func (p *imageDescriptionProcessor) Process(ctx context.Context, state *MessageState) error {
	if p.deps.VisionModelID == "" || len(state.InlineImages) == 0 {
		return nil
	}

	desc, err := DescribeImagesWithVisionModel(ctx, p.deps.VisionModelID, state.InlineImages, p.deps.FetchModel, p.deps.ResolveCredentials, p.deps.HTTPClient)
	if err != nil {
		return err
	}
	if desc == "" {
		return nil
	}

	if state.Query != "" {
		state.Query = desc + "\n\n" + state.Query
	} else {
		state.Query = desc
	}
	state.InlineImages = nil

	p.deps.Logger.Info("image description complete",
		slog.String("bot_id", state.BotID),
	)

	return nil
}

// ---------------------------------------------------------------------------
// Standalone image description function (no Resolver dependency)
// ---------------------------------------------------------------------------

// DescribeImagesWithVisionModel forwards images to a multimodal model
// and returns a detailed text description. All external dependencies
// are injected so this function can be tested independently.
func DescribeImagesWithVisionModel(
	ctx context.Context,
	modelID string,
	images []sdk.ImagePart,
	fetchModel func(ctx context.Context, modelID string) (ModelProvider, error),
	resolveCredentials func(ctx context.Context, provider Provider) (Credentials, error),
	httpClient *http.Client,
) (string, error) {
	// Filter empty images first — avoids resolving the model when there
	// are no actual images to describe.
	imageParts := make([]sdk.MessagePart, 0, len(images))
	for _, img := range images {
		if strings.TrimSpace(img.Image) != "" {
			imageParts = append(imageParts, img)
		}
	}
	if len(imageParts) == 0 {
		return "", nil
	}

	modelProvider, err := fetchModel(ctx, modelID)
	if err != nil {
		return "", fmt.Errorf("resolve vision model: %w", err)
	}

	creds, err := resolveCredentials(ctx, Provider{
		ID:         modelProvider.ProviderID,
		ClientType: modelProvider.ClientType,
	})
	if err != nil {
		return "", fmt.Errorf("resolve vision model credentials: %w", err)
	}

	sdkModel := models.NewSDKChatModel(models.SDKModelConfig{
		ModelID:        modelProvider.ModelID,
		ClientType:     modelProvider.ClientType,
		APIKey:         creds.APIKey,
		CodexAccountID: creds.CodexAccountID,
		BaseURL:        modelProvider.BaseURL,
		HTTPClient:     httpClient,
	})

	prompt := "Please describe this image in detail. Include all visible objects, people, text, colors, layout, and any other relevant information that would help someone understand this image without seeing it."

	genCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	client := sdk.NewClient()
	text, err := client.GenerateText(genCtx,
		sdk.WithModel(sdkModel),
		sdk.WithMessages([]sdk.Message{
			sdk.UserMessage(prompt, imageParts...),
		}),
	)
	if err != nil {
		return "", fmt.Errorf("vision model call: %w", err)
	}

	return strings.TrimSpace(text), nil
}

// ---------------------------------------------------------------------------
// Pipeline builder (standalone)
// ---------------------------------------------------------------------------

// VisionConfig holds the vision-related configuration extracted from a
// RunConfig. Decoupled so the pipeline builder doesn't depend on RunConfig.
type VisionConfig struct {
	SupportsVision bool
	VisionModelID  string
}

// BuildMessagePipeline constructs the ordered list of message processors
// based on the current configuration. This is a standalone function so it
// can be tested with any configuration without a Resolver.
//
// New content types are added here as additional processor construction
// branches.
func BuildMessagePipeline(cfg VisionConfig, deps PipelineDeps) []MessageProcessor {
	var processors []MessageProcessor

	if !cfg.SupportsVision && cfg.VisionModelID != "" {
		processors = append(processors, NewImageDescriptionProcessor(ImageProcessorDeps{
			VisionModelID:      cfg.VisionModelID,
			FetchModel:         deps.FetchModel,
			ResolveCredentials: deps.ResolveCredentials,
			HTTPClient:         deps.HTTPClient,
			Logger:             deps.Logger,
		}))
	}

	// Future processors:
	// if !cfg.SupportsAudio && cfg.AudioModelID != "" { ... }
	// if !cfg.SupportsVideo && cfg.VideoModelID != "" { ... }

	return processors
}

// ---------------------------------------------------------------------------
// Resolver adapter (thin glue layer)
// ---------------------------------------------------------------------------

// buildMessagePipeline is a thin adapter on Resolver that translates
// Resolver-scoped types into the standalone pipeline deps.
func (r *Resolver) buildMessagePipeline(cfg VisionConfig) []MessageProcessor {
	deps := PipelineDeps{
		FetchModel: func(ctx context.Context, modelID string) (ModelProvider, error) {
			m, p, err := r.fetchChatModel(ctx, modelID)
			if err != nil {
				return ModelProvider{}, err
			}
			return ModelProvider{
				ModelID:    m.ModelID,
				ClientType: p.ClientType,
				ProviderID: p.ID.String(),
				BaseURL:    providers.ProviderConfigString(p, "base_url"),
			}, nil
		},
		ResolveCredentials: func(ctx context.Context, provider Provider) (Credentials, error) {
			// Fetch the full provider record from DB — the Provider struct from the
			// pipeline only carries ID + ClientType and drops Config (api_key).
			provUUID := db.ParseUUIDOrEmpty(provider.ID)
			fullProvider, err := r.queries.GetProviderByID(ctx, provUUID)
			if err != nil {
				return Credentials{}, fmt.Errorf("resolve provider %s: %w", provider.ID, err)
			}
			authResolver := providers.NewService(nil, r.queries, "")
			creds, err := authResolver.ResolveModelCredentials(ctx, fullProvider)
			if err != nil {
				return Credentials{}, err
			}
			return Credentials{
				APIKey:         creds.APIKey,
				CodexAccountID: creds.CodexAccountID,
			}, nil
		},
		HTTPClient: r.streamHTTPClient,
		Logger:     r.logger,
	}
	return BuildMessagePipeline(cfg, deps)
}
