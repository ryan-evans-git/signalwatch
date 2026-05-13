//go:build integration

package pubsub_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gpubsub "cloud.google.com/go/pubsub/v2"
	pubsubadmin "cloud.google.com/go/pubsub/v2/apiv1"
	pubsubpb "cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/ryan-evans-git/signalwatch/internal/input"
	psinput "github.com/ryan-evans-git/signalwatch/internal/input/stream/pubsub"
)

// emulatorOpts returns the SDK options needed to point a v2 admin
// client at the Pub/Sub emulator. The regular *Client picks up
// PUBSUB_EMULATOR_HOST automatically; the apiv1 admin clients do not.
func emulatorOpts() []option.ClientOption {
	host := os.Getenv("PUBSUB_EMULATOR_HOST")
	return []option.ClientOption{
		option.WithEndpoint(host),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	}
}

// One Pub/Sub emulator container per `go test` invocation. Each
// subtest creates its own topic + subscription within that emulator
// for isolation.
var (
	emulatorOnce  sync.Once
	emulatorHost  string
	emulatorErr   error
	emulatorTearD func() error
)

const emulatorImage = "gcr.io/google.com/cloudsdktool/google-cloud-cli:emulators"

// emulatorEndpoint spins up the gcloud emulator container (if not
// already) and returns its host:port. Tests that need pubsub set the
// PUBSUB_EMULATOR_HOST env var to this value so the gpubsub SDK
// targets the emulator instead of real GCP.
func emulatorEndpoint(t *testing.T) string {
	t.Helper()
	emulatorOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		req := testcontainers.ContainerRequest{
			Image:        emulatorImage,
			ExposedPorts: []string{"8085/tcp"},
			Entrypoint: []string{"gcloud", "beta", "emulators", "pubsub", "start",
				"--host-port=0.0.0.0:8085", "--project=signalwatch-test"},
			WaitingFor: wait.ForLog("Server started, listening on 8085").
				WithStartupTimeout(2 * time.Minute),
		}
		c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
		if err != nil {
			emulatorErr = fmt.Errorf("start emulator: %w", err)
			return
		}
		host, err := c.Host(ctx)
		if err != nil {
			emulatorErr = fmt.Errorf("host: %w", err)
			return
		}
		port, err := c.MappedPort(ctx, "8085/tcp")
		if err != nil {
			emulatorErr = fmt.Errorf("port: %w", err)
			return
		}
		emulatorHost = fmt.Sprintf("%s:%s", host, port.Port())
		emulatorTearD = func() error {
			ctx2, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return c.Terminate(ctx2)
		}
	})
	if emulatorErr != nil {
		t.Skipf("pubsub emulator unavailable: %v", emulatorErr)
	}
	t.Setenv("PUBSUB_EMULATOR_HOST", emulatorHost)
	return emulatorHost
}

var resourceSeq atomic.Uint64

func uniqueIDs(t *testing.T) (topicID, subID string) {
	t.Helper()
	n := resourceSeq.Add(1)
	return fmt.Sprintf("topic-%s-%d", t.Name(), n), fmt.Sprintf("sub-%s-%d", t.Name(), n)
}

func setupTopicAndSub(t *testing.T, projectID, topicID, subID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	opts := emulatorOpts()
	topicAdmin, err := pubsubadmin.NewTopicAdminClient(ctx, opts...)
	if err != nil {
		t.Fatalf("NewTopicAdminClient: %v", err)
	}
	defer topicAdmin.Close()
	topicName := fmt.Sprintf("projects/%s/topics/%s", projectID, topicID)
	if _, err := topicAdmin.CreateTopic(ctx, &pubsubpb.Topic{Name: topicName}); err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	subAdmin, err := pubsubadmin.NewSubscriptionAdminClient(ctx, opts...)
	if err != nil {
		t.Fatalf("NewSubscriptionAdminClient: %v", err)
	}
	defer subAdmin.Close()
	if _, err := subAdmin.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:               fmt.Sprintf("projects/%s/subscriptions/%s", projectID, subID),
		Topic:              topicName,
		AckDeadlineSeconds: 10,
	}); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
}

func publishJSON(t *testing.T, projectID, topicID string, payloads []map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := gpubsub.NewClient(ctx, projectID)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	pub := client.Publisher(topicID)
	defer pub.Stop()
	for _, p := range payloads {
		body, _ := json.Marshal(p)
		res := pub.Publish(ctx, &gpubsub.Message{Data: body})
		if _, err := res.Get(ctx); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
}

func TestIntegration_ConsumeAndAck(t *testing.T) {
	_ = emulatorEndpoint(t)
	projectID := "signalwatch-test"
	topicID, subID := uniqueIDs(t)
	setupTopicAndSub(t, projectID, topicID, subID)

	in, err := psinput.New(psinput.Config{
		ProjectID:     projectID,
		Subscriptions: []psinput.Subscription{{Name: subID, SubscriptionID: subID}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	publishJSON(t, projectID, topicID, []map[string]any{
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
			if rec.InputRef != subID {
				t.Errorf("InputRef: want %s, got %s", subID, rec.InputRef)
			}
			if _, ok := rec.Record["level"]; !ok {
				t.Errorf("missing level in %+v", rec.Record)
			}
			got++
		case <-time.After(2 * time.Second):
		}
	}
	if got != 3 {
		t.Fatalf("got %d records, want 3", got)
	}

	cancel()
	<-done
}
