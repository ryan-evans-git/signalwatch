// Package channel defines the Channel interface used by the dispatcher and
// the shared Notification payload presented to channel implementations.
package channel

import (
	"context"
	"time"
)

// Notification is the payload presented to a channel.Send. The dispatcher
// constructs it; channels translate it into their wire format.
type Notification struct {
	IncidentID  string
	RuleID      string
	RuleName    string
	Severity    string
	Description string
	Value       string
	Kind        string // firing | repeat | resolved
	Address     string // channel-specific destination
	Now         time.Time
	TriggeredAt time.Time
}

// Channel sends notifications somewhere users can see them.
type Channel interface {
	Name() string
	Send(ctx context.Context, n Notification) error
}
