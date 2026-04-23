package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/db/sqlc"
	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/providers"
	"github.com/memohai/memoh/internal/settings"
	"github.com/memohai/memoh/internal/workspace/bridge"
)

const imageGenDir = "/data/generated-images"

type ImageGenProvider struct {
	logger     *slog.Logger
	settings   *settings.Service
	models     *models.Service
	queries    *sqlc.Queries
	containers bridge.Provider
	dataMount  string
}

func NewImageGenProvider(
	log *slog.Logger,
	settingsSvc *settings.Service,
	modelsSvc *models.Service,
	queries *sqlc.Queries,
	containers bridge.Provider,
	dataMount string,
) *ImageGenProvider {
	if log == nil {
		log = slog.Default()
	}
	return &ImageGenProvider{
		logger:     log.With(slog.String("tool", "image_gen")),
		settings:   settingsSvc,
		models:     modelsSvc,
		queries:    queries,
		containers: containers,
		dataMount:  dataMount,
	}
}

func (p *ImageGenProvider) Tools(ctx context.Context, session SessionContext) ([]sdk.Tool, error) {
	if session.IsSubagent || p.settings == nil || p.models == nil || p.queries == nil {
		return nil, nil
	}
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return nil, nil
	}
	botSettings, err := p.settings.GetBot(ctx, botID)
	if err != nil {
		return nil, nil
	}
	if strings.TrimSpace(botSettings.ImageModelID) == "" {
		return nil, nil
	}
	sess := session
	return []sdk.Tool{
		{
			Name:        "generate_image",
			Description: "Generate an image from a text description using the configured image generation model. Returns the file path of the generated image in the workspace.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prompt": map[string]any{"type": "string", "description": "Detailed description of the image to generate"},
					"size":   map[string]any{"type": "string", "description": "Image size, e.g. 1024x1024, 1792x1024, 1024x1792. Defaults to 1024x1024."},
				},
				"required": []string{"prompt"},
			},
			Execute: func(execCtx *sdk.ToolExecContext, input any) (any, error) {
				return p.execGenerateImage(execCtx.Context, sess, inputAsMap(input))
			},
		},
	}, nil
}

func (p *ImageGenProvider) execGenerateImage(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}
	prompt := strings.TrimSpace(StringArg(args, "prompt"))
	if prompt == "" {
		return nil, errors.New("prompt is required")
	}
	size := strings.TrimSpace(StringArg(args, "size"))
	if size == "" {
		size = "1024x1024"
	}

	botSettings, err := p.settings.GetBot(ctx, botID)
	if err != nil {
		return nil, errors.New("failed to load bot settings")
	}
	imageModelID := strings.TrimSpace(botSettings.ImageModelID)
	if imageModelID == "" {
		return nil, errors.New("no image generation model configured")
	}

	modelResp, err := p.models.GetByID(ctx, imageModelID)
	if err != nil {
		return nil, fmt.Errorf("failed to load image model: %w", err)
	}
	if !modelResp.HasCompatibility(models.CompatImageOutput) {
		return nil, errors.New("configured model does not support image generation")
	}

	provider, err := models.FetchProviderByID(ctx, p.queries, modelResp.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("failed to load model provider: %w", err)
	}

	authResolver := providers.NewService(nil, p.queries, "")
	creds, err := authResolver.ResolveModelCredentials(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve provider credentials: %w", err)
	}

	var result any
	// Dispatch: models with image-api compat use the dedicated /images/generations endpoint.
	if modelResp.HasCompatibility(models.CompatImageApi) {
		result, err = p.execImageAPI(ctx, botID, prompt, modelResp, provider, creds)
	} else {
		result, err = p.execChatImage(ctx, botID, prompt, size, modelResp, provider, creds)
	}
	if err != nil {
		return nil, err
	}

	// Emit the generated image as an attachment event so the channel adapter
	// can deliver it inline (e.g. upload to Misskey Drive). This works for
	// both chat mode (via stream emitter → inbound processor) and discuss
	// mode (via stream emitter → DiscussDriver attachment handler).
	p.emitImageAttachment(session, result)

	return result, nil
}

// emitImageAttachment inspects the image generation result and emits a
// StreamEventAttachment when the session has an active stream emitter.
func (*ImageGenProvider) emitImageAttachment(session SessionContext, result any) {
	if session.Emitter == nil {
		return
	}

	m, ok := result.(map[string]any)
	if !ok {
		return
	}

	var att Attachment

	// Path A: container save succeeded — result has "path" and "media_type".
	if path, _ := m["path"].(string); path != "" {
		att = Attachment{
			Type: "image",
			Path: path,
			Mime: fmt.Sprintf("%v", m["media_type"]),
		}
		if sz, ok := m["size_bytes"].(int); ok {
			att.Size = int64(sz)
		}
	} else {
		// Path B: container unreachable — result has inline base64 "content".
		// The content array contains {"type": "image", "data": "...", "mimeType": "..."}
		content, _ := m["content"].([]map[string]any)
		for _, item := range content {
			if t, _ := item["type"].(string); t == "image" {
				data, _ := item["data"].(string)
				mime, _ := item["mimeType"].(string)
				if data != "" {
					att = Attachment{
						Type: "image",
						URL:  fmt.Sprintf("data:%s;base64,%s", mime, data),
						Mime: mime,
					}
				}
				break
			}
		}
	}

	if att.Type == "" {
		return
	}

	session.Emitter(ToolStreamEvent{
		Type:        StreamEventAttachment,
		Attachments: []Attachment{att},
	})
}

