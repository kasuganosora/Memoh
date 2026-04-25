CREATE TABLE IF NOT EXISTS bot_jargons (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bot_id      UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    session_id  TEXT,
    content     TEXT NOT NULL,
    meaning     TEXT,
    count       INT NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_bot_jargons_unique
    ON bot_jargons(bot_id, content);

CREATE INDEX idx_bot_jargons_bot
    ON bot_jargons(bot_id, count DESC);
