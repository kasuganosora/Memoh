package tts

import "context"

type TtsType string

type TtsMeta struct {
	Provider    string
	Description string
}

type TtsAdapter interface {
	Type() TtsType
	Meta() TtsMeta
	DefaultModel() string
	Models() []ModelInfo
	ResolveModel(model string) (string, error)
	Synthesize(ctx context.Context, text string, model string, config AudioConfig) ([]byte, error)
	Stream(ctx context.Context, text string, model string, config AudioConfig) (chan []byte, chan error)
}

// CredentialledTtsAdapter is an optional extension of TtsAdapter for providers
// that require API credentials (key + base URL) from the providers table.
type CredentialledTtsAdapter interface {
	TtsAdapter
	SynthesizeWithCreds(ctx context.Context, text, model string, config AudioConfig, apiKey, baseURL string) ([]byte, error)
}
