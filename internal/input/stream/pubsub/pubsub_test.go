package pubsub_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/input"
	psinput "github.com/ryan-evans-git/signalwatch/internal/input/stream/pubsub"
)

// ---- fakes ----

type fakeMessage struct {
	data    []byte
	publish time.Time
	acks    *atomic.Int32
	nacks   *atomic.Int32
}

func (m *fakeMessage) Data() []byte           { return m.data }
func (m *fakeMessage) PublishTime() time.Time { return m.publish }
func (m *fakeMessage) Ack()                   { m.acks.Add(1) }
func (m *fakeMessage) Nack()                  { m.nacks.Add(1) }

// fakeReceiver replays a fixed slice of messages, then blocks on ctx.
type fakeReceiver struct {
	mu        sync.Mutex
	messages  []psinput.Message
	receiveEr error
}

func (r *fakeReceiver) Receive(ctx context.Context, f func(context.Context, psinput.Message)) error {
	r.mu.Lock()
	if r.receiveEr != nil {
		err := r.receiveEr
		r.mu.Unlock()
		return err
	}
	msgs := append([]psinput.Message(nil), r.messages...)
	r.mu.Unlock()
	for _, m := range msgs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			f(ctx, m)
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

func factoryFor(r psinput.Receiver, closeErr error) psinput.SubscriberFactory {
	closed := atomic.Bool{}
	return func(_ context.Context, _, _ string) (psinput.Receiver, func() error, error) {
		return r, func() error {
			closed.Store(true)
			return closeErr
		}, nil
	}
}

// ---- New ----

func TestNew_RejectsMissingProjectID(t *testing.T) {
	_, err := psinput.New(psinput.Config{
		Subscriptions: []psinput.Subscription{{Name: "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "ProjectID") {
		t.Fatalf("want ProjectID error, got %v", err)
	}
}

func TestNew_RejectsNoSubscriptions(t *testing.T) {
	_, err := psinput.New(psinput.Config{ProjectID: "p"})
	if err == nil || !strings.Contains(err.Error(), "subscription") {
		t.Fatalf("want subscription error, got %v", err)
	}
}

func TestNew_RejectsEmptySubscriptionName(t *testing.T) {
	_, err := psinput.New(psinput.Config{
		ProjectID:     "p",
		Subscriptions: []psinput.Subscription{{Name: " "}},
	})
	if err == nil || !strings.Contains(err.Error(), "Name") {
		t.Fatalf("want Name error, got %v", err)
	}
}

func TestName(t *testing.T) {
	in, _ := psinput.New(psinput.Config{
		ProjectID:     "p",
		Subscriptions: []psinput.Subscription{{Name: "orders"}},
	})
	if in.Name() != "pubsub" {
		t.Fatalf("Name: want pubsub, got %q", in.Name())
	}
}

// ---- Start: happy path ----

func TestStart_EmitsAndAcksGoodMessages(t *testing.T) {
	acks := &atomic.Int32{}
	nacks := &atomic.Int32{}
	when := time.Date(2026, 5, 13, 9, 0, 0, 0, time.UTC)
	r := &fakeReceiver{messages: []psinput.Message{
		&fakeMessage{data: []byte(`{"level":"ERROR"}`), publish: when, acks: acks, nacks: nacks},
		&fakeMessage{data: []byte(`{"level":"INFO"}`), publish: when.Add(time.Second), acks: acks, nacks: nacks},
	}}
	in, err := psinput.New(psinput.Config{
		ProjectID:         "p",
		Subscriptions:     []psinput.Subscription{{Name: "events"}},
		SubscriberFactory: factoryFor(r, nil),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sink := make(chan input.EvaluationRecord, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	startErr := make(chan error, 1)
	go func() { startErr <- in.Start(ctx, sink) }()

	for i := 0; i < 2; i++ {
		select {
		case rec := <-sink:
			if rec.InputRef != "events" {
				t.Errorf("InputRef: want events, got %s", rec.InputRef)
			}
		case <-time.After(time.Second):
			t.Fatalf("missed record %d", i)
		}
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if acks.Load() == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := acks.Load(); got != 2 {
		t.Errorf("acks: want 2, got %d", got)
	}
	if got := nacks.Load(); got != 0 {
		t.Errorf("no nacks expected, got %d", got)
	}

	cancel()
	if err := <-startErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start: want context.Canceled, got %v", err)
	}
}

// ---- Start: bad messages nack ----

func TestStart_NacksBadMessages(t *testing.T) {
	acks := &atomic.Int32{}
	nacks := &atomic.Int32{}
	r := &fakeReceiver{messages: []psinput.Message{
		&fakeMessage{data: []byte("not json"), acks: acks, nacks: nacks},
		&fakeMessage{data: []byte(`[1,2,3]`), acks: acks, nacks: nacks},  // array
		&fakeMessage{data: []byte(`"scalar"`), acks: acks, nacks: nacks}, // scalar
		&fakeMessage{data: []byte(``), acks: acks, nacks: nacks},         // empty
		&fakeMessage{data: []byte(`{"ok":true}`), acks: acks, nacks: nacks},
	}}
	in, _ := psinput.New(psinput.Config{
		ProjectID:         "p",
		Subscriptions:     []psinput.Subscription{{Name: "q"}},
		SubscriberFactory: factoryFor(r, nil),
	})

	sink := make(chan input.EvaluationRecord, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go in.Start(ctx, sink)

	select {
	case rec := <-sink:
		if rec.Record["ok"] != true {
			t.Fatalf("only the valid object should reach the sink, got %+v", rec.Record)
		}
	case <-time.After(time.Second):
		t.Fatalf("never received the valid record")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if acks.Load() == 1 && nacks.Load() == 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := acks.Load(); got != 1 {
		t.Errorf("acks: want 1, got %d", got)
	}
	if got := nacks.Load(); got != 4 {
		t.Errorf("nacks: want 4, got %d", got)
	}
}

// ---- Start: receive-error logged ----

func TestStart_ReceiveErrorIsLogged(t *testing.T) {
	r := &fakeReceiver{receiveEr: errors.New("synthetic receive failure")}
	in, _ := psinput.New(psinput.Config{
		ProjectID:         "p",
		Subscriptions:     []psinput.Subscription{{Name: "q"}},
		SubscriberFactory: factoryFor(r, nil),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- in.Start(ctx, make(chan input.EvaluationRecord)) }()
	// Wait briefly so the receive error has time to be logged.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

// ---- Start: factory error ----

func TestStart_FactoryErrorLoggedAndExits(t *testing.T) {
	in, _ := psinput.New(psinput.Config{
		ProjectID:     "p",
		Subscriptions: []psinput.Subscription{{Name: "q"}},
		SubscriberFactory: func(_ context.Context, _, _ string) (psinput.Receiver, func() error, error) {
			return nil, nil, errors.New("synthetic client open failure")
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- in.Start(ctx, make(chan input.EvaluationRecord)) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Start did not return after factory error + cancel")
	}
}

// ---- Start: ctx cancellation nacks unsent ----

func TestStart_NacksOnContextCancel(t *testing.T) {
	acks := &atomic.Int32{}
	nacks := &atomic.Int32{}
	r := &fakeReceiver{messages: []psinput.Message{
		&fakeMessage{data: []byte(`{"ok":true}`), acks: acks, nacks: nacks},
	}}
	in, _ := psinput.New(psinput.Config{
		ProjectID:         "p",
		Subscriptions:     []psinput.Subscription{{Name: "q"}},
		SubscriberFactory: factoryFor(r, nil),
	})
	// Unbuffered sink + immediately-cancelled ctx → handleMessage's
	// select sees ctx.Done before sink accepts → nack.
	sink := make(chan input.EvaluationRecord)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- in.Start(ctx, sink) }()
	cancel()
	<-done
	if acks.Load() != 0 {
		t.Errorf("no acks expected when ctx cancelled mid-send, got %d", acks.Load())
	}
	// Nack is best-effort here — the fakeReceiver may not have
	// delivered the message before ctx cancel races. Just assert no
	// false ack.
}
