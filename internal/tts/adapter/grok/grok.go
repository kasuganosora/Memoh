package grok

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/memohai/memoh/internal/tts"
)

const TtsTypeGrok tts.TtsType = "grok"

const grokModelTTS = "grok-tts"

type GrokAdapter struct {
	logger *slog.Logger
	client *http.Client
}

const defaultGrokBaseURL = "https://api.x.ai/v1"

func NewGrokAdapter(log *slog.Logger) *GrokAdapter {
	return &GrokAdapter{
		logger: log.With(slog.String("adapter", "grok")),
		client: &http.Client{Timeout: 180 * time.Second},
	}
}

func (*GrokAdapter) Type() tts.TtsType {
	return TtsTypeGrok
}

func (*GrokAdapter) Meta() tts.TtsMeta {
	return tts.TtsMeta{
		Provider:    "xAI (Grok)",
		Description: "xAI Grok text-to-speech API",
	}
}

func (*GrokAdapter) DefaultModel() string {
	return grokModelTTS
}

var grokVoices = []tts.VoiceInfo{
	{ID: "eve", Name: "Eve"},
	{ID: "ara", Name: "Ara"},
	{ID: "rex", Name: "Rex"},
	{ID: "sal", Name: "Sal"},
	{ID: "leo", Name: "Leo"},
}

// BCP-47 language codes supported by Grok TTS (from official docs).
var grokLanguages = []string{
	"en", "ar-EG", "ar-SA", "ar-AE", "bn", "zh", "fr", "de", "hi",
	"id", "it", "ja", "ko", "pt-BR", "pt-PT", "ru", "es-MX", "es-ES", "tr", "vi",
}

var grokFormats = []string{"mp3", "wav", "pcm", "mulaw", "alaw"}

func (*GrokAdapter) Models() []tts.ModelInfo {
	return []tts.ModelInfo{
		{
			ID:          grokModelTTS,
			Name:        "Grok TTS",
			Description: "xAI Grok text-to-speech with expressive voices",
			Capabilities: tts.ModelCapabilities{
				Voices:    grokVoices,
				Languages: grokLanguages,
				Formats:   grokFormats,
			},
		},
	}
}

func (*GrokAdapter) ResolveModel(model string) (string, error) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return grokModelTTS, nil
	}
	if !strings.EqualFold(trimmed, grokModelTTS) {
		return "", fmt.Errorf("grok tts: unsupported model: %s", model)
	}
	return grokModelTTS, nil
}

// Synthesize is not supported without credentials — use SynthesizeWithCreds.
func (*GrokAdapter) Synthesize(_ context.Context, _ string, _ string, _ tts.AudioConfig) ([]byte, error) {
	return nil, errors.New("grok tts: requires provider credentials, use SynthesizeWithCreds")
}

type ttsRequest struct {
	Text           string `json:"text"`
	VoiceID        string `json:"voice_id"`
	Language       string `json:"language"`
	ResponseFormat string `json:"response_format,omitempty"`
}

// SynthesizeWithCreds performs TTS using provider API key and base URL.
func (a *GrokAdapter) SynthesizeWithCreds(ctx context.Context, text string, _ string, config tts.AudioConfig, apiKey, baseURL string) ([]byte, error) {
	if apiKey == "" {
		return nil, errors.New("grok tts: API key is required")
	}
	voiceID := config.Voice.ID
	if voiceID == "" {
		voiceID = "eve"
	}
	lang := config.Voice.Lang
	if lang == "" {
		lang = "en"
	}
	if baseURL == "" {
		baseURL = defaultGrokBaseURL
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/tts"

	body := ttsRequest{
		Text:           text,
		VoiceID:        voiceID,
		Language:       lang,
		ResponseFormat: config.Format,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("grok tts: marshal request: %w", err)
	}

	// xAI docs: retry on 429, 500, 503 with exponential backoff.
	const maxRetries = 2
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if reqErr != nil {
			return nil, fmt.Errorf("grok tts: create request: %w", reqErr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)

		resp, doErr := a.client.Do(req)
		if doErr != nil {
			return nil, fmt.Errorf("grok tts: request failed: %w", doErr)
		}

		if resp.StatusCode == http.StatusOK {
			audio, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				return nil, fmt.Errorf("grok tts: read response: %w", readErr)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "" {
				// Store actual content-type from API response for downstream use.
				// The caller (service.go) will use resolveContentType(config.Format)
				// but we log the actual type here for debugging.
				a.logger.Debug("grok tts: response content-type", slog.String("content_type", ct))
			}
			return audio, nil
		}

		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusInternalServerError ||
			resp.StatusCode == http.StatusServiceUnavailable {
			lastErr = fmt.Errorf("grok tts: API returned %d: %s", resp.StatusCode, string(respBody))
			if attempt < maxRetries {
				backoff := time.Duration(1<<attempt) * time.Second
				a.logger.Warn("grok tts: retryable error, retrying",
					slog.Int("attempt", attempt+1),
					slog.Int("status", resp.StatusCode),
					slog.Duration("backoff", backoff))
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

func (*GrokAdapter) Stream(_ context.Context, _ string, _ string, _ tts.AudioConfig) (chan []byte, chan error) {
	dataCh := make(chan []byte, 1)
	errCh := make(chan error, 1)

	errCh <- errors.New("grok tts: streaming requires provider credentials")
	close(errCh)
	close(dataCh)
	return dataCh, errCh
}
