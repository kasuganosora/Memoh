package flow

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/providers"
)

// imageDescriptionProcessor describes images through a vision-capable
// model when the main model does not support images natively.
type imageDescriptionProcessor struct {
	visionModelID string
	describer     func(ctx context.Context, images []sdk.ImagePart) (string, error)
	logger        *slog.Logger
}

func (p *imageDescriptionProcessor) Name() string { return "image_description" }

func (p *imageDescriptionProcessor) Process(ctx context.Context, state *MessageState) error {
	if p.visionModelID == "" || len(state.InlineImages) == 0 {
		return nil
	}

	desc, err := p.describer(ctx, state.InlineImages)
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

	p.logger.Info("image description complete",
		slog.String("bot_id", state.BotID),
	)

	return nil
}

// buildMessagePipeline constructs the ordered list of message processors
// based on the current run configuration. New content types can be added
// here as additional processors.
func (r *Resolver) buildMessagePipeline(cfg VisionConfig) []MessageProcessor {
	var processors []MessageProcessor

	if !cfg.SupportsVision && cfg.VisionModelID != "" {
		processors = append(processors, &imageDescriptionProcessor{
			visionModelID: cfg.VisionModelID,
			describer: func(ctx context.Context, images []sdk.ImagePart) (string, error) {
				return r.describeImagesWithVisionModel(ctx, cfg.VisionModelID, images)
			},
			logger: r.logger,
		})
	}

	// Future processors:
	// if !cfg.SupportsAudio && cfg.AudioModelID != "" { ... }
	// if !cfg.SupportsVideo && cfg.VideoModelID != "" { ... }

	return processors
}

// VisionConfig holds the vision-related configuration extracted from a
// RunConfig. Decoupled from RunConfig so the pipeline builder doesn't
// depend on the full config struct.
type VisionConfig struct {
	SupportsVision bool
	VisionModelID  string
}

// describeImagesWithVisionModel forwards images to a multimodal model
// and returns a detailed text description.
func (r *Resolver) describeImagesWithVisionModel(ctx context.Context, modelID string, images []sdk.ImagePart) (string, error) {
	visionModel, provider, err := r.fetchChatModel(ctx, modelID)
	if err != nil {
		return "", fmt.Errorf("resolve vision model: %w", err)
	}

	authResolver := providers.NewService(nil, r.queries, "")
	creds, err := authResolver.ResolveModelCredentials(ctx, provider)
	if err != nil {
		return "", fmt.Errorf("resolve vision model credentials: %w", err)
	}

	sdkModel := models.NewSDKChatModel(models.SDKModelConfig{
		ModelID:        visionModel.ModelID,
		ClientType:     provider.ClientType,
		APIKey:         creds.APIKey,
		CodexAccountID: creds.CodexAccountID,
		BaseURL:        providers.ProviderConfigString(provider, "base_url"),
		HTTPClient:     r.streamHTTPClient,
	})

	imageParts := make([]sdk.MessagePart, 0, len(images))
	for _, img := range images {
		if strings.TrimSpace(img.Image) != "" {
			imageParts = append(imageParts, img)
		}
	}
	if len(imageParts) == 0 {
		return "", nil
	}

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
