// Production adapters wrapping *pubsub.Client + *pubsub.Message as
// Receiver / Message. Pure passthroughs — the behavior they implement
// ("open a real GCP Pub/Sub client, then forward calls") can only be
// exercised against a real Pub/Sub or the GCP emulator, which the
// integration tests do. This file is excluded from the unit-test
// coverage gate in .testcoverage.yml; the test (pubsub integration)
// CI job exercises it via the emulator.
//
// Targets cloud.google.com/go/pubsub/v2. v2 renamed Subscription →
// Subscriber and Topic → Publisher; otherwise the streaming-receive
// surface is unchanged.
package pubsub

import (
	"context"
	"fmt"
	"time"

	gpubsub "cloud.google.com/go/pubsub/v2"
)

// defaultSubscriberFactory wraps a *pubsub.Client.Subscriber in our
// Receiver interface. Each call opens a fresh client; the returned
// closer Closes it when the caller is done.
func defaultSubscriberFactory(ctx context.Context, projectID, subscriptionID string) (Receiver, func() error, error) {
	client, err := gpubsub.NewClient(ctx, projectID)
	if err != nil {
		return nil, nil, fmt.Errorf("pubsub.NewClient: %w", err)
	}
	r := &realReceiver{sub: client.Subscriber(subscriptionID)}
	return r, client.Close, nil
}

type realReceiver struct{ sub *gpubsub.Subscriber }

func (r *realReceiver) Receive(ctx context.Context, f func(context.Context, Message)) error {
	return r.sub.Receive(ctx, func(c context.Context, m *gpubsub.Message) {
		f(c, &realMessage{m: m})
	})
}

type realMessage struct{ m *gpubsub.Message }

func (r *realMessage) Data() []byte           { return r.m.Data }
func (r *realMessage) PublishTime() time.Time { return r.m.PublishTime }
func (r *realMessage) Ack()                   { r.m.Ack() }
func (r *realMessage) Nack()                  { r.m.Nack() }
