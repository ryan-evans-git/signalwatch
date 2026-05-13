CREATE TABLE IF NOT EXISTS api_tokens (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    token_hash    TEXT NOT NULL UNIQUE,
    scopes        TEXT NOT NULL DEFAULT '[]',
    created_at    BIGINT NOT NULL,
    expires_at    BIGINT,
    last_used_at  BIGINT,
    revoked       BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_token_hash ON api_tokens(token_hash);
