-- Postgres migration mirrors the sqlite schema with INTEGER->BIGINT for
-- ms-resolution timestamps and Postgres-flavored constraint syntax.

CREATE TABLE IF NOT EXISTS rules (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    description   TEXT,
    enabled       BIGINT NOT NULL DEFAULT 1,
    severity      TEXT NOT NULL DEFAULT 'info',
    labels        TEXT NOT NULL DEFAULT '{}',
    input_ref     TEXT NOT NULL,
    condition     TEXT NOT NULL,
    schedule_ns   BIGINT NOT NULL DEFAULT 0,
    created_at    BIGINT NOT NULL,
    updated_at    BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_rules_input_ref ON rules(input_ref);

CREATE TABLE IF NOT EXISTS subscribers (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    channels    TEXT NOT NULL DEFAULT '[]',
    created_at  BIGINT NOT NULL,
    updated_at  BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS subscriptions (
    id                  TEXT PRIMARY KEY,
    subscriber_id       TEXT NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
    rule_id             TEXT,
    label_selector      TEXT NOT NULL DEFAULT '{}',
    dwell_ns            BIGINT NOT NULL DEFAULT 0,
    repeat_interval_ns  BIGINT NOT NULL DEFAULT 0,
    notify_on_resolve   BIGINT NOT NULL DEFAULT 1,
    channel_filter      TEXT NOT NULL DEFAULT '[]',
    created_at          BIGINT NOT NULL,
    updated_at          BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_subscriptions_rule_id ON subscriptions(rule_id);
CREATE INDEX IF NOT EXISTS idx_subscriptions_subscriber_id ON subscriptions(subscriber_id);

CREATE TABLE IF NOT EXISTS incidents (
    id            TEXT PRIMARY KEY,
    rule_id       TEXT NOT NULL,
    triggered_at  BIGINT NOT NULL,
    resolved_at   BIGINT,
    last_value    TEXT
);
CREATE INDEX IF NOT EXISTS idx_incidents_rule_id ON incidents(rule_id);
CREATE INDEX IF NOT EXISTS idx_incidents_open ON incidents(rule_id) WHERE resolved_at IS NULL;

CREATE TABLE IF NOT EXISTS notifications (
    id              TEXT PRIMARY KEY,
    incident_id     TEXT NOT NULL,
    subscription_id TEXT NOT NULL,
    subscriber_id   TEXT NOT NULL,
    channel         TEXT NOT NULL,
    address         TEXT NOT NULL,
    kind            TEXT NOT NULL,
    sent_at         BIGINT NOT NULL,
    status          TEXT NOT NULL,
    error           TEXT
);
CREATE INDEX IF NOT EXISTS idx_notifications_incident ON notifications(incident_id);

CREATE TABLE IF NOT EXISTS live_states (
    rule_id        TEXT PRIMARY KEY,
    state          TEXT NOT NULL,
    triggered_at   BIGINT NOT NULL DEFAULT 0,
    last_eval_at   BIGINT NOT NULL DEFAULT 0,
    last_value     TEXT,
    last_error     TEXT,
    incident_id    TEXT
);

CREATE TABLE IF NOT EXISTS incident_sub_states (
    incident_id      TEXT NOT NULL,
    subscription_id  TEXT NOT NULL,
    last_notified_at BIGINT NOT NULL DEFAULT 0,
    notify_count     BIGINT NOT NULL DEFAULT 0,
    resolution_sent  BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (incident_id, subscription_id)
);
