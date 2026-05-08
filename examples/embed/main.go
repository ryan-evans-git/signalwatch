// Example: embed the signalwatch engine in a Go program.
//
// This is the same engine the standalone service uses, just driven directly
// from your code. Run with:
//
//   go run ./examples/embed
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/ryan-evans-git/signalwatch/engine"
	"github.com/ryan-evans-git/signalwatch/internal/channel"
	"github.com/ryan-evans-git/signalwatch/internal/channel/webhook"
	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/input/event"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
	"github.com/ryan-evans-git/signalwatch/internal/store/sqlite"
	"github.com/ryan-evans-git/signalwatch/internal/subscriber"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	st, err := sqlite.Open("file:embed-example.db?_pragma=journal_mode(WAL)")
	if err != nil {
		return err
	}
	defer st.Close()

	wh := webhook.New(webhook.Config{Name: "demo", URL: "https://httpbin.org/post"})
	eventInput := event.New("orders")

	eng, err := engine.New(engine.Options{
		Store:      st,
		Channels:   map[string]channel.Channel{"demo": wh},
		Inputs:     []input.Input{eventInput},
		EventInput: eventInput,
	})
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := eng.Start(ctx); err != nil {
		return err
	}
	defer eng.Close()

	// Define a rule programmatically: warn when a single order's total is
	// greater than $1000.
	r := &rule.Rule{
		ID:        uuid.NewString(),
		Name:      "high-value order",
		Enabled:   true,
		Severity:  rule.SeverityInfo,
		InputRef:  "orders",
		Condition: rule.Threshold{Field: "total", Op: rule.OpGT, Value: 1000},
	}
	if err := eng.Rules().Create(ctx, r); err != nil {
		return err
	}

	// Wire up a subscriber + subscription.
	sub := &subscriber.Subscriber{
		ID:       uuid.NewString(),
		Name:     "demo",
		Channels: []subscriber.ChannelBinding{{Channel: "demo"}},
	}
	if err := eng.Subscribers().Create(ctx, sub); err != nil {
		return err
	}
	if err := eng.Subscriptions().Create(ctx, &subscriber.Subscription{
		ID:              uuid.NewString(),
		SubscriberID:    sub.ID,
		RuleID:          r.ID,
		NotifyOnResolve: false,
	}); err != nil {
		return err
	}

	// Push some events.
	for i, total := range []float64{49.99, 500, 1500, 50, 2000} {
		_ = eng.Submit(ctx, "orders", rule.Record{
			"order_id": fmt.Sprintf("o-%d", i),
			"total":    total,
		})
		time.Sleep(200 * time.Millisecond)
	}

	fmt.Fprintln(os.Stderr, "submitted 5 orders; check the engine's notifications.")
	time.Sleep(time.Second)
	return nil
}
