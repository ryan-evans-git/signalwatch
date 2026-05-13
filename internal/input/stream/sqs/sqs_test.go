package sqs_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/ryan-evans-git/signalwatch/internal/input"
	sqsinput "github.com/ryan-evans-git/signalwatch/internal/input/stream/sqs"
)

// ---- fakeClient ----

// fakeClient returns a fixed sequence of ReceiveMessage results from
// its Receives slice; after exhaustion, it blocks until ctx cancels so
// the poller loop exits cleanly. DeleteMessage just records its
// argument.
type fakeClient struct {
	mu       sync.Mutex
	receives []fakeReceive
	idx      atomic.Int32

	deletedMu sync.Mutex
	deleted   []string // ReceiptHandles passed to DeleteMessage
	deleteErr error    // if set, DeleteMessage returns this error
}

type fakeReceive struct {
	out *awssqs.ReceiveMessageOutput
	err error
}

func (c *fakeClient) ReceiveMessage(ctx context.Context, params *awssqs.ReceiveMessageInput, _ ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	i := int(c.idx.Add(1)) - 1
	c.mu.Lock()
	defer c.mu.Unlock()
	if i >= len(c.receives) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	r := c.receives[i]
	if r.err != nil {
		return nil, r.err
	}
	if r.out == nil {
		return &awssqs.ReceiveMessageOutput{}, nil
	}
	return r.out, nil
}

func (c *fakeClient) DeleteMessage(_ context.Context, params *awssqs.DeleteMessageInput, _ ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	c.deletedMu.Lock()
	c.deleted = append(c.deleted, aws.ToString(params.ReceiptHandle))
	err := c.deleteErr
	c.deletedMu.Unlock()
	if err != nil {
		return nil, err
	}
	return &awssqs.DeleteMessageOutput{}, nil
}

func (c *fakeClient) deletedHandles() []string {
	c.deletedMu.Lock()
	defer c.deletedMu.Unlock()
	return append([]string(nil), c.deleted...)
}

// helper: build a one-message ReceiveMessageOutput.
func msg(body, handle, id string) *awssqs.ReceiveMessageOutput {
	return &awssqs.ReceiveMessageOutput{
		Messages: []types.Message{
			{Body: aws.String(body), ReceiptHandle: aws.String(handle), MessageId: aws.String(id)},
		},
	}
}

// ---- New ----

func TestNew_RejectsNilClient(t *testing.T) {
	_, err := sqsinput.New(sqsinput.Config{
		Queues: []sqsinput.Queue{{Name: "q", URL: "u"}},
	})
	if err == nil || !strings.Contains(err.Error(), "client required") {
		t.Fatalf("want client-required error, got %v", err)
	}
}

func TestNew_RejectsNoQueues(t *testing.T) {
	_, err := sqsinput.New(sqsinput.Config{Client: &fakeClient{}})
	if err == nil || !strings.Contains(err.Error(), "queue required") {
		t.Fatalf("want queue-required error, got %v", err)
	}
}

func TestNew_RejectsEmptyQueueName(t *testing.T) {
	_, err := sqsinput.New(sqsinput.Config{
		Client: &fakeClient{},
		Queues: []sqsinput.Queue{{Name: " ", URL: "u"}},
	})
	if err == nil || !strings.Contains(err.Error(), "queue name required") {
		t.Fatalf("want queue-name error, got %v", err)
	}
}

func TestNew_RejectsEmptyQueueURL(t *testing.T) {
	_, err := sqsinput.New(sqsinput.Config{
		Client: &fakeClient{},
		Queues: []sqsinput.Queue{{Name: "orders"}},
	})
	if err == nil || !strings.Contains(err.Error(), "URL") {
		t.Fatalf("want URL error, got %v", err)
	}
}

func TestName(t *testing.T) {
	in, _ := sqsinput.New(sqsinput.Config{
		Client: &fakeClient{},
		Queues: []sqsinput.Queue{{Name: "orders", URL: "u"}},
	})
	if in.Name() != "sqs" {
		t.Fatalf("Name: want sqs, got %q", in.Name())
	}
}

// ---- Start: happy path ----

