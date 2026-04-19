-- 0070_add_openai_images_client_type
-- Remove openai-images client type.

ALTER TABLE IF EXISTS providers DROP CONSTRAINT IF EXISTS providers_client_type_check;
ALTER TABLE providers ADD CONSTRAINT providers_client_type_check CHECK (
  client_type IN (
    'openai-responses',
    'openai-completions',
    'anthropic-messages',
    'google-generative-ai',
    'openai-codex',
    'github-copilot',
    'edge-speech',
    'grok-speech',
    'gemini-speech'
  )
);
