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
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/memohai/memoh/internal/tts"
)

const TtsTypeGemini tts.TtsType = "gemini"

const geminiModelTTS = "gemini-3.1-flash-tts-preview"

type GeminiAdapter struct {
	logger *slog.Logger
	client *http.Client
}

const defaultGeminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"

func NewGeminiAdapter(log *slog.Logger) *GeminiAdapter {
	return &GeminiAdapter{
		logger: log.With(slog.String("adapter", "gemini")),
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

func (*GeminiAdapter) Type() tts.TtsType {
	return TtsTypeGemini
}

func (*GeminiAdapter) Meta() tts.TtsMeta {
	return tts.TtsMeta{
		Provider:    "Google Gemini",
		Description: "Gemini text-to-speech via generateContent API",
	}
}

func (*GeminiAdapter) DefaultModel() string {
	return geminiModelTTS
}

var geminiVoices = []tts.VoiceInfo{
	{ID: "Zephyr", Name: "Zephyr"},
	{ID: "Puck", Name: "Puck"},
	{ID: "Charon", Name: "Charon"},
	{ID: "Kore", Name: "Kore"},
	{ID: "Fenrir", Name: "Fenrir"},
	{ID: "Leda", Name: "Leda"},
	{ID: "Orus", Name: "Orus"},
	{ID: "Aoede", Name: "Aoede"},
	{ID: "Callirrhoe", Name: "Callirrhoe"},
	{ID: "Autonoe", Name: "Autonoe"},
	{ID: "Enceladus", Name: "Enceladus"},
	{ID: "Iapetus", Name: "Iapetus"},
	{ID: "Umbriel", Name: "Umbriel"},
	{ID: "Algieba", Name: "Algieba"},
	{ID: "Despina", Name: "Despina"},
	{ID: "Erinome", Name: "Erinome"},
	{ID: "Algenib", Name: "Algenib"},
	{ID: "Rasalgethi", Name: "Rasalgethi"},
	{ID: "Laomedeia", Name: "Laomedeia"},
	{ID: "Achernar", Name: "Achernar"},
	{ID: "Alnilam", Name: "Alnilam"},
	{ID: "Schedar", Name: "Schedar"},
	{ID: "Gacrux", Name: "Gacrux"},
	{ID: "Pulcherrima", Name: "Pulcherrima"},
	{ID: "Achird", Name: "Achird"},
	{ID: "Zubenelgenubi", Name: "Zubenelgenubi"},
	{ID: "Vindemiatrix", Name: "Vindemiatrix"},
	{ID: "Sadachbia", Name: "Sadachbia"},
	{ID: "Sadaltager", Name: "Sadaltager"},
	{ID: "Sulafat", Name: "Sulafat"},
}

// Languages supported by Gemini TTS.
var geminiLanguages = []string{
	"en", "zh", "hi", "es", "fr", "de", "ja", "ko", "pt", "it",
	"nl", "pl", "ru", "tr", "vi", "th", "id", "ar", "bn", "ta",
}

var geminiFormats = []string{"wav"}

func (*GeminiAdapter) Models() []tts.ModelInfo {
	return []tts.ModelInfo{
		{
			ID:          geminiModelTTS,
			Name:        "Gemini 3.1 Flash TTS",
			Description: "Google Gemini text-to-speech with 30 expressive voices",
			Capabilities: tts.ModelCapabilities{
				Voices:    geminiVoices,
				Languages: geminiLanguages,
				Formats:   geminiFormats,
			},
		},
	}
}

func (*GeminiAdapter) ResolveModel(model string) (string, error) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return geminiModelTTS, nil
	}
	// Accept any model that looks like a Gemini TTS model ID.
	return trimmed, nil
}

// Synthesize is not supported without credentials — use SynthesizeWithCreds.
func (*GeminiAdapter) Synthesize(_ context.Context, _ string, _ string, _ tts.AudioConfig) ([]byte, error) {
	return nil, errors.New("gemini tts: requires provider credentials, use SynthesizeWithCreds")
}

