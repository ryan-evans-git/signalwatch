// Package rabbitmq provides a streaming input that consumes from one
// or more RabbitMQ (AMQP 0.9.1) queues. Each message body is parsed
// as JSON into a rule.Record and emitted to the engine sink with
// InputRef = queue's configured Name.
//
// Wire-format: messages are expected to be JSON objects. Non-JSON or
// non-object payloads are rejected with no requeue (so they hit a
// dead-letter exchange if one is bound). Successful deliveries are
// ack'd. Channel-level errors are logged and the consumer keeps
// reading.
package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
)

// Queue configures one RabbitMQ queue this input consumes from.
type Queue struct {
	// Name becomes the EvaluationRecord InputRef so engine rules with
	// InputRef="orders" match this queue.
	Name string
	// QueueName is the AMQP queue to consume from. Defaults to Name
	// when empty.
	QueueName string
	// Consumer is the AMQP consumer tag (visible in `rabbitmqctl
	// list_consumers`). Defaults to "signalwatch-" + Name.
	Consumer string
	// PrefetchCount is the AMQP basic.qos count. 0 falls back to 32.
	PrefetchCount int
}

// AMQPConnection is the subset of *amqp.Connection the input uses,
// extracted so tests can inject a fake.
type AMQPConnection interface {
	Channel() (AMQPChannel, error)
	Close() error
	IsClosed() bool
}

// AMQPChannel is the subset of *amqp.Channel the input uses.
type AMQPChannel interface {
	Qos(prefetchCount, prefetchSize int, global bool) error
	Consume(queue, consumer string, autoAck, exclusive, noLocal, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error)
	Close() error
}

// Dialer opens an AMQP connection. Tests inject a fake dialer; the
// default dials via amqp091-go.
type Dialer func(url string) (AMQPConnection, error)

// Config configures the RabbitMQ input.
type Config struct {
	// URL is the AMQP connection URL,
	// e.g. "amqp://guest:guest@localhost:5672/".
	URL string
	// Queues are the queues this input consumes from. One consumer
	// goroutine per queue.
	Queues []Queue
	// Logger receives warning logs. Defaults to slog.Default() when nil.
	Logger *slog.Logger
	// Dialer, if non-nil, replaces the default amqp091-go dialer.
	// Production callers leave it nil.
	Dialer Dialer
	// DialTimeout caps the initial AMQP dial. Defaults to 30s when
	// zero. amqp091-go reconnects internally for stream interruptions;
	// this only affects the initial open.
	DialTimeout time.Duration
}

// Input is the RabbitMQ streaming input.
type Input struct {
	cfg    Config
	logger *slog.Logger
}

// New constructs a RabbitMQ input. Validates config; Start is what
// actually dials AMQP.
func New(cfg Config) (*Input, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("rabbitmq input: URL required")
	}
	if len(cfg.Queues) == 0 {
		return nil, errors.New("rabbitmq input: at least one queue required")
	}
	for i, q := range cfg.Queues {
		if strings.TrimSpace(q.Name) == "" {
			return nil, errors.New("rabbitmq input: queue name required")
		}
		if cfg.Queues[i].QueueName == "" {
			cfg.Queues[i].QueueName = q.Name
		}
		if cfg.Queues[i].Consumer == "" {
			cfg.Queues[i].Consumer = "signalwatch-" + q.Name
		}
		if cfg.Queues[i].PrefetchCount == 0 {
			cfg.Queues[i].PrefetchCount = 32
		}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 30 * time.Second
	}
	if cfg.Dialer == nil {
		cfg.Dialer = defaultDialer
	}
	return &Input{cfg: cfg, logger: cfg.Logger}, nil
}

func (i *Input) Name() string { return "rabbitmq" }

// Start dials AMQP, opens one channel per queue, and consumes in a
// goroutine per queue. Blocks until ctx is cancelled.
func (i *Input) Start(ctx context.Context, sink chan<- input.EvaluationRecord) error {
	conn, err := i.cfg.Dialer(i.cfg.URL)
	if err != nil {
		return fmt.Errorf("rabbitmq input: dial: %w", err)
	}
	defer conn.Close()

	var wg sync.WaitGroup
	for _, q := range i.cfg.Queues {
		wg.Add(1)
		go func(q Queue) {
			defer wg.Done()
			i.runQueue(ctx, conn, q, sink)
		}(q)
	}
	wg.Wait()
	return ctx.Err()
}

func (i *Input) runQueue(ctx context.Context, conn AMQPConnection, q Queue, sink chan<- input.EvaluationRecord) {
	ch, err := conn.Channel()
	if err != nil {
		i.logger.Error("rabbitmq.channel_open", "queue", q.Name, "err", err)
		return
	}
	defer ch.Close()

	if err := ch.Qos(q.PrefetchCount, 0, false); err != nil {
		i.logger.Error("rabbitmq.qos", "queue", q.Name, "err", err)
		return
	}

	deliveries, err := ch.Consume(q.QueueName, q.Consumer, false, false, false, false, nil)
	if err != nil {
		i.logger.Error("rabbitmq.consume", "queue", q.Name, "err", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case d, ok := <-deliveries:
			if !ok {
				// Channel closed by broker / on connection drop. Exit
				// the loop; the caller can restart if it wants.
				return
			}
			i.handleDelivery(q, d, sink, ctx)
		}
	}
}

// handleDelivery decodes one message and ack/rejects appropriately.
func (i *Input) handleDelivery(q Queue, d amqp.Delivery, sink chan<- input.EvaluationRecord, ctx context.Context) {
	rec, ok := parseMessage(d.Body)
	if !ok {
		i.logger.Warn("rabbitmq.bad_message", "queue", q.Name, "delivery_tag", d.DeliveryTag)
		// Reject without requeue so the message either dead-letters
		// (if a DLX is bound) or is dropped.
		_ = d.Reject(false)
		return
	}
	when := d.Timestamp
	if when.IsZero() {
		when = time.Now()
	}
	select {
	case sink <- input.EvaluationRecord{InputRef: q.Name, When: when, Record: rec}:
		_ = d.Ack(false)
	case <-ctx.Done():
		// Don't ack — broker will redeliver to the next consumer when
		// the connection drops.
	}
}

// parseMessage decodes a JSON-object message body into a rule.Record.
// Returns ok=false for empty, non-JSON, and non-object JSON payloads.
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
