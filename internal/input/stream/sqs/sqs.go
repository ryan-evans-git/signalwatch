// Package sqs provides a streaming input that polls one or more SQS
// queues. Each message body is parsed as JSON into a rule.Record and
// emitted to the engine sink with InputRef = queue's configured Name.
//
// Wire-format: messages are expected to be JSON objects. Non-JSON or
// non-object payloads are dropped after a warning log; successful
// messages are deleted from the queue after delivery. Receive errors
// are logged and the loop continues — common during transient AWS
// throttling.
package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
)

// Queue configures one SQS queue the input subscribes to.
type Queue struct {
	// Name becomes the EvaluationRecord InputRef so engine rules with
	// InputRef="orders" match this queue.
	Name string
	// URL is the full SQS queue URL (e.g.
	// "https://sqs.us-east-1.amazonaws.com/123456789012/orders").
	URL string
	// WaitTimeSeconds controls SQS long-polling. 0 falls back to 20
	// (the SQS maximum, recommended for low-latency consumers).
	WaitTimeSeconds int32
	// MaxNumberOfMessages caps the batch size per receive. SQS allows
	// 1-10; 0 falls back to 10.
	MaxNumberOfMessages int32
}

// Client is the subset of *sqs.Client the input uses. Spelled out as
// an interface so tests can inject a fake without the AWS SDK.
type Client interface {
	ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, params *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

// Config configures the SQS input.
type Config struct {
	// Client is an SQS client (or fake). Required.
	Client Client
	// Queues are the queues this input polls. The input fans out one
	// poller goroutine per queue.
	Queues []Queue
	// Logger receives warning logs. Defaults to slog.Default() when nil.
	Logger *slog.Logger
}

// Input is the SQS streaming input.
type Input struct {
	cfg    Config
	logger *slog.Logger
}

// New constructs an SQS input. Returns an error on obviously-broken
// configuration (nil client, no queues, empty queue url/name); Start
// is what actually polls SQS.
func New(cfg Config) (*Input, error) {
	if cfg.Client == nil {
		return nil, errors.New("sqs input: client required")
	}
	if len(cfg.Queues) == 0 {
		return nil, errors.New("sqs input: at least one queue required")
	}
	for i, q := range cfg.Queues {
		if strings.TrimSpace(q.Name) == "" {
			return nil, errors.New("sqs input: queue name required")
		}
		if strings.TrimSpace(q.URL) == "" {
			return nil, errors.New("sqs input: queue " + q.Name + " requires URL")
		}
		if cfg.Queues[i].WaitTimeSeconds == 0 {
			cfg.Queues[i].WaitTimeSeconds = 20
		}
		if cfg.Queues[i].MaxNumberOfMessages == 0 {
			cfg.Queues[i].MaxNumberOfMessages = 10
		}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Input{cfg: cfg, logger: cfg.Logger}, nil
}

func (i *Input) Name() string { return "sqs" }

// Start fans out one poller goroutine per configured queue. Each
// goroutine long-polls SQS in a loop, JSON-decodes message bodies, and
// pushes EvaluationRecords into sink. Messages are deleted from the
// queue on successful delivery. Start blocks until ctx is cancelled.
func (i *Input) Start(ctx context.Context, sink chan<- input.EvaluationRecord) error {
	var wg sync.WaitGroup
	for _, q := range i.cfg.Queues {
		wg.Add(1)
		go func(q Queue) {
			defer wg.Done()
			i.runQueue(ctx, q, sink)
		}(q)
	}
	wg.Wait()
	return ctx.Err()
}

func (i *Input) runQueue(ctx context.Context, q Queue, sink chan<- input.EvaluationRecord) {
	for {
		if ctx.Err() != nil {
			return
		}
		out, err := i.cfg.Client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(q.URL),
			MaxNumberOfMessages: q.MaxNumberOfMessages,
			WaitTimeSeconds:     q.WaitTimeSeconds,
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			i.logger.Warn("sqs.receive_error", "queue", q.Name, "err", err)
			// Brief back-off so a permanently-broken queue doesn't spin
			// the CPU.
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		for _, m := range out.Messages {
			if !i.handleMessage(ctx, q, m, sink) {
				// ctx cancelled mid-loop.
				return
			}
		}
	}
}

// handleMessage parses one SQS message and pushes it into the sink.
// Returns false if ctx cancelled during the send. Messages that decode
// successfully are deleted from the queue; bad messages are logged
// and left in place (SQS will redrive via visibility timeout).
func (i *Input) handleMessage(ctx context.Context, q Queue, m types.Message, sink chan<- input.EvaluationRecord) bool {
	body := ""
	if m.Body != nil {
		body = *m.Body
	}
	rec, ok := parseMessage([]byte(body))
	if !ok {
		i.logger.Warn("sqs.bad_message", "queue", q.Name, "messageId", aws.ToString(m.MessageId))
		return true
	}
	select {
	case sink <- input.EvaluationRecord{InputRef: q.Name, When: time.Now(), Record: rec}:
	case <-ctx.Done():
		return false
	}
	// Best-effort delete. A failed delete only means SQS will redrive
	// the message after the visibility timeout — not fatal, just
	// duplicated downstream.
	if _, err := i.cfg.Client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(q.URL),
		ReceiptHandle: m.ReceiptHandle,
	}); err != nil {
		if !errors.Is(err, context.Canceled) {
			i.logger.Warn("sqs.delete_error", "queue", q.Name, "err", err)
		}
	}
	return true
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
