CREATE TABLE IF NOT EXISTS api_tokens (
    id            VARCHAR(255) PRIMARY KEY,
    name          VARCHAR(255) NOT NULL,
    token_hash    VARCHAR(64) NOT NULL UNIQUE,
    scopes        TEXT NOT NULL,
    created_at    BIGINT NOT NULL,
    expires_at    BIGINT,
    last_used_at  BIGINT,
    revoked       TINYINT(1) NOT NULL DEFAULT 0,
    KEY idx_api_tokens_token_hash (token_hash)
);
