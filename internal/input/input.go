// Package input defines the Input interface and a typed EvaluationRecord
// the engine routes to rules.
package input

import (
	"context"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/rule"
)

// EvaluationRecord is one input record the engine routes to subscribed rules.
// InputRef is set by the input source itself; the engine uses it to look up
// rules whose Rule.InputRef matches.
type EvaluationRecord struct {
	InputRef string
	When     time.Time
	Record   rule.Record
}

// Input is the source-of-records side of the engine. Inputs run for the
// lifetime of ctx; closing ctx must cause Start to return.
type Input interface {
	Name() string
	Start(ctx context.Context, sink chan<- EvaluationRecord) error
}
