package rabbitmq_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/ryan-evans-git/signalwatch/internal/input"
	rmqinput "github.com/ryan-evans-git/signalwatch/internal/input/stream/rabbitmq"
)

// ---- fakeConn / fakeChannel ----

type fakeConn struct {
	channelErr   error
	closed       atomic.Bool
	closes       atomic.Int32
	channelCalls atomic.Int32
	makeChan     func() rmqinput.AMQPChannel
}

func (c *fakeConn) Channel() (rmqinput.AMQPChannel, error) {
	c.channelCalls.Add(1)
	if c.channelErr != nil {
		return nil, c.channelErr
	}
	if c.makeChan != nil {
		return c.makeChan(), nil
	}
	return &fakeChannel{}, nil
}
func (c *fakeConn) Close() error   { c.closes.Add(1); c.closed.Store(true); return nil }
func (c *fakeConn) IsClosed() bool { return c.closed.Load() }

type fakeChannel struct {
	qosErr     error
	consumeErr error

	deliveries chan amqp.Delivery
	closed     atomic.Bool
}

func (c *fakeChannel) Qos(_, _ int, _ bool) error {
	return c.qosErr
}
func (c *fakeChannel) Consume(_, _ string, _, _, _, _ bool, _ amqp.Table) (<-chan amqp.Delivery, error) {
	if c.consumeErr != nil {
		return nil, c.consumeErr
	}
	if c.deliveries == nil {
		c.deliveries = make(chan amqp.Delivery)
	}
	return c.deliveries, nil
}
func (c *fakeChannel) Close() error {
	c.closed.Store(true)
	return nil
}

// ackTracker is a fake amqp.Delivery — we can't easily mock the
// Ack/Reject methods on amqp.Delivery itself because they're tied to
// an internal Acknowledger. So we use a wrapper: each fake delivery
// has an explicit Acknowledger that records calls.
type fakeAcker struct {
	mu      sync.Mutex
	acks    []uint64
	rejects []rejectCall
}

type rejectCall struct {
	tag     uint64
	requeue bool
}

func (a *fakeAcker) Ack(tag uint64, _ bool) error {
	a.mu.Lock()
	a.acks = append(a.acks, tag)
	a.mu.Unlock()
	return nil
}
func (a *fakeAcker) Nack(tag uint64, _, _ bool) error { return nil }
func (a *fakeAcker) Reject(tag uint64, requeue bool) error {
	a.mu.Lock()
	a.rejects = append(a.rejects, rejectCall{tag: tag, requeue: requeue})
	a.mu.Unlock()
	return nil
}

func (a *fakeAcker) ackedTags() []uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]uint64(nil), a.acks...)
}
func (a *fakeAcker) rejectCalls() []rejectCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]rejectCall(nil), a.rejects...)
}

func makeDelivery(acker amqp.Acknowledger, tag uint64, body string) amqp.Delivery {
	return amqp.Delivery{
		Acknowledger: acker,
		DeliveryTag:  tag,
		Body:         []byte(body),
	}
}

// ---- New ----

func TestNew_RejectsMissingURL(t *testing.T) {
	_, err := rmqinput.New(rmqinput.Config{
		Queues: []rmqinput.Queue{{Name: "q"}},
	})
	if err == nil || !strings.Contains(err.Error(), "URL required") {
		t.Fatalf("want URL-required error, got %v", err)
	}
}

func TestNew_RejectsNoQueues(t *testing.T) {
	_, err := rmqinput.New(rmqinput.Config{URL: "amqp://u"})
	if err == nil || !strings.Contains(err.Error(), "queue required") {
		t.Fatalf("want queue-required error, got %v", err)
	}
}

func TestNew_RejectsEmptyQueueName(t *testing.T) {
	_, err := rmqinput.New(rmqinput.Config{
		URL:    "amqp://u",
		Queues: []rmqinput.Queue{{Name: " "}},
	})
	if err == nil || !strings.Contains(err.Error(), "queue name required") {
		t.Fatalf("want queue-name error, got %v", err)
	}
}

