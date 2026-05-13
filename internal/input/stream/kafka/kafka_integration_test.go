//go:build integration

package kafka_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kg "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ryan-evans-git/signalwatch/internal/input"
	kafkainput "github.com/ryan-evans-git/signalwatch/internal/input/stream/kafka"
)

// One Kafka container is shared across all integration subtests in a
// `go test` invocation. Each subtest names its topic / group uniquely
// to avoid cross-test pollution.
var (
	kafkaOnce    sync.Once
	kafkaBrokers []string
	kafkaErr     error
)

const kafkaImage = "docker.io/confluentinc/confluent-local:7.6.1"

func brokers(t *testing.T) []string {
	t.Helper()
	kafkaOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		c, err := tckafka.Run(ctx, kafkaImage,
			testcontainers.WithWaitStrategy(
				wait.ForLog("Kafka Server started").
					WithStartupTimeout(2*time.Minute),
			),
		)
		if err != nil {
			kafkaErr = fmt.Errorf("start kafka: %w", err)
			return
		}
		bs, err := c.Brokers(ctx)
		if err != nil {
			kafkaErr = fmt.Errorf("get brokers: %w", err)
			return
		}
		kafkaBrokers = bs
	})
	if kafkaErr != nil {
		t.Skipf("kafka testcontainer unavailable: %v", kafkaErr)
	}
	return kafkaBrokers
}

var topicSeq atomic.Uint64

func uniqueTopic(t *testing.T) (topic, group string) {
	t.Helper()
	n := topicSeq.Add(1)
	return fmt.Sprintf("topic-%s-%d", t.Name(), n), fmt.Sprintf("group-%s-%d", t.Name(), n)
}

// createTopic uses kafka-go's admin API to create the topic with one
// partition so the consumer doesn't sit waiting for the auto-create
// dance.
func createTopic(t *testing.T, brokerAddr, topic string) {
	t.Helper()
	conn, err := kg.Dial("tcp", brokerAddr)
	if err != nil {
		t.Fatalf("kafka dial: %v", err)
	}
	defer conn.Close()
	if err := conn.CreateTopics(kg.TopicConfig{Topic: topic, NumPartitions: 1, ReplicationFactor: 1}); err != nil {
		t.Fatalf("create topic %s: %v", topic, err)
	}
	// Brief wait for the topic metadata to propagate; smaller brokers
	// occasionally race here.
	time.Sleep(500 * time.Millisecond)
}

// produce writes JSON-encoded messages into topic.
func produce(t *testing.T, brokerAddr, topic string, payloads []map[string]any) {
	t.Helper()
	w := &kg.Writer{
		Addr:         kg.TCP(brokerAddr),
		Topic:        topic,
		Balancer:     &kg.LeastBytes{},
		RequiredAcks: kg.RequireAll,
	}
	defer w.Close()
	msgs := make([]kg.Message, 0, len(payloads))
	for _, p := range payloads {
		body, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		msgs = append(msgs, kg.Message{Value: body})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := w.WriteMessages(ctx, msgs...); err != nil {
		t.Fatalf("write messages: %v", err)
	}
}

func TestIntegration_ConsumeProducesRecords(t *testing.T) {
	bs := brokers(t)
	topic, group := uniqueTopic(t)
	createTopic(t, bs[0], topic)

	in, err := kafkainput.New(kafkainput.Config{
		Brokers: bs,
		Topics: []kafkainput.Topic{
			{Name: topic, GroupID: group, MinBytes: 1, MaxBytes: 1 << 20},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sink := make(chan input.EvaluationRecord, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { _ = in.Start(ctx, sink); close(done) }()

	produce(t, bs[0], topic, []map[string]any{
		{"level": "ERROR", "host": "web-1"},
		{"level": "INFO", "host": "web-2"},
		{"level": "ERROR", "host": "web-3"},
	})

	got := 0
	deadline := time.Now().Add(60 * time.Second)
	for got < 3 && time.Now().Before(deadline) {
		select {
		case rec := <-sink:
			if rec.InputRef != topic {
				t.Errorf("InputRef: want %s, got %s", topic, rec.InputRef)
			}
			if _, ok := rec.Record["level"]; !ok {
				t.Errorf("missing level key in %+v", rec.Record)
			}
			got++
		case <-time.After(2 * time.Second):
			// Continue waiting up to deadline.
		}
	}
	if got != 3 {
		t.Fatalf("got %d records, want 3", got)
	}

	cancel()
	<-done
}

func TestIntegration_BadMessageIsDroppedNotFatal(t *testing.T) {
	bs := brokers(t)
	topic, group := uniqueTopic(t)
	createTopic(t, bs[0], topic)

	in, _ := kafkainput.New(kafkainput.Config{
		Brokers: bs,
		Topics:  []kafkainput.Topic{{Name: topic, GroupID: group}},
	})

	sink := make(chan input.EvaluationRecord, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { _ = in.Start(ctx, sink); close(done) }()

	// Send a bad message followed by a good one; only the good one
	// should reach the sink.
	w := &kg.Writer{Addr: kg.TCP(bs[0]), Topic: topic, RequiredAcks: kg.RequireAll}
	defer w.Close()
	wctx, wc := context.WithTimeout(context.Background(), 30*time.Second)
	defer wc()
	if err := w.WriteMessages(wctx,
		kg.Message{Value: []byte("not json")},
		kg.Message{Value: []byte(`{"ok":true}`)},
	); err != nil {
		t.Fatalf("WriteMessages: %v", err)
	}

	select {
	case rec := <-sink:
		if rec.Record["ok"] != true {
			t.Fatalf("only the valid object should reach the sink, got %+v", rec.Record)
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("never received the valid record")
	}

	cancel()
	<-done
}
