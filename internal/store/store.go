// Package store defines the persistence interfaces backing the engine. Each
// concrete store (sqlite, postgres, mysql) implements the Store interface
// and exposes its own constructor.
package store

import (
	"context"

	"github.com/ryan-evans-git/signalwatch/internal/rule"
	"github.com/ryan-evans-git/signalwatch/internal/subscriber"
)

// Store is the top-level persistence interface.
type Store interface {
	Rules() RuleRepo
	Subscribers() SubscriberRepo
	Subscriptions() SubscriptionRepo
	Incidents() IncidentRepo
	Notifications() NotificationRepo
	LiveStates() LiveStateRepo
	IncidentSubStates() IncidentSubStateRepo

	// Migrate brings the store schema to the latest version.
	Migrate(ctx context.Context) error
	// Close releases store resources.
	Close() error
}

type RuleRepo interface {
	Create(ctx context.Context, r *rule.Rule) error
	Update(ctx context.Context, r *rule.Rule) error
	Delete(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (*rule.Rule, error)
	List(ctx context.Context) ([]*rule.Rule, error)
	ListByInput(ctx context.Context, inputRef string) ([]*rule.Rule, error)
}

type SubscriberRepo interface {
	Create(ctx context.Context, s *subscriber.Subscriber) error
	Update(ctx context.Context, s *subscriber.Subscriber) error
	Delete(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (*subscriber.Subscriber, error)
	List(ctx context.Context) ([]*subscriber.Subscriber, error)
}

type SubscriptionRepo interface {
	Create(ctx context.Context, s *subscriber.Subscription) error
	Update(ctx context.Context, s *subscriber.Subscription) error
	Delete(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (*subscriber.Subscription, error)
	List(ctx context.Context) ([]*subscriber.Subscription, error)
	ListForRule(ctx context.Context, ruleID string, labels map[string]string) ([]*subscriber.Subscription, error)
}

type IncidentRepo interface {
	Open(ctx context.Context, inc *subscriber.Incident) error
	Resolve(ctx context.Context, id string, resolvedAt int64) error
	Get(ctx context.Context, id string) (*subscriber.Incident, error)
	List(ctx context.Context, limit int) ([]*subscriber.Incident, error)
	ListForRule(ctx context.Context, ruleID string, limit int) ([]*subscriber.Incident, error)
	// ListResolvedBefore returns incidents whose resolved_at is non-
	// null and strictly before t. Used by the retention pruner to
	// identify candidates for archival + deletion.
	ListResolvedBefore(ctx context.Context, t int64) ([]*subscriber.Incident, error)
	// DeleteResolvedBefore deletes all incidents whose resolved_at is
	// non-null and strictly before t. Cascades to the incident's
	// notifications and incident_sub_states. Returns the number of
	// incidents deleted.
	DeleteResolvedBefore(ctx context.Context, t int64) (int, error)
}

type NotificationRepo interface {
	Record(ctx context.Context, n *subscriber.Notification) error
	ListForIncident(ctx context.Context, incidentID string) ([]*subscriber.Notification, error)
	List(ctx context.Context, limit int) ([]*subscriber.Notification, error)
}

type LiveStateRepo interface {
	Get(ctx context.Context, ruleID string) (*rule.LiveState, error)
	Upsert(ctx context.Context, s *rule.LiveState) error
	List(ctx context.Context) ([]*rule.LiveState, error)
}

type IncidentSubStateRepo interface {
	Get(ctx context.Context, incidentID, subscriptionID string) (*subscriber.IncidentSubState, error)
	Upsert(ctx context.Context, s *subscriber.IncidentSubState) error
	ListForIncident(ctx context.Context, incidentID string) ([]*subscriber.IncidentSubState, error)
}
