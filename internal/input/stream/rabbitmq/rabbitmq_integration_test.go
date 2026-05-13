//go:build integration

package rabbitmq_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	tcrabbit "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/ryan-evans-git/signalwatch/internal/input"
	rmqinput "github.com/ryan-evans-git/signalwatch/internal/input/stream/rabbitmq"
)

// One RabbitMQ container per test binary; subtests each create
// their own queue.
var (
	rmqOnce sync.Once
	rmqURL  string
	rmqErr  error
)

const rmqImage = "docker.io/rabbitmq:3.13-alpine"

func amqpURL(t *testing.T) string {
	t.Helper()
	rmqOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		ctr, err := tcrabbit.Run(ctx, rmqImage,
			tcrabbit.WithAdminUsername("guest"),
			tcrabbit.WithAdminPassword("guest"),
		)
		if err != nil {
			rmqErr = fmt.Errorf("start rabbitmq: %w", err)
			return
		}
		url, err := ctr.AmqpURL(ctx)
		if err != nil {
			rmqErr = fmt.Errorf("get amqp url: %w", err)
			return
		}
		// Probe to make sure the broker is actually accepting AMQP
		// connections (testcontainers waits for the management port
		// but AMQP can lag a moment).
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			c, dialErr := amqp.Dial(url)
			if dialErr == nil {
				_ = c.Close()
				rmqURL = url
				return
			}
			time.Sleep(time.Second)
		}
		rmqErr = fmt.Errorf("rabbitmq never accepted AMQP within 45s")
	})
	if rmqErr != nil {
		t.Skipf("rabbitmq testcontainer unavailable: %v", rmqErr)
	}
	return rmqURL
}

var queueSeq atomic.Uint64

func declareQueue(t *testing.T, url string) string {
	t.Helper()
	n := queueSeq.Add(1)
	name := fmt.Sprintf("sw-test-%d", n)
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	defer ch.Close()
	if _, err := ch.QueueDeclare(name, true, false, false, false, nil); err != nil {
		t.Fatalf("queue declare: %v", err)
	}
	return name
}

func publishJSON(t *testing.T, url, queue string, payloads []map[string]any) {
	t.Helper()
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	defer ch.Close()
	for _, p := range payloads {
		body, _ := json.Marshal(p)
		if err := ch.Publish("", queue, false, false, amqp.Publishing{
			ContentType: "application/json",
			Body:        body,
		}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
}

func TestIntegration_ConsumeAndAck(t *testing.T) {
	url := amqpURL(t)
	queue := declareQueue(t, url)

	in, err := rmqinput.New(rmqinput.Config{
		URL:    url,
		Queues: []rmqinput.Queue{{Name: queue}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	publishJSON(t, url, queue, []map[string]any{
		{"level": "ERROR", "host": "web-1"},
		{"level": "INFO", "host": "web-2"},
		{"level": "ERROR", "host": "web-3"},
	})

	sink := make(chan input.EvaluationRecord, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { _ = in.Start(ctx, sink); close(done) }()

	got := 0
	deadline := time.Now().Add(30 * time.Second)
	for got < 3 && time.Now().Before(deadline) {
		select {
		case rec := <-sink:
			if rec.InputRef != queue {
				t.Errorf("InputRef: want %s, got %s", queue, rec.InputRef)
			}
			if _, ok := rec.Record["level"]; !ok {
				t.Errorf("missing level in %+v", rec.Record)
			}
			got++
		case <-time.After(2 * time.Second):
			// keep waiting up to deadline
		}
	}
	if got != 3 {
		t.Fatalf("got %d records, want 3", got)
	}

	cancel()
	<-done

	// Probe the queue — all three messages should have been acked
	// and the queue should be empty. Use a fresh connection.
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	defer ch.Close()
	// QueueDeclare with Passive=true returns metadata for an existing
	// queue without modifying it; preferred over deprecated QueueInspect.
	q, err := ch.QueueDeclarePassive(queue, true, false, false, false, nil)
	if err != nil {
		t.Fatalf("queue declare passive: %v", err)
	}
	if q.Messages != 0 {
		t.Errorf("queue not drained — %d messages remain", q.Messages)
	}
}