// Request types for Gemini generateContent API with audio output.
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

// SynthesizeWithCreds performs TTS using Gemini generateContent API with provider API key.
func (a *GeminiAdapter) SynthesizeWithCreds(ctx context.Context, text string, model string, config tts.AudioConfig, apiKey, baseURL string) ([]byte, error) {
	if apiKey == "" {
		return nil, errors.New("gemini tts: API key is required")
	}
	resolvedModel, err := a.ResolveModel(model)
	if err != nil {
		return nil, err
	}
	voiceName := config.Voice.ID
	if voiceName == "" {
		voiceName = "Kore"
	}
	if baseURL == "" {
		baseURL = defaultGeminiBaseURL
	}

	endpoint := strings.TrimRight(baseURL, "/") + "/models/" + resolvedModel + ":generateContent"

	reqBody := geminiRequest{
		Model: resolvedModel,
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: text}}},
		},
		GenerationConfig: &geminiGenConfig{
			ResponseModalities: []string{"AUDIO"},
			SpeechConfig: &geminiSpeechCfg{
				VoiceConfig: &geminiVoiceCfg{
					PrebuiltVoiceConfig: &geminiPrebuiltVoice{
						VoiceName: voiceName,
					},
				},
			},
		},
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("gemini tts: marshal request: %w", err)
	}

	// Gemini docs recommend retrying on 500 errors caused by occasional
	// text-token returns instead of audio tokens.
	const maxRetries = 2
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		audio, retry, reqErr := a.doGenerateRequest(ctx, endpoint, apiKey, payload)
		if reqErr == nil {
			return audio, nil
		}
		lastErr = reqErr
		if !retry {
			break
		}
		if attempt < maxRetries {
			backoff := time.Duration(1<<attempt) * time.Second
			a.logger.Warn("gemini tts: retryable error, retrying",
				slog.Int("attempt", attempt+1),
				slog.Any("error", reqErr),
				slog.Duration("backoff", backoff))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	return nil, lastErr
}

// doGenerateRequest executes a single generateContent HTTP request.
// Returns (audio, retryable, error). retryable=true means the caller should retry.
func (a *GeminiAdapter) doGenerateRequest(ctx context.Context, endpoint, apiKey string, payload []byte) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, false, fmt.Errorf("gemini tts: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", apiKey)

	resp, err := a.client.Do(req)
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
		// Gemini wraps errors in {"error": {...}} envelope.
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

	inlineData := gemResp.Candidates[0].Content.Parts[0].InlineData
	if inlineData == nil || inlineData.Data == "" {
		return nil, false, errors.New("gemini tts: empty audio data in response")
	}

	pcm, err := base64.StdEncoding.DecodeString(inlineData.Data)
	if err != nil {
		return nil, false, fmt.Errorf("gemini tts: decode base64 audio: %w", err)
	}

	return pcmToWav(pcm), false, nil
}

// pcmToWav wraps raw PCM audio data (24kHz, 16-bit, mono) into a WAV container.
func pcmToWav(pcm []byte) []byte { //nolint:gosec // PCM size is bounded by TTS output
	const (
		numChannels   = 1
		sampleRate    = 24000
		bitsPerSample = 16
	)
	byteRate := sampleRate * numChannels * bitsPerSample / 8
	blockAlign := numChannels * bitsPerSample / 8
	dataSize := uint32(len(pcm)) //nolint:gosec // PCM size bounded by TTS output

	buf := new(bytes.Buffer)
	// RIFF header
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, 36+dataSize)
	buf.WriteString("WAVE")
	// fmt chunk
	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16)) // chunk size
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))  // PCM format
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

func (*GeminiAdapter) Stream(_ context.Context, _ string, _ string, _ tts.AudioConfig) (chan []byte, chan error) {
	dataCh := make(chan []byte, 1)
	errCh := make(chan error, 1)

	errCh <- errors.New("gemini tts: streaming requires provider credentials")
	close(errCh)
	close(dataCh)
	return dataCh, errCh
}
