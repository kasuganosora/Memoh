-- 0071_chat_timing
-- Add chat_timing JSONB column to bots table for smart conversation timing configuration.
ALTER TABLE bots ADD COLUMN IF NOT EXISTS chat_timing JSONB NOT NULL DEFAULT '{}'::jsonb;