func TestName(t *testing.T) {
	in, _ := rmqinput.New(rmqinput.Config{
		URL:    "amqp://u",
		Queues: []rmqinput.Queue{{Name: "orders"}},
		Dialer: func(url string) (rmqinput.AMQPConnection, error) { return &fakeConn{}, nil },
	})
	if in.Name() != "rabbitmq" {
		t.Fatalf("Name: want rabbitmq, got %q", in.Name())
	}
}

// ---- Start: dial error ----

func TestStart_DialErrorReturnsImmediately(t *testing.T) {
	in, _ := rmqinput.New(rmqinput.Config{
		URL:    "amqp://nope",
		Queues: []rmqinput.Queue{{Name: "q"}},
		Dialer: func(url string) (rmqinput.AMQPConnection, error) {
			return nil, errors.New("synthetic dial failure")
		},
	})
	err := in.Start(context.Background(), make(chan input.EvaluationRecord))
	if err == nil || !strings.Contains(err.Error(), "dial") {
		t.Fatalf("want dial error, got %v", err)
	}
}

// ---- Start: happy path ----

func TestStart_EmitsAndAcksGoodMessages(t *testing.T) {
	acker := &fakeAcker{}
	ch := &fakeChannel{deliveries: make(chan amqp.Delivery, 2)}
	conn := &fakeConn{makeChan: func() rmqinput.AMQPChannel { return ch }}

	in, err := rmqinput.New(rmqinput.Config{
		URL:    "amqp://u",
		Queues: []rmqinput.Queue{{Name: "events"}},
		Dialer: func(url string) (rmqinput.AMQPConnection, error) { return conn, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sink := make(chan input.EvaluationRecord, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	startErr := make(chan error, 1)
	go func() { startErr <- in.Start(ctx, sink) }()

	ch.deliveries <- makeDelivery(acker, 1, `{"level":"ERROR"}`)
	ch.deliveries <- makeDelivery(acker, 2, `{"level":"INFO"}`)

	for i := 0; i < 2; i++ {
		select {
		case rec := <-sink:
			if rec.InputRef != "events" {
				t.Errorf("InputRef: want events, got %s", rec.InputRef)
			}
			if _, ok := rec.Record["level"]; !ok {
				t.Errorf("missing level in %+v", rec.Record)
			}
		case <-time.After(time.Second):
			t.Fatalf("missed record %d", i)
		}
	}

	// Wait briefly for the acks to land.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(acker.ackedTags()) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := acker.ackedTags(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("acks: want [1 2], got %v", got)
	}
	if len(acker.rejectCalls()) != 0 {
		t.Errorf("no rejects expected, got %v", acker.rejectCalls())
	}

	cancel()
	if err := <-startErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start: want context.Canceled, got %v", err)
	}
}

// ---- Start: bad messages rejected without requeue ----

func TestStart_RejectsBadMessagesWithoutRequeue(t *testing.T) {
	acker := &fakeAcker{}
	ch := &fakeChannel{deliveries: make(chan amqp.Delivery, 4)}
	conn := &fakeConn{makeChan: func() rmqinput.AMQPChannel { return ch }}

	in, _ := rmqinput.New(rmqinput.Config{
		URL:    "amqp://u",
		Queues: []rmqinput.Queue{{Name: "q"}},
		Dialer: func(url string) (rmqinput.AMQPConnection, error) { return conn, nil },
	})

	sink := make(chan input.EvaluationRecord, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go in.Start(ctx, sink)

	ch.deliveries <- makeDelivery(acker, 1, "not json")
	ch.deliveries <- makeDelivery(acker, 2, `[1,2,3]`)     // array
	ch.deliveries <- makeDelivery(acker, 3, `"scalar"`)    // scalar
	ch.deliveries <- makeDelivery(acker, 4, `{"ok":true}`) // good

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
		if len(acker.ackedTags()) == 1 && len(acker.rejectCalls()) == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := acker.ackedTags(); len(got) != 1 || got[0] != 4 {
		t.Errorf("acks: want [4], got %v", got)
	}
	rejects := acker.rejectCalls()
	if len(rejects) != 3 {
		t.Fatalf("rejects: want 3, got %d (%v)", len(rejects), rejects)
	}
	for _, r := range rejects {
		if r.requeue {
			t.Errorf("reject with requeue=true is wrong (poison message would loop): %+v", r)
		}
	}
}

// ---- Start: channel-open error logged, queue exits, others continue ----

func TestStart_ChannelOpenErrorIsLogged(t *testing.T) {
	conn := &fakeConn{channelErr: errors.New("synthetic channel error")}
	in, _ := rmqinput.New(rmqinput.Config{
		URL:    "amqp://u",
		Queues: []rmqinput.Queue{{Name: "q"}},
		Dialer: func(url string) (rmqinput.AMQPConnection, error) { return conn, nil },
	})

	sink := make(chan input.EvaluationRecord)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Start returns ctx.Err() once all goroutines exit; the failed
	// channel-open goroutine exits immediately, so cancel and check.
	startErr := make(chan error, 1)
	go func() { startErr <- in.Start(ctx, sink) }()

	cancel()
	if err := <-startErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start: want context.Canceled, got %v", err)
	}
	if conn.channelCalls.Load() != 1 {
		t.Errorf("Channel(): called %d times, want 1", conn.channelCalls.Load())
	}
}

// ---- Start: deliveries channel close exits cleanly ----

func TestStart_DeliveriesChannelCloseExitsQueueLoop(t *testing.T) {
	ch := &fakeChannel{deliveries: make(chan amqp.Delivery)}
	conn := &fakeConn{makeChan: func() rmqinput.AMQPChannel { return ch }}
	in, _ := rmqinput.New(rmqinput.Config{
		URL:    "amqp://u",
		Queues: []rmqinput.Queue{{Name: "q"}},
		Dialer: func(url string) (rmqinput.AMQPConnection, error) { return conn, nil },
	})

	sink := make(chan input.EvaluationRecord)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startErr := make(chan error, 1)
	go func() { startErr <- in.Start(ctx, sink) }()

	// Closing the deliveries channel should cause runQueue to return.
	close(ch.deliveries)

	// Once the only queue goroutine exits, Start returns. But we still
	// have to provide a way for it to notice — its wait.Wait then
	// triggers a return of ctx.Err(), which is nil if not cancelled.
	// Cancel after a brief moment so we get context.Canceled cleanly.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-startErr:
	case <-time.After(time.Second):
		t.Fatalf("Start did not return after deliveries close + cancel")
	}
}

// ---- Start: ctx.Done exits ----

func TestStart_ExitsOnContextDone(t *testing.T) {
	ch := &fakeChannel{deliveries: make(chan amqp.Delivery)}
	conn := &fakeConn{makeChan: func() rmqinput.AMQPChannel { return ch }}
	in, _ := rmqinput.New(rmqinput.Config{
		URL:    "amqp://u",
		Queues: []rmqinput.Queue{{Name: "q"}},
		Dialer: func(url string) (rmqinput.AMQPConnection, error) { return conn, nil },
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- in.Start(ctx, make(chan input.EvaluationRecord)) }()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("Start did not return after cancel")
	}
}

// ---- Start: Qos error logged ----

func TestStart_QosErrorIsLogged(t *testing.T) {
	ch := &fakeChannel{qosErr: errors.New("synthetic qos")}
	conn := &fakeConn{makeChan: func() rmqinput.AMQPChannel { return ch }}
	in, _ := rmqinput.New(rmqinput.Config{
		URL:    "amqp://u",
		Queues: []rmqinput.Queue{{Name: "q"}},
		Dialer: func(url string) (rmqinput.AMQPConnection, error) { return conn, nil },
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- in.Start(ctx, make(chan input.EvaluationRecord)) }()

	cancel()
	<-done
}

// ---- Start: Consume error logged ----

func TestStart_ConsumeErrorIsLogged(t *testing.T) {
	ch := &fakeChannel{consumeErr: errors.New("synthetic consume")}
	conn := &fakeConn{makeChan: func() rmqinput.AMQPChannel { return ch }}
	in, _ := rmqinput.New(rmqinput.Config{
		URL:    "amqp://u",
		Queues: []rmqinput.Queue{{Name: "q"}},
		Dialer: func(url string) (rmqinput.AMQPConnection, error) { return conn, nil },
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- in.Start(ctx, make(chan input.EvaluationRecord)) }()

	cancel()
	<-done
}