func TestStart_EmitsRecordsAndDeletesMessages(t *testing.T) {
	c := &fakeClient{
		receives: []fakeReceive{
			{out: msg(`{"level":"ERROR","host":"web-1"}`, "rh-1", "m-1")},
			{out: msg(`{"level":"INFO","host":"web-2"}`, "rh-2", "m-2")},
		},
	}
	in, err := sqsinput.New(sqsinput.Config{
		Client: c,
		Queues: []sqsinput.Queue{{Name: "events", URL: "u"}},
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
				t.Errorf("missing level: %+v", rec.Record)
			}
		case <-time.After(time.Second):
			t.Fatalf("missed record %d", i)
		}
	}

	// Wait briefly for the second delete to fire.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(c.deletedHandles()) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	deleted := c.deletedHandles()
	if len(deleted) != 2 || deleted[0] != "rh-1" || deleted[1] != "rh-2" {
		t.Errorf("DeleteMessage handles: want [rh-1 rh-2], got %v", deleted)
	}

	cancel()
	if err := <-startErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start: want context.Canceled, got %v", err)
	}
}

// ---- Start: bad messages dropped (and NOT deleted, so SQS can redrive) ----

func TestStart_DropsBadMessagesAndDoesNotDelete(t *testing.T) {
	c := &fakeClient{
		receives: []fakeReceive{
			{out: msg("not json", "bad-1", "m-1")},
			{out: msg(`[1,2,3]`, "bad-2", "m-2")},  // array
			{out: msg(`"scalar"`, "bad-3", "m-3")}, // scalar
			{out: msg(``, "bad-4", "m-4")},         // empty
			{out: msg(`{"ok":true}`, "good", "m-5")},
		},
	}
	in, _ := sqsinput.New(sqsinput.Config{
		Client: c,
		Queues: []sqsinput.Queue{{Name: "q", URL: "u"}},
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

	// Wait briefly for the good message delete.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(c.deletedHandles()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	deleted := c.deletedHandles()
	// Only the good message should have been deleted; the four bad
	// ones stay in the queue for SQS redrive.
	if len(deleted) != 1 || deleted[0] != "good" {
		t.Fatalf("want only [good] deleted, got %v", deleted)
	}
}

// ---- Start: transient receive errors logged and skipped ----

func TestStart_TransientReceiveErrorIsLoggedAndSkipped(t *testing.T) {
	c := &fakeClient{
		receives: []fakeReceive{
			{err: errors.New("synthetic throttle")},
			{out: msg(`{"after":"throttle"}`, "rh", "m")},
		},
	}
	in, _ := sqsinput.New(sqsinput.Config{
		Client: c,
		Queues: []sqsinput.Queue{{Name: "q", URL: "u"}},
	})

	sink := make(chan input.EvaluationRecord, 1)
	// Long enough to wait through the 1-second backoff after the error.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go in.Start(ctx, sink)

	select {
	case rec := <-sink:
		if rec.Record["after"] != "throttle" {
			t.Fatalf("post-error record: %+v", rec.Record)
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("never received the post-error record")
	}
}

// ---- Start: ctx.Done exits cleanly ----

func TestStart_ExitsOnContextDone(t *testing.T) {
	c := &fakeClient{} // empty -> ReceiveMessage blocks on ctx
	in, _ := sqsinput.New(sqsinput.Config{
		Client: c,
		Queues: []sqsinput.Queue{{Name: "q", URL: "u"}},
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
	case <-time.After(2 * time.Second):
		t.Fatalf("Start did not return after cancel")
	}
}

// ---- Start: delete error logged, not fatal ----

func TestStart_DeleteErrorIsLoggedNotFatal(t *testing.T) {
	c := &fakeClient{
		receives: []fakeReceive{
			{out: msg(`{"ok":true}`, "rh-1", "m-1")},
			{out: msg(`{"again":true}`, "rh-2", "m-2")},
		},
		deleteErr: errors.New("synthetic IAM denial"),
	}
	in, _ := sqsinput.New(sqsinput.Config{
		Client: c,
		Queues: []sqsinput.Queue{{Name: "q", URL: "u"}},
	})

	sink := make(chan input.EvaluationRecord, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go in.Start(ctx, sink)

	for i := 0; i < 2; i++ {
		select {
		case <-sink:
		case <-time.After(time.Second):
			t.Fatalf("missed record %d despite delete-error tolerance", i)
		}
	}
}
