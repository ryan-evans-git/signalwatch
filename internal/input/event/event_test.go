package event_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/input/event"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
)

func TestNew_DefaultsName(t *testing.T) {
	in := event.New("")
	if in.Name() != "events" {
		t.Fatalf("default name: want events, got %q", in.Name())
	}
}

func TestNew_RespectsCustomName(t *testing.T) {
	in := event.New("orders")
	if in.Name() != "orders" {
		t.Fatalf("custom name: want orders, got %q", in.Name())
	}
}

// Submit before Start should error — there's no sink yet.
func TestSubmit_BeforeStartErrors(t *testing.T) {
	in := event.New("events")
	err := in.Submit(context.Background(), "events", rule.Record{"k": 1})
	if err == nil || !strings.Contains(err.Error(), "not started") {
		t.Fatalf("want not-started error, got %v", err)
	}
}

func TestStart_PopulatesSinkAndExitsOnCtxDone(t *testing.T) {
	in := event.New("events")
	sink := make(chan input.EvaluationRecord, 1)

	ctx, cancel := context.WithCancel(context.Background())
	startErr := make(chan error, 1)
	go func() { startErr <- in.Start(ctx, sink) }()

	// Wait until Start has installed the sink. We can't observe it
	// directly so we Submit and assert success.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := in.Submit(context.Background(), "", rule.Record{"k": 1}); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case rec := <-sink:
		if rec.InputRef != "events" {
			t.Fatalf("InputRef: want events, got %q", rec.InputRef)
		}
		if rec.Record["k"] != 1 {
			t.Fatalf("Record[k]: want 1, got %v", rec.Record["k"])
		}
		if rec.When.IsZero() {
			t.Fatalf("When should be populated")
		}
	case <-time.After(time.Second):
		t.Fatal("never received submitted record")
	}

	cancel()
	select {
	case err := <-startErr:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("Start return: want context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not return after ctx cancel")
	}

	// After Start returns, the sink is cleared again — Submit errors.
	if err := in.Submit(context.Background(), "", rule.Record{"k": 1}); err == nil {
		t.Fatalf("Submit after Start returned should error")
	}
}

func TestSubmit_DefaultsInputRefToConfigured(t *testing.T) {
	in := event.New("orders")
	sink := make(chan input.EvaluationRecord, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go in.Start(ctx, sink)

	// Wait briefly for sink registration.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := in.Submit(context.Background(), "", rule.Record{"x": 1}); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	rec := <-sink
	if rec.InputRef != "orders" {
		t.Fatalf("empty inputRef should default to configured name; got %q", rec.InputRef)
	}
}

// Submit respects ctx.Done by returning ctx.Err when the sink is full.
func TestSubmit_RespectsCtxDoneWhenSinkBlocks(t *testing.T) {
	in := event.New("events")
	sink := make(chan input.EvaluationRecord) // unbuffered, no reader
	startCtx, startCancel := context.WithCancel(context.Background())
	defer startCancel()
	go in.Start(startCtx, sink)

	// Register sink.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		// Use a quickly-cancelled ctx so the first Submit returns immediately.
		quickCtx, quickCancel := context.WithCancel(context.Background())
		quickCancel()
		if err := in.Submit(quickCtx, "", rule.Record{"x": 1}); errors.Is(err, context.Canceled) {
			// Sink is installed; the canceled-ctx path executed.
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("never observed canceled-ctx return path")
}
