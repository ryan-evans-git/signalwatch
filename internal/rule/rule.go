// Package rule defines the core rule model and the rule state machine.
package rule

import (
	"errors"
	"time"
)

// Severity classifies a rule for routing and display.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Mode controls when the evaluator runs the rule.
type Mode string

const (
	// ModePush runs the rule on every record arriving from its input.
	ModePush Mode = "push"
	// ModeScheduled runs the rule on a fixed interval against in-memory windows
	// or external query sources.
	ModeScheduled Mode = "scheduled"
)

// Rule is a user-defined alert definition.
type Rule struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Enabled     bool              `json:"enabled"`
	Severity    Severity          `json:"severity"`
	Labels      map[string]string `json:"labels,omitempty"`

	// InputRef names the Input this rule reads from.
	InputRef string `json:"input_ref"`

	// Condition is the typed condition the evaluator compiles and runs.
	Condition Condition `json:"condition"`

	// Schedule applies only when Condition.Mode() == ModeScheduled.
	// Stored as a Go duration string (e.g. "30s", "5m").
	Schedule time.Duration `json:"schedule,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate checks whether r is internally consistent enough to be stored
// and compiled. It does not actually compile the condition.
func (r *Rule) Validate() error {
	if r.Name == "" {
		return errors.New("rule: name is required")
	}
	if r.InputRef == "" {
		return errors.New("rule: input_ref is required")
	}
	if r.Condition == nil {
		return errors.New("rule: condition is required")
	}
	if r.Condition.Mode() == ModeScheduled && r.Schedule <= 0 {
		return errors.New("rule: scheduled rules require a positive schedule duration")
	}
	switch r.Severity {
	case "", SeverityInfo, SeverityWarning, SeverityCritical:
	default:
		return errors.New("rule: invalid severity")
	}
	return nil
}

// State is the authoritative live state of a rule.
type State string

const (
	StateOK     State = "ok"
	StateFiring State = "firing"
)

// LiveState is the current evaluation state of a rule. It is updated after
// every evaluation and persisted to the store.
type LiveState struct {
	RuleID      string    `json:"rule_id"`
	State       State     `json:"state"`
	TriggeredAt time.Time `json:"triggered_at,omitempty"`
	LastEvalAt  time.Time `json:"last_eval_at,omitempty"`
	LastValue   string    `json:"last_value,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	IncidentID  string    `json:"incident_id,omitempty"`
}
