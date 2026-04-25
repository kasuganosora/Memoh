// Package gemini provides a Google Gemini TTS provider implementing sdk.SpeechProvider.
// It uses the Gemini generateContent API with audio output modality.
package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
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
	defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"
	defaultModelID = "gemini-3.1-flash-tts-preview"
	defaultVoice   = "Kore"
	contentTypeWAV = "audio/wav"
)

// Option configures the Gemini TTS provider.
type Option func(*Provider)

// WithAPIKey sets the API key used for x-goog-api-key header.
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

// Provider implements sdk.SpeechProvider for the Gemini generateContent API.
type Provider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// New creates a new Gemini TTS provider.
func New(opts ...Option) *Provider {
	p := &Provider{
		baseURL:    defaultBaseURL,
		httpClient: &http.Client{Timeout: 120 * time.Second},
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

// --- Gemini API JSON types ---

type geminiRequest struct {
	Model            string           `json:"model"`
	Contents         []geminiContent  `json:"contents"`
	GenerationConfig *geminiGenConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *geminiInlineData `json:"inlineData,omitempty"`
}

type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiGenConfig struct {
	ResponseModalities []string         `json:"responseModalities"`
	SpeechConfig       *geminiSpeechCfg `json:"speechConfig,omitempty"`
}

type geminiSpeechCfg struct {
	VoiceConfig *geminiVoiceCfg `json:"voiceConfig,omitempty"`
}

type geminiVoiceCfg struct {
	PrebuiltVoiceConfig *geminiPrebuiltVoice `json:"prebuiltVoiceConfig,omitempty"`
}

type geminiPrebuiltVoice struct {
	VoiceName string `json:"voiceName"`
}

type geminiResponse struct {
	Candidates []geminiCandidate `json:"candidates"`
	Error      *geminiAPIError   `json:"error,omitempty"`
}

type geminiCandidate struct {
	Content *geminiContent `json:"content,omitempty"`
}

type geminiAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// --- Config parsing ---

type audioConfig struct {
	Voice string
}

func parseConfig(cfg map[string]any) audioConfig {
	ac := audioConfig{
		Voice: defaultVoice,
	}
	if cfg == nil {
		return ac
	}
	if v, ok := cfg["voice"].(string); ok && v != "" {
		ac.Voice = v
	}
	return ac
}

// --- SpeechProvider implementation ---

// DoSynthesize synthesizes speech via Gemini generateContent API and returns WAV audio.
func (p *Provider) DoSynthesize(ctx context.Context, params sdk.SpeechParams) (*sdk.SpeechResult, error) {
	if p.apiKey == "" {
		return nil, errors.New("gemini tts: API key is required")
	}
	if params.Text == "" {
		return nil, errors.New("gemini tts: text is required")
	}

	cfg := parseConfig(params.Config)

	modelID := defaultModelID
	if params.Model != nil && params.Model.ID != "" {
		modelID = params.Model.ID
	}

	reqBody := geminiRequest{
		Model: modelID,
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: params.Text}}},
		},
		GenerationConfig: &geminiGenConfig{
			ResponseModalities: []string{"AUDIO"},
			SpeechConfig: &geminiSpeechCfg{
				VoiceConfig: &geminiVoiceCfg{
					PrebuiltVoiceConfig: &geminiPrebuiltVoice{
						VoiceName: cfg.Voice,
					},
				},
			},
		},
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("gemini tts: marshal request: %w", err)
	}

	endpoint := p.baseURL + "/models/" + modelID + ":generateContent"

	const maxRetries = 2
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		audio, retry, reqErr := p.doRequest(ctx, endpoint, payload)
		if reqErr == nil {
			return &sdk.SpeechResult{
				Audio:       audio,
				ContentType: contentTypeWAV,
			}, nil
		}
		lastErr = reqErr
		if !retry {
			break
		}
		if attempt < maxRetries {
			backoff := time.Duration(1<<attempt) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	return nil, lastErr
}

// doRequest executes a single generateContent HTTP request.
// Returns (audio, retryable, error).
func (p *Provider) doRequest(ctx context.Context, endpoint string, payload []byte) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, false, fmt.Errorf("gemini tts: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", p.apiKey)

	resp, err := p.httpClient.Do(req) //nolint:gosec // SSRF via configurable provider base URL, intended behavior
	if err != nil {
		return nil, false, fmt.Errorf("gemini tts: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("gemini tts: read response: %w", err)
	}

	if resp.StatusCode == http.StatusInternalServerError {
		return nil, true, errors.New("gemini tts: server error 500")
	}

	if resp.StatusCode != http.StatusOK {
		var envelope struct {
			Error *geminiAPIError `json:"error"`
		}
		if json.Unmarshal(respBody, &envelope) == nil && envelope.Error != nil && envelope.Error.Message != "" {
			return nil, false, fmt.Errorf("gemini tts: API returned %d: %s", resp.StatusCode, envelope.Error.Message)
		}
		return nil, false, fmt.Errorf("gemini tts: API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var gemResp geminiResponse
	if err := json.Unmarshal(respBody, &gemResp); err != nil {
		return nil, false, fmt.Errorf("gemini tts: decode response: %w", err)
	}

	if gemResp.Error != nil {
		return nil, false, fmt.Errorf("gemini tts: API error: %s", gemResp.Error.Message)
	}

	if len(gemResp.Candidates) == 0 || gemResp.Candidates[0].Content == nil || len(gemResp.Candidates[0].Content.Parts) == 0 {
		return nil, false, errors.New("gemini tts: no audio data in response")
	}

	// Gemini returns PCM audio as base64-encoded inline data.
	for _, part := range gemResp.Candidates[0].Content.Parts {
		if part.InlineData != nil && part.InlineData.Data != "" {
			pcm, decErr := base64.StdEncoding.DecodeString(part.InlineData.Data)
			if decErr != nil {
				return nil, false, fmt.Errorf("gemini tts: decode base64 audio: %w", decErr)
			}
			return pcmToWav(pcm), false, nil
		}
	}

	return nil, false, errors.New("gemini tts: no inline audio data in response")
}

// DoStream synthesizes speech and returns a streaming result.
// The Gemini API returns the complete audio; we wrap it as a single-chunk stream.
func (p *Provider) DoStream(ctx context.Context, params sdk.SpeechParams) (*sdk.SpeechStreamResult, error) {
	result, err := p.DoSynthesize(ctx, params)
	if err != nil {
		return nil, err
	}

	stream := make(chan []byte, 1)
	errCh := make(chan error, 1)
	stream <- result.Audio
	close(stream)

	return sdk.NewSpeechStreamResult(stream, result.ContentType, errCh), nil
}

// pcmToWav wraps raw PCM audio data (24kHz, 16-bit, mono) into a WAV container.
func pcmToWav(pcm []byte) []byte {
	const (
		numChannels   = 1
		sampleRate    = 24000
		bitsPerSample = 16
	)
	byteRate := sampleRate * numChannels * bitsPerSample / 8
	blockAlign := numChannels * bitsPerSample / 8
	dataSize := uint32(len(pcm)) //nolint:gosec // G115: PCM data bounded by HTTP response/API limits, safe on 64-bit

	buf := new(bytes.Buffer)
	// RIFF header
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, 36+dataSize)
	buf.WriteString("WAVE")
	// fmt chunk
	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint16(numChannels))
	_ = binary.Write(buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(buf, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(buf, binary.LittleEndian, uint16(blockAlign))
	_ = binary.Write(buf, binary.LittleEndian, uint16(bitsPerSample))
	// data chunk
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, dataSize)
	buf.Write(pcm)

	return buf.Bytes()
}
