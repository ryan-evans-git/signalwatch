package kafka_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kg "github.com/segmentio/kafka-go"

	"github.com/ryan-evans-git/signalwatch/internal/input"
	kafkainput "github.com/ryan-evans-git/signalwatch/internal/input/stream/kafka"
)

// ---- fakeReader ----

// fakeReader replays a fixed slice of (message, error) pairs from its
// Messages slice, then returns context.Canceled when the slice is
// exhausted so the consumer loop exits.
type fakeReader struct {
	mu       sync.Mutex
	messages []fakeMsg
	idx      atomic.Int32
	closes   atomic.Int32
}

type fakeMsg struct {
	msg kg.Message
	err error
}

func (r *fakeReader) ReadMessage(ctx context.Context) (kg.Message, error) {
	i := int(r.idx.Add(1)) - 1
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.messages) {
		// Block until ctx cancels so the loop exits cleanly.
		<-ctx.Done()
		return kg.Message{}, ctx.Err()
	}
	m := r.messages[i]
	return m.msg, m.err
}

func (r *fakeReader) Close() error {
	r.closes.Add(1)
	return nil
}

func newFakeReaderFactory(messages []fakeMsg) (*fakeReader, func(cfg kg.ReaderConfig) kafkainput.Reader) {
	r := &fakeReader{messages: messages}
	return r, func(cfg kg.ReaderConfig) kafkainput.Reader { return r }
}

// ---- New ----

func TestNew_RejectsMissingBrokers(t *testing.T) {
	_, err := kafkainput.New(kafkainput.Config{
		Topics: []kafkainput.Topic{{Name: "x", GroupID: "g"}},
	})
	if err == nil || !strings.Contains(err.Error(), "brokers required") {
		t.Fatalf("want brokers-required error, got %v", err)
	}
}

func TestNew_RejectsNoTopics(t *testing.T) {
	_, err := kafkainput.New(kafkainput.Config{Brokers: []string{"localhost:9092"}})
	if err == nil || !strings.Contains(err.Error(), "topic required") {
		t.Fatalf("want topic-required error, got %v", err)
	}
}

func TestNew_RejectsEmptyTopicName(t *testing.T) {
	_, err := kafkainput.New(kafkainput.Config{
		Brokers: []string{"localhost:9092"},
		Topics:  []kafkainput.Topic{{Name: " ", GroupID: "g"}},
	})
	if err == nil || !strings.Contains(err.Error(), "topic name required") {
		t.Fatalf("want topic-name error, got %v", err)
	}
}

func TestNew_RejectsEmptyGroupID(t *testing.T) {
	_, err := kafkainput.New(kafkainput.Config{
		Brokers: []string{"localhost:9092"},
		Topics:  []kafkainput.Topic{{Name: "orders"}},
	})
	if err == nil || !strings.Contains(err.Error(), "GroupID") {
		t.Fatalf("want groupID error, got %v", err)
	}
}

