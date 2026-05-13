-- one_shot caps a subscription at exactly one successful notification
-- across its lifetime (not just within one incident). See the dispatcher
-- gate in internal/dispatcher/dispatcher.go for the enforcement path.
ALTER TABLE subscriptions ADD COLUMN one_shot INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_notifications_subscription ON notifications(subscription_id);
