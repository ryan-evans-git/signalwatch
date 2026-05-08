// Package event provides a push-mode input. Records are submitted in-process
// via the Input's Submit method (used by the engine's library API) or via
// HTTP POST /v1/events handled by the api package which forwards into the
// same sink.
package event

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
)

// Input is an in-process event sink. The engine wires it as both the
// callable target for engine.Submit and the receiver for HTTP POSTs to
// /v1/events.
type Input struct {
	name string

	mu   sync.Mutex
	sink chan<- input.EvaluationRecord
}

// New returns a new event input bound to the named input ref.
func New(name string) *Input {
	if name == "" {
		name = "events"
	}
	return &Input{name: name}
}

func (i *Input) Name() string { return i.name }

func (i *Input) Start(ctx context.Context, sink chan<- input.EvaluationRecord) error {
	i.mu.Lock()
	i.sink = sink
	i.mu.Unlock()
	<-ctx.Done()

	i.mu.Lock()
	i.sink = nil
	i.mu.Unlock()
	return ctx.Err()
}

// Submit pushes a record at the given input ref. inputRef may be empty, in
// which case the configured input name is used.
func (i *Input) Submit(ctx context.Context, inputRef string, r rule.Record) error {
	i.mu.Lock()
	sink := i.sink
	i.mu.Unlock()
	if sink == nil {
		return errors.New("event input: not started")
	}
	if inputRef == "" {
		inputRef = i.name
	}
	rec := input.EvaluationRecord{
		InputRef: inputRef,
		When:     time.Now(),
		Record:   r,
	}
	select {
	case sink <- rec:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
