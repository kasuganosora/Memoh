package models

import (
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

type ModelType string

const (
	ModelTypeChat          ModelType = "chat"
	ModelTypeEmbedding     ModelType = "embedding"
	ModelTypeSpeech        ModelType = "speech"
	ModelTypeTranscription ModelType = "transcription"
	ModelTypeImage         ModelType = "image"
)

type ClientType string

const (
	ClientTypeOpenAIResponses         ClientType = "openai-responses"
	ClientTypeOpenAICompletions       ClientType = "openai-completions"
	ClientTypeAnthropicMessages       ClientType = "anthropic-messages"
	ClientTypeGoogleGenerativeAI      ClientType = "google-generative-ai"
	ClientTypeOpenAICodex             ClientType = "openai-codex"
	ClientTypeGitHubCopilot           ClientType = "github-copilot"
	ClientTypeEdgeSpeech              ClientType = "edge-speech"
	ClientTypeGrokSpeech              ClientType = "grok-speech"
	ClientTypeGeminiSpeech            ClientType = "gemini-speech"
	ClientTypeOpenAIImages            ClientType = "openai-images"
	ClientTypeOpenAISpeech            ClientType = "openai-speech"
	ClientTypeOpenAITranscription     ClientType = "openai-transcription"
	ClientTypeOpenRouterSpeech        ClientType = "openrouter-speech"
	ClientTypeOpenRouterTranscription ClientType = "openrouter-transcription"
	ClientTypeElevenLabsSpeech        ClientType = "elevenlabs-speech"
	ClientTypeElevenLabsTranscription ClientType = "elevenlabs-transcription"
	ClientTypeDeepgramSpeech          ClientType = "deepgram-speech"
	ClientTypeDeepgramTranscription   ClientType = "deepgram-transcription"
	ClientTypeMiniMaxSpeech           ClientType = "minimax-speech"
	ClientTypeVolcengineSpeech        ClientType = "volcengine-speech"
	ClientTypeAlibabaSpeech           ClientType = "alibabacloud-speech"
	ClientTypeMicrosoftSpeech         ClientType = "microsoft-speech"
	ClientTypeGoogleSpeech            ClientType = "google-speech"
	ClientTypeGoogleTranscription     ClientType = "google-transcription"
)

const (
	CompatVision      = "vision"
	CompatToolCall    = "tool-call"
	CompatImageOutput = "image-output"
	CompatImageApi    = "image-api"
	CompatReasoning   = "reasoning"
)

const (
	ReasoningEffortNone   = "none"
	ReasoningEffortLow    = "low"
	ReasoningEffortMedium = "medium"
	ReasoningEffortHigh   = "high"
	ReasoningEffortXHigh  = "xhigh"
)

// validCompatibilities enumerates accepted compatibility tokens.
var validCompatibilities = map[string]struct{}{
	CompatVision: {}, CompatToolCall: {}, CompatImageOutput: {}, CompatImageApi: {}, CompatReasoning: {},
}

var validReasoningEfforts = map[string]struct{}{
	ReasoningEffortNone:   {},
	ReasoningEffortLow:    {},
	ReasoningEffortMedium: {},
	ReasoningEffortHigh:   {},
	ReasoningEffortXHigh:  {},
}

// ModelConfig holds the JSONB config stored per model.
type ModelConfig struct {
	Dimensions       *int     `json:"dimensions,omitempty"`
	Compatibilities  []string `json:"compatibilities,omitempty"`
	ContextWindow    *int     `json:"context_window,omitempty"`
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
}

type Model struct {
	ModelID    string          `json:"model_id"`
	Name       string          `json:"name"`
	ProviderID string          `json:"provider_id"`
	Type       ModelType       `json:"type"`
	Config     json.RawMessage `json:"config"`
}

// ParseConfig unmarshals the raw config into a typed ModelConfig.
func (m *Model) ParseConfig() ModelConfig {
	var cfg ModelConfig
	if len(m.Config) > 0 {
		_ = json.Unmarshal(m.Config, &cfg)
	}
	return cfg
}

// SetConfig marshals a ModelConfig into the raw config.
func (m *Model) SetConfig(cfg ModelConfig) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	m.Config = b
	return nil
}

