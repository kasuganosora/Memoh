-- 0072_channel_allowed_tools
-- Remove allowed_tools column from bot_channel_configs.

ALTER TABLE bot_channel_configs
  DROP COLUMN IF EXISTS allowed_tools;