func TestName(t *testing.T) {
	in, err := kafkainput.New(kafkainput.Config{
		Brokers: []string{"localhost:9092"},
		Topics:  []kafkainput.Topic{{Name: "orders", GroupID: "g"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if in.Name() != "kafka" {
		t.Fatalf("Name: want kafka, got %q", in.Name())
	}
}

// ---- SASL config validation ----

func TestNew_AcceptsMSKSASLConfig(t *testing.T) {
	_, err := kafkainput.New(kafkainput.Config{
		Brokers: []string{"b-1.cluster.kafka.us-east-1.amazonaws.com:9098"},
		Topics:  []kafkainput.Topic{{Name: "orders", GroupID: "g"}},
		SASL:    &kafkainput.SASLConfig{Mechanism: "AWS_MSK_IAM", Region: "us-east-1"},
	})
	if err != nil {
		t.Fatalf("MSK config should validate: %v", err)
	}
}

func TestNew_RejectsUnknownSASLMechanism(t *testing.T) {
	_, err := kafkainput.New(kafkainput.Config{
		Brokers: []string{"b:9092"},
		Topics:  []kafkainput.Topic{{Name: "t", GroupID: "g"}},
		SASL:    &kafkainput.SASLConfig{Mechanism: "SCRAM-SHA-512", Region: "us-east-1"},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported SASL mechanism") {
		t.Fatalf("want unsupported-mechanism error, got %v", err)
	}
}

func TestNew_RejectsMSKWithoutRegion(t *testing.T) {
	_, err := kafkainput.New(kafkainput.Config{
		Brokers: []string{"b:9098"},
		Topics:  []kafkainput.Topic{{Name: "t", GroupID: "g"}},
		SASL:    &kafkainput.SASLConfig{Mechanism: "AWS_MSK_IAM"},
	})
	if err == nil || !strings.Contains(err.Error(), "Region") {
		t.Fatalf("want region-required error, got %v", err)
	}
}

// ---- SASL wires through to ReaderConfig.Dialer ----

// captureReader returns a factory that captures the ReaderConfig
// passed to it. The returned func blocks until the factory is called
// (or ctx cancels); using a channel keeps the read race-free without
// a mutex.
func captureReader() (factory func(kg.ReaderConfig) kafkainput.Reader, wait func(*testing.T) kg.ReaderConfig) {
	ch := make(chan kg.ReaderConfig, 1)
	factory = func(cfg kg.ReaderConfig) kafkainput.Reader {
		select {
		case ch <- cfg:
		default:
		}
		return &fakeReader{}
	}
	wait = func(t *testing.T) kg.ReaderConfig {
		t.Helper()
		select {
		case cfg := <-ch:
			return cfg
		case <-time.After(2 * time.Second):
			t.Fatalf("factory never invoked")
			return kg.ReaderConfig{}
		}
	}
	return
}

func TestStart_MSKConfigWiresDialer(t *testing.T) {
	factory, wait := captureReader()
	in, err := kafkainput.New(kafkainput.Config{
		Brokers:   []string{"b-1.cluster.kafka.us-east-1.amazonaws.com:9098"},
		Topics:    []kafkainput.Topic{{Name: "orders", GroupID: "g"}},
		SASL:      &kafkainput.SASLConfig{Mechanism: "AWS_MSK_IAM", Region: "us-east-1"},
		NewReader: factory,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = in.Start(ctx, make(chan input.EvaluationRecord)) }()
	captured := wait(t)
	if captured.Dialer == nil {
		t.Fatalf("MSK config should yield a non-nil Dialer in ReaderConfig")
	}
	if captured.Dialer.SASLMechanism == nil {
		t.Errorf("Dialer.SASLMechanism is nil")
	} else if captured.Dialer.SASLMechanism.Name() != "AWS_MSK_IAM" {
		t.Errorf("mechanism name: %q", captured.Dialer.SASLMechanism.Name())
	}
	if captured.Dialer.TLS == nil {
		t.Errorf("Dialer.TLS must be set for MSK")
	}
}

func TestStart_NoSASL_LeavesDialerNil(t *testing.T) {
	// Without SASL config, ReaderConfig.Dialer stays at its zero
	// value so kafka-go uses its built-in default.
	factory, wait := captureReader()
	in, _ := kafkainput.New(kafkainput.Config{
		Brokers:   []string{"localhost:9092"},
		Topics:    []kafkainput.Topic{{Name: "orders", GroupID: "g"}},
		NewReader: factory,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = in.Start(ctx, make(chan input.EvaluationRecord)) }()
	captured := wait(t)
	if captured.Dialer != nil {
		t.Errorf("non-MSK config should not set a Dialer, got %+v", captured.Dialer)
	}
}

// ---- Start: happy path ----

func TestStart_EmitsRecordsFromJSONMessages(t *testing.T) {
	when := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	r, factory := newFakeReaderFactory([]fakeMsg{
		{msg: kg.Message{Value: []byte(`{"level":"ERROR","host":"web-1"}`), Time: when}},
		{msg: kg.Message{Value: []byte(`{"level":"INFO","host":"web-2"}`), Time: when.Add(time.Second)}},
	})

	in, err := kafkainput.New(kafkainput.Config{
		Brokers:   []string{"unused"},
		Topics:    []kafkainput.Topic{{Name: "events", GroupID: "g"}},
		NewReader: factory,
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
				t.Errorf("InputRef: want events, got %q", rec.InputRef)
			}
			if _, ok := rec.Record["level"]; !ok {
				t.Errorf("record missing 'level' key: %+v", rec.Record)
			}
		case <-time.After(time.Second):
			t.Fatalf("missed record %d", i)
		}
	}

	cancel()
	if err := <-startErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start: want context.Canceled, got %v", err)
	}
	if r.closes.Load() != 1 {
		t.Errorf("Reader.Close() should be called exactly once, got %d", r.closes.Load())
	}
}

// ---- Start: bad messages dropped ----

func TestStart_DropsNonJSONAndNonObjectMessages(t *testing.T) {
	_, factory := newFakeReaderFactory([]fakeMsg{
		{msg: kg.Message{Value: []byte("not json at all")}},
		{msg: kg.Message{Value: []byte("")}},               // empty
		{msg: kg.Message{Value: []byte(`[1,2,3]`)}},        // array, not object
		{msg: kg.Message{Value: []byte(`"plain-string"`)}}, // scalar
		{msg: kg.Message{Value: []byte(`{"ok":true}`)}},    // good
	})

	in, _ := kafkainput.New(kafkainput.Config{
		Brokers:   []string{"u"},
		Topics:    []kafkainput.Topic{{Name: "t", GroupID: "g"}},
		NewReader: factory,
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
		t.Fatalf("never received the one valid record")
	}

	// No further records should arrive — drain briefly.
	select {
	case rec := <-sink:
		t.Fatalf("unexpected extra record: %+v", rec.Record)
	case <-time.After(100 * time.Millisecond):
	}
}

// ---- Start: transient read errors logged and skipped ----

func TestStart_TransientReadErrorIsLoggedAndSkipped(t *testing.T) {
	_, factory := newFakeReaderFactory([]fakeMsg{
		{err: errors.New("synthetic transient broker hiccup")},
		{msg: kg.Message{Value: []byte(`{"after":"hiccup"}`)}},
	})

	in, _ := kafkainput.New(kafkainput.Config{
		Brokers:   []string{"u"},
		Topics:    []kafkainput.Topic{{Name: "t", GroupID: "g"}},
		NewReader: factory,
	})

	sink := make(chan input.EvaluationRecord, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go in.Start(ctx, sink)

	select {
	case rec := <-sink:
		if rec.Record["after"] != "hiccup" {
			t.Fatalf("post-error record: %+v", rec.Record)
		}
	case <-time.After(time.Second):
		t.Fatalf("never received the post-error record")
	}
}

// ---- Start: ctx.Done exits cleanly ----

func TestStart_ExitsOnContextDone(t *testing.T) {
	_, factory := newFakeReaderFactory(nil) // empty -> reader blocks on ctx

	in, _ := kafkainput.New(kafkainput.Config{
		Brokers:   []string{"u"},
		Topics:    []kafkainput.Topic{{Name: "t", GroupID: "g"}},
		NewReader: factory,
	})

	sink := make(chan input.EvaluationRecord)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- in.Start(ctx, sink) }()

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

// ---- Start: fans out per topic ----

func TestStart_OneGoroutinePerTopic(t *testing.T) {
	// Multiple topics get their own reader; we use a factory that
	// records which topics it was asked for.
	var (
		mu     sync.Mutex
		topics []string
	)
	factory := func(cfg kg.ReaderConfig) kafkainput.Reader {
		mu.Lock()
		topics = append(topics, cfg.Topic)
		mu.Unlock()
		// Empty reader — blocks on ctx.
		return &fakeReader{}
	}

	in, _ := kafkainput.New(kafkainput.Config{
		Brokers: []string{"u"},
		Topics: []kafkainput.Topic{
			{Name: "orders", GroupID: "g1"},
			{Name: "billing", GroupID: "g2"},
		},
		NewReader: factory,
	})

	sink := make(chan input.EvaluationRecord)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = in.Start(ctx, sink); close(done) }()

	// Wait briefly for both readers to register.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(topics)
		mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	got := append([]string(nil), topics...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected 2 topics registered, got %v", got)
	}

	cancel()
	<-done
}
