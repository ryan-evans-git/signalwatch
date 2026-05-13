//go:build integration

package sqs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/testcontainers/testcontainers-go"
	tclocalstack "github.com/testcontainers/testcontainers-go/modules/localstack"

	"github.com/ryan-evans-git/signalwatch/internal/input"
	sqsinput "github.com/ryan-evans-git/signalwatch/internal/input/stream/sqs"
)

// One localstack container per test binary; subtests each create
// their own queue.
var (
	stackOnce     sync.Once
	stackEndpoint string
	stackErr      error
)

const localstackImage = "docker.io/localstack/localstack:3.8"

func endpoint(t *testing.T) string {
	t.Helper()
	stackOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		ctr, err := tclocalstack.Run(ctx, localstackImage,
			testcontainers.WithEnv(map[string]string{
				"SERVICES": "sqs",
			}),
		)
		if err != nil {
			stackErr = fmt.Errorf("start localstack: %w", err)
			return
		}
		// Default localstack port is 4566.
		host, err := ctr.Host(ctx)
		if err != nil {
			stackErr = fmt.Errorf("host: %w", err)
			return
		}
		port, err := ctr.MappedPort(ctx, "4566/tcp")
		if err != nil {
			stackErr = fmt.Errorf("port: %w", err)
			return
		}
		stackEndpoint = fmt.Sprintf("http://%s:%s", host, port.Port())
	})
	if stackErr != nil {
		t.Skipf("localstack unavailable: %v", stackErr)
	}
	return stackEndpoint
}

func newClient(t *testing.T) *awssqs.Client {
	t.Helper()
	ep := endpoint(t)
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	return awssqs.NewFromConfig(cfg, func(o *awssqs.Options) {
		o.BaseEndpoint = aws.String(ep)
	})
}

var queueSeq atomic.Uint64

func createQueue(t *testing.T, c *awssqs.Client) (name, url string) {
	t.Helper()
	n := queueSeq.Add(1)
	name = fmt.Sprintf("sw-test-%d", n)
	out, err := c.CreateQueue(context.Background(), &awssqs.CreateQueueInput{QueueName: aws.String(name)})
	if err != nil {
		t.Fatalf("CreateQueue: %v", err)
	}
	return name, aws.ToString(out.QueueUrl)
}

func sendJSON(t *testing.T, c *awssqs.Client, url string, payloads []map[string]any) {
	t.Helper()
	for _, p := range payloads {
		b, _ := json.Marshal(p)
		if _, err := c.SendMessage(context.Background(), &awssqs.SendMessageInput{
			QueueUrl:    aws.String(url),
			MessageBody: aws.String(string(b)),
		}); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
	}
}

func TestIntegration_ConsumeAndDeleteJSONMessages(t *testing.T) {
	c := newClient(t)
	name, url := createQueue(t, c)

	in, err := sqsinput.New(sqsinput.Config{
		Client: c,
		Queues: []sqsinput.Queue{{
			Name: name, URL: url,
			WaitTimeSeconds: 1, // short long-poll for faster tests
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sendJSON(t, c, url, []map[string]any{
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
			if rec.InputRef != name {
				t.Errorf("InputRef: want %s, got %s", name, rec.InputRef)
			}
			if _, ok := rec.Record["level"]; !ok {
				t.Errorf("missing level key in %+v", rec.Record)
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

	// After cancel, the queue should be empty (we deleted each message
	// after delivery). Verify with a fresh receive against the real
	// localstack queue.
	probe, err := c.ReceiveMessage(context.Background(), &awssqs.ReceiveMessageInput{
		QueueUrl:            aws.String(url),
		MaxNumberOfMessages: 10,
		WaitTimeSeconds:     1,
	})
	if err != nil {
		t.Fatalf("probe receive: %v", err)
	}
	if len(probe.Messages) != 0 {
		t.Errorf("queue not drained — messages remain: %d", len(probe.Messages))
	}
}