func (m *Model) Validate() error {
	if m.ModelID == "" {
		return errors.New("model ID is required")
	}
	if m.ProviderID == "" {
		return errors.New("provider ID is required")
	}
	if _, err := uuid.Parse(m.ProviderID); err != nil {
		return errors.New("provider ID must be a valid UUID")
	}
	if m.Type != ModelTypeChat && m.Type != ModelTypeEmbedding && m.Type != ModelTypeSpeech && m.Type != ModelTypeTranscription && m.Type != ModelTypeImage {
		return errors.New("invalid model type")
	}
	cfg := m.ParseConfig()
	if m.Type == ModelTypeEmbedding {
		if cfg.Dimensions == nil || *cfg.Dimensions <= 0 {
			return errors.New("dimensions must be greater than 0 for embedding models")
		}
	}
	for _, c := range cfg.Compatibilities {
		if _, ok := validCompatibilities[c]; !ok {
			return errors.New("invalid compatibility: " + c)
		}
	}
	for _, effort := range cfg.ReasoningEfforts {
		if _, ok := validReasoningEfforts[effort]; !ok {
			return errors.New("invalid reasoning effort: " + effort)
		}
	}
	return nil
}

// HasCompatibility checks whether the model config includes the given capability.
func (m *Model) HasCompatibility(c string) bool {
	cfg := m.ParseConfig()
	for _, v := range cfg.Compatibilities {
		if v == c {
			return true
		}
	}
	return false
}

// clientTypesWithInlineImages lists client types whose provider API actually
// accepts image content parts (image_url, etc.) in chat message payloads.
// CompatVision on the model alone is not sufficient — the provider's API must
// also support the wire format. For example, DeepSeek uses openai-completions
// but rejects image_url content; only true OpenAI and Anthropic accept it.
var clientTypesWithInlineImages = map[ClientType]bool{
	ClientTypeOpenAIResponses:    true,
	ClientTypeAnthropicMessages:  true,
	ClientTypeGoogleGenerativeAI: true,
}

// ClientSupportsInlineImages returns true if the given client type's provider
// API supports sending inline image content in chat messages. This is used to
// gate SupportsImageInput: even if a model has CompatVision, images must not
// be injected unless the provider API can actually process them.
func ClientSupportsInlineImages(clientType ClientType) bool {
	return clientTypesWithInlineImages[clientType]
}

type AddRequest Model

type AddResponse struct {
	ID      string `json:"id"`
	ModelID string `json:"model_id"`
}

type GetRequest struct {
	ID string `json:"id"`
}

type GetResponse struct {
	ID      string `json:"id"`
	ModelID string `json:"model_id"`
	Model
}

type UpdateRequest Model

type ListRequest struct {
	Type ModelType `json:"type,omitempty"`
}

type DeleteRequest struct {
	ID      string `json:"id,omitempty"`
	ModelID string `json:"model_id,omitempty"`
}

type DeleteResponse struct {
	Message string `json:"message"`
}

type CountResponse struct {
	Count int64 `json:"count"`
}

// TestStatus represents the outcome of probing a model.
type TestStatus string

const (
	TestStatusOK                TestStatus = "ok"
	TestStatusAuthError         TestStatus = "auth_error"
	TestStatusModelNotSupported TestStatus = "model_not_supported"
	TestStatusError             TestStatus = "error"
)

// TestResponse is returned by POST /models/:id/test.
type TestResponse struct {
	Status    TestStatus `json:"status"`
	Reachable bool       `json:"reachable"`
	LatencyMs int64      `json:"latency_ms,omitempty"`
	Message   string     `json:"message,omitempty"`
}
