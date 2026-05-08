// Package subscriber holds the subscriber, subscription, incident, and
// notification types that the dispatcher and store work with.
package subscriber

import (
	"errors"
	"time"
)

// ChannelBinding ties a subscriber to a specific configured channel by name,
// with an address (email, slack channel, webhook URL) appropriate to that
// channel.
type ChannelBinding struct {
	Channel string `json:"channel"` // configured channel name (e.g. "ops-email")
	Address string `json:"address"` // channel-specific address
}

// Subscriber is a person or system that wants to be notified.
type Subscriber struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Channels  []ChannelBinding `json:"channels"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// Subscription connects a subscriber to one or more rules with delivery
// preferences (dwell, repeat, resolve).
type Subscription struct {
	ID           string `json:"id"`
	SubscriberID string `json:"subscriber_id"`

	// Match is either a specific rule id, or a label selector. Exactly one
	// must be set.
	RuleID        string            `json:"rule_id,omitempty"`
	LabelSelector map[string]string `json:"label_selector,omitempty"`

	Dwell           time.Duration `json:"dwell"`
	RepeatInterval  time.Duration `json:"repeat_interval"`
	NotifyOnResolve bool          `json:"notify_on_resolve"`

	// ChannelFilter, if non-empty, restricts which of the subscriber's
	// channels are used for this subscription.
	ChannelFilter []string `json:"channel_filter,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Subscription) Validate() error {
	if s.SubscriberID == "" {
		return errors.New("subscription: subscriber_id required")
	}
	if s.RuleID == "" && len(s.LabelSelector) == 0 {
		return errors.New("subscription: rule_id or label_selector required")
	}
	if s.RuleID != "" && len(s.LabelSelector) > 0 {
		return errors.New("subscription: only one of rule_id or label_selector may be set")
	}
	if s.Dwell < 0 || s.RepeatInterval < 0 {
		return errors.New("subscription: dwell and repeat_interval must be >= 0")
	}
	return nil
}

// Incident records a single OK->FIRING->OK cycle of a rule.
type Incident struct {
	ID          string    `json:"id"`
	RuleID      string    `json:"rule_id"`
	TriggeredAt time.Time `json:"triggered_at"`
	ResolvedAt  time.Time `json:"resolved_at,omitempty"`
	LastValue   string    `json:"last_value,omitempty"`
}

// IncidentSubState tracks per-(incident, subscription) delivery state used
// by the dispatcher to enforce dwell/dedup/repeat semantics.
type IncidentSubState struct {
	IncidentID     string    `json:"incident_id"`
	SubscriptionID string    `json:"subscription_id"`
	LastNotifiedAt time.Time `json:"last_notified_at,omitempty"`
	NotifyCount    int       `json:"notify_count"`
	ResolutionSent bool      `json:"resolution_sent"`
}

// NotificationKind classifies a queued or sent notification.
type NotificationKind string

const (
	KindFiring   NotificationKind = "firing"
	KindRepeat   NotificationKind = "repeat"
	KindResolved NotificationKind = "resolved"
)

// Notification is the audit-trail record of an attempted notification send.
type Notification struct {
	ID             string           `json:"id"`
	IncidentID     string           `json:"incident_id"`
	SubscriptionID string           `json:"subscription_id"`
	SubscriberID   string           `json:"subscriber_id"`
	Channel        string           `json:"channel"`
	Address        string           `json:"address"`
	Kind           NotificationKind `json:"kind"`
	SentAt         time.Time        `json:"sent_at"`
	Status         string           `json:"status"` // "ok" | "error"
	Error          string           `json:"error,omitempty"`
}