// execChatImage generates an image via the chat-based path (models that return
// images inline in chat responses, e.g. GPT-5 Image, Gemini Flash Image).
func (p *ImageGenProvider) execChatImage(ctx context.Context, botID, prompt, size string, modelResp models.GetResponse, provider sqlc.Provider, creds providers.ModelCredentials) (any, error) {
	sdkModel := models.NewSDKChatModel(models.SDKModelConfig{
		ModelID:    modelResp.ModelID,
		ClientType: provider.ClientType,
		APIKey:     creds.APIKey,
		BaseURL:    providers.ProviderConfigString(provider, "base_url"),
	})

	userMsg := fmt.Sprintf("Generate an image with the following description. Size: %s\n\n%s", size, prompt)
	result, err := sdk.GenerateTextResult(ctx,
		sdk.WithModel(sdkModel),
		sdk.WithMessages([]sdk.Message{
			{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: userMsg}}},
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("image generation failed: %w", err)
	}

	if len(result.Files) == 0 {
		if result.Text != "" {
			return map[string]any{"error": "no image generated", "model_response": result.Text}, nil
		}
		return nil, errors.New("no image was generated by the model")
	}

	file := result.Files[0]
	imgBytes, err := base64.StdEncoding.DecodeString(file.Data)
	if err != nil {
		return nil, fmt.Errorf("failed to decode generated image: %w", err)
	}

	ext := mediaTypeToExt(file.MediaType)
	return p.saveToContainer(ctx, botID, imgBytes, file.Data, ext, file.MediaType)
}

// execImageAPI generates an image via the dedicated /images/generations endpoint
// (e.g. Grok Imagine, DALL-E).
func (p *ImageGenProvider) execImageAPI(ctx context.Context, botID, prompt string, modelResp models.GetResponse, provider sqlc.Provider, creds providers.ModelCredentials) (any, error) {
	imgModel := models.NewSDKImageModel(models.SDKModelConfig{
		ModelID:    modelResp.ModelID,
		ClientType: provider.ClientType,
		APIKey:     creds.APIKey,
		BaseURL:    providers.ProviderConfigString(provider, "base_url"),
	})

	result, err := sdk.GenerateImage(ctx,
		sdk.WithImageGenerationModel(imgModel),
		sdk.WithImagePrompt(prompt),
		sdk.WithImageResponseFormat("b64_json"),
	)
	if err != nil {
		return nil, fmt.Errorf("image generation failed: %w", err)
	}

	if len(result.Data) == 0 {
		return nil, errors.New("no image was generated by the model")
	}

	imgData := result.Data[0]

	// Prefer base64 data; fall back to downloading the URL.
	var imgBytes []byte
	var base64Str string
	switch {
	case imgData.B64JSON != "":
		base64Str = imgData.B64JSON
		imgBytes, err = base64.StdEncoding.DecodeString(imgData.B64JSON)
		if err != nil {
			return nil, fmt.Errorf("failed to decode generated image: %w", err)
		}
	case imgData.URL != "":
		imgBytes, base64Str, err = downloadImage(ctx, imgData.URL)
		if err != nil {
			return nil, fmt.Errorf("failed to download generated image: %w", err)
		}
	default:
		return nil, errors.New("image response contains no data")
	}

	ext := detectImageExt(imgBytes)
	mediaType := "image/png"
	switch ext {
	case "jpg":
		mediaType = "image/jpeg"
	case "webp":
		mediaType = "image/webp"
	}

	return p.saveToContainer(ctx, botID, imgBytes, base64Str, ext, mediaType)
}

// saveToContainer writes the image bytes to the bot's workspace container.
// If the container is unreachable, it falls back to returning inline base64 data.
func (p *ImageGenProvider) saveToContainer(ctx context.Context, botID string, imgBytes []byte, base64Data, ext, mediaType string) (any, error) {
	containerPath := fmt.Sprintf("%s/%d.%s", imageGenDir, time.Now().UnixMilli(), ext)

	client, clientErr := p.containers.MCPClient(ctx, botID)
	if clientErr != nil {
		return map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "Image generated (container not reachable, not saved to disk)"},
				{"type": "image", "data": base64Data, "mimeType": mediaType},
			},
		}, nil
	}

	mkdirCmd := fmt.Sprintf("mkdir -p %s", imageGenDir)
	_, _ = client.Exec(ctx, mkdirCmd, "/", 5)

	if writeErr := client.WriteFile(ctx, containerPath, imgBytes); writeErr != nil {
		return map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": fmt.Sprintf("Image generated (failed to save: %s)", writeErr.Error())},
				{"type": "image", "data": base64Data, "mimeType": mediaType},
			},
		}, nil
	}

	return map[string]any{
		"path":       containerPath,
		"media_type": mediaType,
		"size_bytes": len(imgBytes),
	}, nil
}

// mediaTypeToExt returns a file extension for common image media types.
func mediaTypeToExt(mediaType string) string {
	switch {
	case strings.Contains(mediaType, "jpeg"), strings.Contains(mediaType, "jpg"):
		return "jpg"
	case strings.Contains(mediaType, "webp"):
		return "webp"
	default:
		return "png"
	}
}

// detectImageExt uses magic bytes to determine the image format extension.
func detectImageExt(data []byte) string {
	if len(data) < 4 {
		return "png"
	}
	// JPEG: FF D8 FF
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "jpg"
	}
	// WEBP: RIFF....WEBP
	if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "webp"
	}
	return "png"
}

// downloadImage fetches an image from a URL and returns the raw bytes and base64 encoding.
func downloadImage(ctx context.Context, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download returned status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, "", fmt.Errorf("read body: %w", err)
	}
	return data, base64.StdEncoding.EncodeToString(data), nil
}
