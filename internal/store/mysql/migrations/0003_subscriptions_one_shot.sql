ALTER TABLE subscriptions ADD COLUMN one_shot TINYINT(1) NOT NULL DEFAULT 0;
CREATE INDEX idx_notifications_subscription ON notifications(subscription_id);
