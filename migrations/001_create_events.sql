CREATE TABLE IF NOT EXISTS events (
    id           UUID        PRIMARY KEY,
    event_type   TEXT        NOT NULL,
    source       TEXT        NOT NULL,
    payload      JSONB       NOT NULL,
    status       TEXT        NOT NULL DEFAULT 'pending',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at TIMESTAMPTZ
);
