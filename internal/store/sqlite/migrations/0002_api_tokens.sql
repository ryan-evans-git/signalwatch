CREATE TABLE IF NOT EXISTS api_tokens (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    token_hash    TEXT NOT NULL UNIQUE,        -- sha256 hex of raw secret
    scopes        TEXT NOT NULL DEFAULT '[]',  -- JSON ["read","admin"]
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER,                     -- ms-epoch; null = never
    last_used_at  INTEGER,                     -- ms-epoch; null = never observed
    revoked       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_token_hash ON api_tokens(token_hash);
