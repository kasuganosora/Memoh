CREATE TABLE IF NOT EXISTS bot_expressions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bot_id      UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    session_id  TEXT,
    situation   TEXT NOT NULL,
    style       TEXT NOT NULL,
    count       INT NOT NULL DEFAULT 1,
    checked     BOOL NOT NULL DEFAULT false,
    rejected    BOOL NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_active TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_bot_expressions_bot_situation
    ON bot_expressions(bot_id, checked, rejected, count DESC);

CREATE INDEX idx_bot_expressions_bot_lastactive
    ON bot_expressions(bot_id, last_active DESC);
