-- 0082_add_message_source_dedup
-- Add partial unique index on source_message_id to prevent duplicate message rows
-- when the same external message is processed more than once.
CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_history_messages_source_dedup
  ON bot_history_messages (session_id, source_message_id)
  WHERE source_message_id IS NOT NULL AND source_message_id != '';

