// Package kafka provides a streaming input that consumes from one or
// more Kafka topics. Each message body is parsed as JSON into a
// rule.Record and emitted to the engine sink with InputRef = topic name.
//
// Wire-format: messages are expected to be JSON objects. Non-JSON or
// non-object payloads are dropped after a warning log; the consumer
// keeps going so a single bad message doesn't take the whole input down.
package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	kg "github.com/segmentio/kafka-go"

	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
)

// Topic configures one topic the input subscribes to.
type Topic struct {
	// Name is the topic on the broker. Also becomes the EvaluationRecord
	// InputRef so engine rules with InputRef="orders" match topic
	// "orders".
	Name string
	// GroupID is the Kafka consumer group. Required (Kafka rejects
	// anonymous consumers in any consumer-group-managed cluster).
	GroupID string
	// MinBytes / MaxBytes are forwarded to kafka.ReaderConfig. Sensible
	// defaults applied when zero.
	MinBytes int
	MaxBytes int
}

// Config configures the Kafka input.
type Config struct {
	// Brokers is the bootstrap broker list, e.g. []string{"localhost:9092"}.
	Brokers []string
	// Topics are the topics this input subscribes to. The input fans
	// out one reader goroutine per topic.
	Topics []Topic
	// Logger receives warning logs (bad messages, transient errors).
	// Defaults to slog.Default() when nil.
	Logger *slog.Logger
	// DialTimeout caps how long the initial Kafka dial takes per topic.
	// Zero defaults to 10s.
	DialTimeout time.Duration
	// NewReader, if non-nil, is used in place of the default
	// kafka.NewReader. Tests inject this to substitute an in-memory
	// reader without spinning up a real broker. Production should
	// leave it nil.
	NewReader func(cfg kg.ReaderConfig) Reader
}

// Reader is the subset of *kafka.Reader the input uses. Spelled out as
// an interface so tests can inject a fake.
type Reader interface {
	ReadMessage(ctx context.Context) (kg.Message, error)
	Close() error
}

// Input is the kafka streaming input.
type Input struct {
	cfg    Config
	logger *slog.Logger
}

// New constructs a Kafka input. Returns an error only on obviously-
// broken configuration (no brokers, no topics, empty group id); Start
// is what actually dials Kafka.
func New(cfg Config) (*Input, error) {
	if len(cfg.Brokers) == 0 {
		return nil, errors.New("kafka input: brokers required")
	}
	if len(cfg.Topics) == 0 {
		return nil, errors.New("kafka input: at least one topic required")
	}
	for i, t := range cfg.Topics {
		if strings.TrimSpace(t.Name) == "" {
			return nil, errors.New("kafka input: topic name required")
		}
		if strings.TrimSpace(t.GroupID) == "" {
			return nil, errors.New("kafka input: topic " + t.Name + " requires GroupID")
		}
		if cfg.Topics[i].MinBytes == 0 {
			cfg.Topics[i].MinBytes = 1
		}
		if cfg.Topics[i].MaxBytes == 0 {
			cfg.Topics[i].MaxBytes = 10 << 20 // 10 MiB
		}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.NewReader == nil {
		cfg.NewReader = func(rcfg kg.ReaderConfig) Reader {
			return kg.NewReader(rcfg)
		}
	}
	return &Input{cfg: cfg, logger: cfg.Logger}, nil
}

func (i *Input) Name() string { return "kafka" }

// Start fans out one reader goroutine per configured topic. Each
// goroutine reads from Kafka in a loop, decodes the message body as
// JSON, and pushes one EvaluationRecord per message into sink. Start
// blocks until ctx is cancelled.
func (i *Input) Start(ctx context.Context, sink chan<- input.EvaluationRecord) error {
	var wg sync.WaitGroup
	for _, topic := range i.cfg.Topics {
		wg.Add(1)
		go func(t Topic) {
			defer wg.Done()
			i.runTopic(ctx, t, sink)
		}(topic)
	}
	wg.Wait()
	return ctx.Err()
}

func (i *Input) runTopic(ctx context.Context, t Topic, sink chan<- input.EvaluationRecord) {
	reader := i.cfg.NewReader(kg.ReaderConfig{
		Brokers:  i.cfg.Brokers,
		Topic:    t.Name,
		GroupID:  t.GroupID,
		MinBytes: t.MinBytes,
		MaxBytes: t.MaxBytes,
	})
	defer reader.Close()

	for {
		msg, err := reader.ReadMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
				errors.Is(err, io.EOF) {
				return
			}
			// Log and continue — Kafka readers commonly emit transient
			// errors that resolve themselves on the next poll.
			i.logger.Warn("kafka.read_error", "topic", t.Name, "err", err)
			continue
		}

		rec, ok := parseMessage(msg.Value)
		if !ok {
			i.logger.Warn("kafka.bad_message",
				"topic", t.Name,
				"partition", msg.Partition,
				"offset", msg.Offset)
			continue
		}

		when := msg.Time
		if when.IsZero() {
			when = time.Now()
		}
		select {
		case sink <- input.EvaluationRecord{InputRef: t.Name, When: when, Record: rec}:
		case <-ctx.Done():
			return
		}
	}
}

// parseMessage decodes a JSON-object message body into a rule.Record.
// Returns ok=false for empty bodies, non-JSON, and non-object JSON
// (arrays, scalars).
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
