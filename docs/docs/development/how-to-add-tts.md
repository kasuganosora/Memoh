# How to Add a New TTS Adapter

This guide covers adding a new text-to-speech provider (e.g., Google Cloud TTS, Amazon Polly) to Memoh.

## Overview

TTS adapters implement the `TtsAdapter` interface and optionally `CredentialledTtsAdapter`. The service layer looks up the provider configuration from the database, dispatches to the correct adapter, and returns audio data.

## File Structure

```
internal/tts/adapter/<provider>/
└── <provider>.go    # Adapter implementation
```

Also create a provider YAML template:

```
conf/providers/<provider>-speech.yaml
```

## Step 1: Define the Adapter Type

```go
package polly

import "github.com/memohai/memoh/internal/tts"

const TtsTypePolly tts.TtsType = "polly"
```

## Step 2: Implement TtsAdapter Interface

```go
type PollyAdapter struct {
    log *slog.Logger
}

func NewPollyAdapter(log *slog.Logger) *PollyAdapter {
    return &PollyAdapter{log: log}
}

// Required methods

func (a *PollyAdapter) Type() tts.TtsType {
    return TtsTypePolly
}

func (a *PollyAdapter) Meta() tts.TtsMeta {
    return tts.TtsMeta{
        DisplayName: "Amazon Polly",
        Description: "Amazon Polly text-to-speech",
    }
}

func (a *PollyAdapter) DefaultModel() string {
    return "standard"
}

func (a *PollyAdapter) Models() []tts.ModelInfo {
    return []tts.ModelInfo{
        {
            ID:          "standard",
            DisplayName: "Standard",
            Capabilities: tts.ModelCapabilities{
                Voices: []tts.VoiceInfo{
                    {ID: "Joanna", Name: "Joanna", Lang: "en-US"},
                    {ID: "Matthew", Name: "Matthew", Lang: "en-US"},
                },
                Formats: []string{"mp3", "ogg_vorbis", "pcm"},
                Speed: &tts.ParamConstraint{
                    Min:     0.5,
                    Max:     2.0,
                    Default: 1.0,
                },
            },
        },
    }
}

func (a *PollyAdapter) ResolveModel(model string) (string, error) {
    if model == "" {
        return a.DefaultModel(), nil
    }
    return model, nil
}

func (a *PollyAdapter) Synthesize(ctx context.Context, text, model string, config tts.AudioConfig) ([]byte, error) {
    // Call the provider API and return audio bytes
    voice := config.Voice
    if voice == "" {
        voice = a.Models()[0].Capabilities.Voices[0].ID
    }

    // Make API call...
    return audioData, nil
}

func (a *PollyAdapter) Stream(ctx context.Context, text, model string, config tts.AudioConfig) (chan []byte, chan error) {
    chunks := make(chan []byte)
    errs := make(chan error, 1)

    go func() {
        defer close(chunks)
        defer close(errs)
        // Stream audio chunks...
    }()

    return chunks, errs
}
```

## Step 3: Implement CredentialledTtsAdapter (Optional)

If the provider requires API keys (most do):

```go
func (a *PollyAdapter) SynthesizeWithCreds(ctx context.Context, text, model string, config tts.AudioConfig, apiKey, baseURL string) ([]byte, error) {
    // Use apiKey + baseURL to call the provider API
    return audioData, nil
}
```

The service layer checks: if adapter implements `CredentialledTtsAdapter`, it calls `SynthesizeWithCreds` with `apiKey` and `baseURL` strings from the database. Otherwise, it calls `Synthesize` directly (for providers like Edge TTS that need no credentials).

## Step 4: Register the Adapter

In the FX module or service constructor:

```go
func NewTtsService(log *slog.Logger) *tts.Service {
    svc := tts.NewService(log)

    // Register all adapters
    svc.Register(polny.NewPollyAdapter(log))
    svc.Register(edge.NewEdgeAdapter(log))
    // ... etc

    return svc
}
```

Or via registry pattern in `internal/tts/registry.go`.

## Step 5: Create Provider YAML Template

`conf/providers/polly-speech.yaml`:

```yaml
name: Amazon Polly
type: polly
description: Amazon Polly text-to-speech service
base_url: "https://polly.{region}.amazonaws.com"
client_type: polly-speech
fields:
  - name: api_key
    label: Access Key
    type: password
    required: true
  - name: region
    label: AWS Region
    type: text
    default: us-east-1
```

## Step 6: Add Client Type to DB

Create a migration `db/migrations/XXXX_add_polly_speech_client_type.up.sql`:

```sql
-- XXXX_add_polly_speech_client_type
-- Add Amazon Polly speech client type
INSERT INTO models (id, name, provider_id, type, client_type)
VALUES (
    'polly-speech',
    'Amazon Polly',
    NULL,
    'speech',
    'polly-speech'
) ON CONFLICT (id) DO NOTHING;
```

Also update `0001_init.up.sql` to include this client type in the canonical schema.

Then run:
```bash
mise run sqlc-generate
mise run db-up
```

## Step 7: Frontend Settings (If Needed)

TTS settings are managed per-bot. If your adapter has custom configuration:

1. The `Models()` return value drives the voice/format/language dropdowns in the frontend
2. `ParamConstraint` controls speed/pitch sliders with min/max/default
3. No frontend code changes needed for basic voice selection — it's driven by the adapter's model info

## Model Capabilities Structure

```go
type ModelCapabilities struct {
    Voices    []VoiceInfo      // Available voices
    Languages []string         // Supported language codes (e.g., "en-US", "zh-CN")
    Formats   []string         // Supported audio formats (mp3, wav, ogg, etc.)
    Speed     *ParamConstraint // Speed adjustment (nil = not supported)
    Pitch     *ParamConstraint // Pitch adjustment (nil = not supported)
}

type VoiceInfo struct {
    ID   string // Voice identifier for API
    Name string // User-visible name
    Lang string // BCP 47 language tag (en-US, zh-CN, etc.)
}

type ParamConstraint struct {
    Options []float64 // Discrete options (if set, frontend renders dropdown)
    Min     float64   // Minimum value (frontend renders slider)
    Max     float64   // Maximum value
    Default float64   // Default value
}
```

If `Options` is set (non-empty `[]float64`), the frontend renders a dropdown. Otherwise, it renders a slider between `Min` and `Max`.

If `Speed` or `Pitch` is `nil`, the parameter is not supported and the frontend hides the control.

## Reference Implementations

| Adapter | File | What it demonstrates |
|---------|------|---------------------|
| `edge` | `internal/tts/adapter/edge/edge.go` | WebSocket-based, no API key needed |
| `grok` | `internal/tts/adapter/grok/grok.go` | API-key based, 5 voices, MP3/μ-law |
| `gemini` | `internal/tts/adapter/gemini/gemini.go` | API-key based, multi-speaker, style control |
