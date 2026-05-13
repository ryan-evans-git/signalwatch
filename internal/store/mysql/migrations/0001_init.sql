-- MySQL migration. Schema mirrors sqlite/postgres with MySQL-specific
-- adjustments:
--   - IDs are VARCHAR(255) because MySQL can't index TEXT without a
--     prefix length.
--   - `condition` is a reserved word — backtick-quote in every query.
--   - Indexes declared inline as KEY clauses; MySQL doesn't support
--     `CREATE INDEX IF NOT EXISTS`.
--   - No partial indexes (MySQL doesn't support `WHERE ... ` index
--     predicates). The redundant idx_incidents_open index from the
--     other schemas is dropped here — idx_incidents_rule_id already
--     covers the open-incident lookup.

CREATE TABLE IF NOT EXISTS rules (
    id            VARCHAR(255) PRIMARY KEY,
    name          TEXT NOT NULL,
    description   TEXT,
    enabled       BIGINT NOT NULL DEFAULT 1,
    severity      VARCHAR(64) NOT NULL DEFAULT 'info',
    labels        TEXT NOT NULL,
    input_ref     VARCHAR(255) NOT NULL,
    `condition`   TEXT NOT NULL,
    schedule_ns   BIGINT NOT NULL DEFAULT 0,
    created_at    BIGINT NOT NULL,
    updated_at    BIGINT NOT NULL,
    KEY idx_rules_input_ref (input_ref)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS subscribers (
    id          VARCHAR(255) PRIMARY KEY,
    name        TEXT NOT NULL,
    channels    TEXT NOT NULL,
    created_at  BIGINT NOT NULL,
    updated_at  BIGINT NOT NULL
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS subscriptions (
    id                  VARCHAR(255) PRIMARY KEY,
    subscriber_id       VARCHAR(255) NOT NULL,
    rule_id             VARCHAR(255),
    label_selector      TEXT NOT NULL,
    dwell_ns            BIGINT NOT NULL DEFAULT 0,
    repeat_interval_ns  BIGINT NOT NULL DEFAULT 0,
    notify_on_resolve   BIGINT NOT NULL DEFAULT 1,
    channel_filter      TEXT NOT NULL,
    created_at          BIGINT NOT NULL,
    updated_at          BIGINT NOT NULL,
    KEY idx_subscriptions_rule_id (rule_id),
    KEY idx_subscriptions_subscriber_id (subscriber_id),
    CONSTRAINT fk_subscriptions_subscriber
        FOREIGN KEY (subscriber_id) REFERENCES subscribers(id) ON DELETE CASCADE
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS incidents (
    id            VARCHAR(255) PRIMARY KEY,
    rule_id       VARCHAR(255) NOT NULL,
    triggered_at  BIGINT NOT NULL,
    resolved_at   BIGINT,
    last_value    TEXT,
    KEY idx_incidents_rule_id (rule_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS notifications (
    id              VARCHAR(255) PRIMARY KEY,
    incident_id     VARCHAR(255) NOT NULL,
    subscription_id VARCHAR(255) NOT NULL,
    subscriber_id   VARCHAR(255) NOT NULL,
    channel         VARCHAR(255) NOT NULL,
    address         VARCHAR(255) NOT NULL,
    kind            VARCHAR(32) NOT NULL,
    sent_at         BIGINT NOT NULL,
    status          VARCHAR(32) NOT NULL,
    error           TEXT,
    KEY idx_notifications_incident (incident_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS live_states (
    rule_id        VARCHAR(255) PRIMARY KEY,
    state          VARCHAR(32) NOT NULL,
    triggered_at   BIGINT NOT NULL DEFAULT 0,
    last_eval_at   BIGINT NOT NULL DEFAULT 0,
    last_value     TEXT,
    last_error     TEXT,
    incident_id    VARCHAR(255)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS incident_sub_states (
    incident_id      VARCHAR(255) NOT NULL,
    subscription_id  VARCHAR(255) NOT NULL,
    last_notified_at BIGINT NOT NULL DEFAULT 0,
    notify_count     BIGINT NOT NULL DEFAULT 0,
    resolution_sent  BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (incident_id, subscription_id)
) ENGINE=InnoDB;
