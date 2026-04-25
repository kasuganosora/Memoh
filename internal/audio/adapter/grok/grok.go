// Package grok provides an xAI Grok TTS provider implementing sdk.SpeechProvider.
// It targets the https://api.x.ai/v1/tts endpoint.
package grok

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"
)

const (
	defaultBaseURL  = "https://api.x.ai/v1"
	defaultModelID  = "grok-tts"
	defaultVoice    = "eve"
	defaultFormat   = "mp3"
	defaultLanguage = "en"
)

// Option configures the Grok TTS provider.
type Option func(*Provider)

// WithAPIKey sets the API key used for Bearer authentication.
func WithAPIKey(key string) Option {
	return func(p *Provider) { p.apiKey = key }
}

// WithBaseURL overrides the API base URL.
func WithBaseURL(url string) Option {
	return func(p *Provider) { p.baseURL = url }
}

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(hc *http.Client) Option {
	return func(p *Provider) { p.httpClient = hc }
}

// Provider implements sdk.SpeechProvider for the xAI Grok TTS API.
type Provider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// New creates a new Grok TTS provider.
func New(opts ...Option) *Provider {
	p := &Provider{
		baseURL:    defaultBaseURL,
		httpClient: &http.Client{Timeout: 180 * time.Second},
	}
	for _, o := range opts {
		o(p)
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")
	return p
}

// ListModels returns the speech models exposed by this provider.
func (p *Provider) ListModels(_ context.Context) ([]*sdk.SpeechModel, error) {
	return []*sdk.SpeechModel{
		{ID: defaultModelID, Provider: p},
	}, nil
}

type ttsRequest struct {
	Text           string `json:"text"`
	VoiceID        string `json:"voice_id"`
	Language       string `json:"language"`
	ResponseFormat string `json:"response_format,omitempty"`
}

type audioConfig struct {
	Voice          string
	Language       string
	ResponseFormat string
}

func parseConfig(cfg map[string]any) audioConfig {
	ac := audioConfig{
		Voice:          defaultVoice,
		Language:       defaultLanguage,
		ResponseFormat: defaultFormat,
	}
	if cfg == nil {
		return ac
	}
	if v, ok := cfg["voice"].(string); ok && v != "" {
		ac.Voice = v
	}
	if v, ok := cfg["language"].(string); ok && v != "" {
		ac.Language = v
	}
	if v, ok := cfg["response_format"].(string); ok && v != "" {
		ac.ResponseFormat = v
	}
	return ac
}

// formatContentType maps Grok response_format to MIME type.
func formatContentType(format string) string {
	switch strings.ToLower(format) {
	case "mp3":
		return "audio/mpeg"
	case "wav":
		return "audio/wav"
	case "pcm":
		return "audio/pcm"
	case "mulaw":
		return "audio/mulaw"
	case "alaw":
		return "audio/alaw"
	default:
		return "audio/mpeg"
	}
}

// DoSynthesize synthesizes speech and returns the complete audio bytes.
func (p *Provider) DoSynthesize(ctx context.Context, params sdk.SpeechParams) (*sdk.SpeechResult, error) {
	if p.apiKey == "" {
		return nil, errors.New("grok tts: API key is required")
	}
	cfg := parseConfig(params.Config)

	if params.Text == "" {
		return nil, errors.New("grok tts: text is required")
	}

	body := ttsRequest{
		Text:           params.Text,
		VoiceID:        cfg.Voice,
		Language:       cfg.Language,
		ResponseFormat: cfg.ResponseFormat,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("grok tts: marshal request: %w", err)
	}

	endpoint := p.baseURL + "/tts"

	const maxRetries = 2
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if reqErr != nil {
			return nil, fmt.Errorf("grok tts: create request: %w", reqErr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+p.apiKey)

		resp, doErr := p.httpClient.Do(req)
		if doErr != nil {
			return nil, fmt.Errorf("grok tts: request failed: %w", doErr)
		}

		if resp.StatusCode == http.StatusOK {
			audio, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				return nil, fmt.Errorf("grok tts: read response: %w", readErr)
			}
			return &sdk.SpeechResult{
				Audio:       audio,
				ContentType: formatContentType(cfg.ResponseFormat),
			}, nil
		}

		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusInternalServerError ||
			resp.StatusCode == http.StatusServiceUnavailable {
			lastErr = fmt.Errorf("grok tts: API returned %d: %s", resp.StatusCode, string(respBody))
			if attempt < maxRetries {
				backoff := time.Duration(1<<attempt) * time.Second
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(backoff):
				}
			}
			continue
		}

		return nil, fmt.Errorf("grok tts: API returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil, lastErr
}

// DoStream synthesizes speech and returns a streaming result.
// The Grok TTS API returns the complete audio in one response; we simulate
// streaming by chunking the synthesized audio.
func (p *Provider) DoStream(ctx context.Context, params sdk.SpeechParams) (*sdk.SpeechStreamResult, error) {
	result, err := p.DoSynthesize(ctx, params)
	if err != nil {
		return nil, err
	}

	// Wrap non-stream result into a stream channel.
	stream := make(chan []byte, 1)
	errCh := make(chan error, 1)
	stream <- result.Audio
	close(stream)

	return sdk.NewSpeechStreamResult(stream, result.ContentType, errCh), nil
}
