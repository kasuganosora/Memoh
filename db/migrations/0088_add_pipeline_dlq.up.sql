CREATE TABLE IF NOT EXISTS memory_pipeline_dlq (
    id BIGSERIAL PRIMARY KEY,
    bot_id TEXT NOT NULL,
    batch_json JSONB NOT NULL,
    error_message TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    next_retry_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_pipeline_dlq_bot_retry ON memory_pipeline_dlq (bot_id, next_retry_at);

COMMENT ON TABLE memory_pipeline_dlq IS 'Dead letter queue for formation pipeline batches that failed after max retries';
