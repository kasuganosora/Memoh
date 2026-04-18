-- 0068_add_grok_speech_client_type
-- Remove grok-speech client type.

ALTER TABLE IF EXISTS providers DROP CONSTRAINT IF EXISTS providers_client_type_check;
ALTER TABLE providers ADD CONSTRAINT providers_client_type_check CHECK (
  client_type IN (
    'openai-responses',
    'openai-completions',
    'anthropic-messages',
    'google-generative-ai',
    'openai-codex',
    'github-copilot',
    'edge-speech'
  )
);
