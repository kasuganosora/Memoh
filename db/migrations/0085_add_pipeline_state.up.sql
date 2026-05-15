CREATE TABLE IF NOT EXISTS memory_pipeline_state (
    bot_id TEXT NOT NULL PRIMARY KEY,
    buffer_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    threshold INTEGER NOT NULL DEFAULT 1,
    warmup_index INTEGER NOT NULL DEFAULT 0,
    retry_count INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE memory_pipeline_state IS 'Persists formation pipeline buffer state for crash recovery';
