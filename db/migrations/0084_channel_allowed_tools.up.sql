-- 0072_channel_allowed_tools
-- Add allowed_tools JSONB column to bot_channel_configs for per-channel tool whitelisting.

ALTER TABLE bot_channel_configs
  ADD COLUMN IF NOT EXISTS allowed_tools JSONB NOT NULL DEFAULT '[]'::jsonb;
