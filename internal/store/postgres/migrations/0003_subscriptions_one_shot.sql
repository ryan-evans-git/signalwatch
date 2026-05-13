-- BIGINT-as-bool matches the existing notify_on_resolve column shape in
-- this table. (The PI 2 api_tokens table uses BOOLEAN; we don't try to
-- unify after-the-fact.)
ALTER TABLE subscriptions ADD COLUMN one_shot BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_notifications_subscription ON notifications(subscription_id);
