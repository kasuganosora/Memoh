package flow

import (
	"context"
	"errors"
	"net/http"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"
)

func TestBuildMessagePipeline_NoVision(t *testing.T) {
	t.Parallel()

	cfg := VisionConfig{
		SupportsVision: true, // model supports vision → no processor needed
		VisionModelID:  "some-uuid",
	}
	deps := PipelineDeps{HTTPClient: http.DefaultClient}

	processors := BuildMessagePipeline(cfg, deps)
	if len(processors) != 0 {
		t.Fatalf("expected 0 processors when vision is supported, got %d", len(processors))
	}
}

func TestBuildMessagePipeline_TextOnly_WithVisionModel(t *testing.T) {
	t.Parallel()

	cfg := VisionConfig{
		SupportsVision: false,
		VisionModelID:  "gpt-4o-uuid",
	}
	deps := PipelineDeps{HTTPClient: http.DefaultClient}

	processors := BuildMessagePipeline(cfg, deps)
	if len(processors) != 1 {
		t.Fatalf("expected 1 processor for text-only model with vision fallback, got %d", len(processors))
	}
	if processors[0].Name() != "image_description" {
		t.Fatalf("unexpected processor name: %q", processors[0].Name())
	}
}

func TestBuildMessagePipeline_TextOnly_NoVisionModel(t *testing.T) {
	t.Parallel()

	cfg := VisionConfig{
		SupportsVision: false,
		VisionModelID:  "", // no fallback configured
	}
	deps := PipelineDeps{HTTPClient: http.DefaultClient}

	processors := BuildMessagePipeline(cfg, deps)
	if len(processors) != 0 {
		t.Fatalf("expected 0 processors when no vision model configured, got %d", len(processors))
	}
}

func TestImageDescriptionProcessor_NoImages(t *testing.T) {
	t.Parallel()

	p := NewImageDescriptionProcessor(ImageProcessorDeps{
		VisionModelID: "some-id",
		HTTPClient:    http.DefaultClient,
	})

	state := &MessageState{BotID: "bot-1", Query: "hello"}
	err := p.Process(context.Background(), state)
	if err != nil {
		t.Fatalf("expected no error when no images, got: %v", err)
	}
	if state.Query != "hello" {
		t.Fatalf("query should not change when no images: got %q", state.Query)
	}
}

func TestImageDescriptionProcessor_NoVisionModel(t *testing.T) {
	t.Parallel()

	p := NewImageDescriptionProcessor(ImageProcessorDeps{
		VisionModelID: "", // empty → skip
		HTTPClient:    http.DefaultClient,
	})

	state := &MessageState{
		BotID:        "bot-1",
		Query:        "hello",
		InlineImages: []sdk.ImagePart{{Image: "data:image/png;base64,abc"}},
	}
	err := p.Process(context.Background(), state)
	if err != nil {
		t.Fatalf("expected no error when no vision model, got: %v", err)
	}
	if len(state.InlineImages) != 1 {
		t.Fatalf("images should not be cleared when no vision model")
	}
}

func TestImageDescriptionProcessor_Success(t *testing.T) {
	t.Parallel()

	p := NewImageDescriptionProcessor(ImageProcessorDeps{
		VisionModelID: "vision-model-1",
		FetchModel: func(_ context.Context, id string) (ModelProvider, error) {
			return ModelProvider{
				ModelID:    "gpt-4o",
				ClientType: "openai-completions",
				ProviderID: "provider-uuid",
			}, nil
		},
		ResolveCredentials: func(_ context.Context, _ Provider) (Credentials, error) {
			return Credentials{APIKey: "sk-test"}, nil
		},
		HTTPClient: http.DefaultClient,
	})

	state := &MessageState{
		BotID:        "bot-1",
		Query:        "what is in this image?",
		InlineImages: []sdk.ImagePart{{Image: "data:image/png;base64,abc"}},
	}

	// The actual LLM call will fail because we're using DefaultClient
	// without a real endpoint. This tests that the processor correctly
	// delegates to describe function and passes through the error.
	err := p.Process(context.Background(), state)
	if err == nil {
		t.Fatal("expected error from LLM call with fake HTTP client")
	}
}

func TestImageDescriptionProcessor_EmptyImages(t *testing.T) {
	t.Parallel()

	p := NewImageDescriptionProcessor(ImageProcessorDeps{
		VisionModelID: "vision-model-1",
		HTTPClient:    http.DefaultClient,
	})

	// Empty image data → DescribeImagesWithVisionModel returns ("", nil)
	// before any model resolution. Processor preserves original state since
	// there was nothing to describe.
	state := &MessageState{
		BotID:        "bot-1",
		Query:        "hello",
		InlineImages: []sdk.ImagePart{{Image: ""}, {Image: "  "}},
	}
	err := p.Process(context.Background(), state)
	if err != nil {
		t.Fatalf("expected no error with empty images, got: %v", err)
	}
	if state.Query != "hello" {
		t.Fatalf("query should not change, got %q", state.Query)
	}
	if len(state.InlineImages) != 2 {
		t.Fatalf("empty images should be preserved (no-op): got %d", len(state.InlineImages))
	}
}

func TestDescribeImagesWithVisionModel_FetchError(t *testing.T) {
	t.Parallel()

	// Pass a non-empty image so we reach fetchModel (empty images skip it).
	_, err := DescribeImagesWithVisionModel(
		context.Background(), "bad-model",
		[]sdk.ImagePart{{Image: "data:image/png;base64,abc"}},
		func(_ context.Context, _ string) (ModelProvider, error) {
			return ModelProvider{}, errors.New("model not found")
		},
		nil, nil,
	)
	if err == nil {
		t.Fatal("expected fetch error to propagate")
	}
}

func TestDescribeImagesWithVisionModel_EmptyImages(t *testing.T) {
	t.Parallel()

	// Even with a valid client, empty images should return empty without
	// making an LLM call. The SDK model is constructed after image filtering,
	// so no HTTP request is made.
	result, err := DescribeImagesWithVisionModel(
		context.Background(), "m1",
		[]sdk.ImagePart{{Image: ""}},
		func(_ context.Context, _ string) (ModelProvider, error) {
			return ModelProvider{ModelID: "gpt-4o", ClientType: "openai-completions", ProviderID: "p1"}, nil
		},
		func(_ context.Context, _ Provider) (Credentials, error) {
			return Credentials{APIKey: "sk-fake"}, nil
		},
		http.DefaultClient,
	)
	if err != nil {
		t.Fatalf("expected no error for empty images, got: %v", err)
	}
	if result != "" {
		t.Fatalf("expected empty result for empty images, got %q", result)
	}
}
