// Package pubsub provides a streaming input that consumes from one or
// more Google Cloud Pub/Sub subscriptions. Each message body is parsed
// as JSON into a rule.Record and emitted to the engine sink with
// InputRef = subscription's configured Name.
//
// Authentication uses Application Default Credentials — env vars
// (`GOOGLE_APPLICATION_CREDENTIALS`), workload identity, or the
// implicit metadata server when running on GCE/GKE/Cloud Run. No
// service-account JSON ever lives in YAML.
//
// Wire-format: messages are expected to be JSON objects. Non-JSON or
// non-object payloads are dropped after a warning log; successful
// messages are Ack'd. Bad messages are Nack'd so Pub/Sub redelivers
// (or routes to a dead-letter topic if one is bound on the
// subscription).
package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
)

// Subscription configures one Pub/Sub subscription this input
// consumes from.
type Subscription struct {
	// Name becomes the EvaluationRecord InputRef so engine rules
	// with InputRef="orders" match this subscription.
	Name string
	// SubscriptionID is the GCP Pub/Sub subscription ID. Defaults to
	// Name when empty.
	SubscriptionID string
}

// Receiver is the subset of *pubsub.Subscription this input uses.
// Spelled out as an interface so tests can inject a fake without the
// GCP SDK or an emulator.
type Receiver interface {
	Receive(ctx context.Context, f func(context.Context, Message)) error
}

// Message is the surface the input cares about from a pubsub message.
// Wraps *pubsub.Message so tests aren't tied to the SDK type.
type Message interface {
	Data() []byte
	PublishTime() time.Time
	Ack()
	Nack()
}

// SubscriberFactory returns a Receiver for the given subscription ID.
// The default factory wraps *pubsub.Client.Subscription; tests inject
// their own.
type SubscriberFactory func(ctx context.Context, projectID, subscriptionID string) (Receiver, func() error, error)

// Config configures the Pub/Sub input.
type Config struct {
	// ProjectID is the GCP project hosting the subscriptions.
	// Required.
	ProjectID string
	// Subscriptions are the subscriptions this input consumes from.
	// One goroutine per subscription.
	Subscriptions []Subscription
	// Logger receives warning logs. Defaults to slog.Default().
	Logger *slog.Logger
	// SubscriberFactory, when non-nil, replaces the default
	// *pubsub.Client-based factory. Tests inject a fake; production
	// callers leave it nil.
	SubscriberFactory SubscriberFactory
}

// Input is the Pub/Sub streaming input.
type Input struct {
	cfg    Config
	logger *slog.Logger
}

func New(cfg Config) (*Input, error) {
	if strings.TrimSpace(cfg.ProjectID) == "" {
		return nil, errors.New("pubsub input: ProjectID required")
	}
	if len(cfg.Subscriptions) == 0 {
		return nil, errors.New("pubsub input: at least one subscription required")
	}
	for i, s := range cfg.Subscriptions {
		if strings.TrimSpace(s.Name) == "" {
			return nil, errors.New("pubsub input: subscription Name required")
		}
		if cfg.Subscriptions[i].SubscriptionID == "" {
			cfg.Subscriptions[i].SubscriptionID = s.Name
		}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.SubscriberFactory == nil {
		cfg.SubscriberFactory = defaultSubscriberFactory
	}
	return &Input{cfg: cfg, logger: cfg.Logger}, nil
}

func (i *Input) Name() string { return "pubsub" }

// Start fans out one Receive loop per configured subscription. Blocks
// until ctx is cancelled.
func (i *Input) Start(ctx context.Context, sink chan<- input.EvaluationRecord) error {
	var wg sync.WaitGroup
	for _, sub := range i.cfg.Subscriptions {
		wg.Add(1)
		go func(s Subscription) {
			defer wg.Done()
			i.runSubscription(ctx, s, sink)
		}(sub)
	}
	wg.Wait()
	return ctx.Err()
}

func (i *Input) runSubscription(ctx context.Context, s Subscription, sink chan<- input.EvaluationRecord) {
	recv, close, err := i.cfg.SubscriberFactory(ctx, i.cfg.ProjectID, s.SubscriptionID)
	if err != nil {
		i.logger.Error("pubsub.client_open", "subscription", s.Name, "err", err)
		return
	}
	defer func() {
		if close != nil {
			_ = close()
		}
	}()

	// Pub/Sub's Receive runs until ctx is cancelled or it errors.
	// On non-cancellation errors, log and exit the goroutine — caller
	// can wrap with a restart loop if it wants reconnection.
	err = recv.Receive(ctx, func(_ context.Context, m Message) {
		i.handleMessage(ctx, s, m, sink)
	})
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		i.logger.Error("pubsub.receive_error", "subscription", s.Name, "err", err)
	}
}

func (i *Input) handleMessage(ctx context.Context, s Subscription, m Message, sink chan<- input.EvaluationRecord) {
	rec, ok := parseMessage(m.Data())
	if !ok {
		i.logger.Warn("pubsub.bad_message", "subscription", s.Name)
		// Nack so Pub/Sub redelivers (or routes to a DLQ topic if
		// one is bound on the subscription).
		m.Nack()
		return
	}
	when := m.PublishTime()
	if when.IsZero() {
		when = time.Now()
	}
	select {
	case sink <- input.EvaluationRecord{InputRef: s.Name, When: when, Record: rec}:
		m.Ack()
	case <-ctx.Done():
		// Don't ack — broker will redeliver on the next consumer.
		m.Nack()
	}
}

func parseMessage(body []byte) (rule.Record, bool) {
	if len(body) == 0 {
		return nil, false
	}
	var generic any
	if err := json.Unmarshal(body, &generic); err != nil {
		return nil, false
	}
	obj, ok := generic.(map[string]any)
	if !ok {
		return nil, false
	}
	return rule.Record(obj), true
}
