CREATE TABLE pipelines (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bot_id        UUID NOT NULL REFERENCES bots(id),
    goal          TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE pipeline_nodes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pipeline_id     UUID NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT,
    depends_on      UUID[] DEFAULT '{}',
    model_tier      TEXT NOT NULL DEFAULT 'standard',
    status          TEXT NOT NULL DEFAULT 'pending',
    input           JSONB,
    output          JSONB,
    error           TEXT,
    retry_count     INTEGER NOT NULL DEFAULT 0,
    max_retries     INTEGER NOT NULL DEFAULT 3,
    timeout_seconds INTEGER NOT NULL DEFAULT 300,
    needs_review    BOOLEAN NOT NULL DEFAULT false,
    review_result   TEXT,
    review_feedback TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ
);

CREATE INDEX idx_pipelines_bot_id ON pipelines(bot_id);
CREATE INDEX idx_pipeline_nodes_pipeline_id ON pipeline_nodes(pipeline_id);
CREATE INDEX idx_pipelines_status ON pipelines(status);
