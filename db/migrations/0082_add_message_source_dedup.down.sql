-- 0082_add_message_source_dedup (down)
-- Remove the partial unique index on source_message_id.
DROP INDEX IF EXISTS idx_bot_history_messages_source_dedup;

