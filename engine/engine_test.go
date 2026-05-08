package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ryan-evans-git/signalwatch/engine"
	"github.com/ryan-evans-git/signalwatch/internal/api"
	"github.com/ryan-evans-git/signalwatch/internal/channel"
	"github.com/ryan-evans-git/signalwatch/internal/channel/webhook"
	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/input/event"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
	"github.com/ryan-evans-git/signalwatch/internal/store/sqlite"
	"github.com/ryan-evans-git/signalwatch/internal/subscriber"
)

// captureChannel collects every notification it sees so tests can assert.
type captureChannel struct {
	name string

	mu  sync.Mutex
	got []channel.Notification
}

func (c *captureChannel) Name() string { return c.name }
func (c *captureChannel) Send(_ context.Context, n channel.Notification) error {
	c.mu.Lock()
	c.got = append(c.got, n)
	c.mu.Unlock()
	return nil
}

func (c *captureChannel) snapshot() []channel.Notification {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]channel.Notification, len(c.got))
	copy(out, c.got)
	return out
}

// TestEngine_EndToEnd_PushEvent boots the engine, posts a rule + subscriber +
// subscription via the HTTP API, pushes events, and asserts that exactly one
// firing notification and one resolution notification fire.
func TestEngine_EndToEnd_PushEvent(t *testing.T) {
	st, err := sqlite.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cap := &captureChannel{name: "test"}
	channels := map[string]channel.Channel{"test": cap}

	eventInput := event.New("events")
	eng, err := engine.New(engine.Options{
		Store:      st,
		Channels:   channels,
		Inputs:     []input.Input{eventInput},
		EventInput: eventInput,
		Logger:     slog.Default(),
	})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := eng.Start(ctx); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	mux := http.NewServeMux()
	api.Mount(mux, eng, nil)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Create rule via API.
	rulePayload := map[string]any{
		"name":      "log errors",
		"enabled":   true,
		"severity":  "warning",
		"input_ref": "events",
		"condition": map[string]any{
			"type": "pattern_match",
			"spec": map[string]any{"field": "level", "kind": "contains", "pattern": "ERROR"},
		},
	}
	mustPostJSON(t, srv.URL+"/v1/rules", rulePayload)

	// Create subscriber + subscription.
	sub := subscriber.Subscriber{
		ID:   uuid.NewString(),
		Name: "ops",
		Channels: []subscriber.ChannelBinding{
			{Channel: "test", Address: "ops@example.com"},
		},
	}
	mustPostJSON(t, srv.URL+"/v1/subscribers", sub)
	mustPostJSON(t, srv.URL+"/v1/subscriptions", map[string]any{
		"subscriber_id":            sub.ID,
		"rule_id":                  ruleID(t, eng),
		"dwell_seconds":            0,
		"repeat_interval_seconds":  0,
		"notify_on_resolve":        true,
	})

	// Push 3 ERROR events back-to-back — expect 1 firing notification.
	for i := 0; i < 3; i++ {
		mustPostJSON(t, srv.URL+"/v1/events", map[string]any{
			"input_ref": "events",
			"record":    rule.Record{"level": "ERROR", "msg": fmt.Sprintf("boom %d", i)},
		})
	}
	waitFor(t, time.Second, func() bool { return len(cap.snapshot()) == 1 })
	if got := cap.snapshot()[0].Kind; got != "firing" {
		t.Errorf("first send kind want firing, got %s", got)
	}

	// Push an INFO event — rule resolves; expect a resolution notification.
	mustPostJSON(t, srv.URL+"/v1/events", map[string]any{
		"input_ref": "events",
		"record":    rule.Record{"level": "INFO", "msg": "back to normal"},
	})
	waitFor(t, time.Second, func() bool { return len(cap.snapshot()) == 2 })
	got := cap.snapshot()
	if got[1].Kind != "resolved" {
		t.Errorf("second send kind want resolved, got %s", got[1].Kind)
	}
}

// TestEngine_WebhookChannel_DeliversToFakeReceiver proves the webhook channel
// actually performs an HTTP POST with the expected payload.
func TestEngine_WebhookChannel_DeliversToFakeReceiver(t *testing.T) {
	var (
		mu       sync.Mutex
		received []map[string]any
	)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		received = append(received, body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(receiver.Close)

	st, err := sqlite.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	wh := webhook.New(webhook.Config{Name: "wh", URL: receiver.URL})
	eventInput := event.New("events")
	eng, err := engine.New(engine.Options{
		Store:      st,
		Channels:   map[string]channel.Channel{"wh": wh},
		Inputs:     []input.Input{eventInput},
		EventInput: eventInput,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	r := &rule.Rule{
		ID:        uuid.NewString(),
		Name:      "errors",
		Enabled:   true,
		Severity:  rule.SeverityCritical,
		InputRef:  "events",
		Condition: rule.PatternMatch{Field: "level", Kind: rule.MatchContains, Pattern: "ERROR"},
	}
	if err := eng.Rules().Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	s := &subscriber.Subscriber{
		ID:       uuid.NewString(),
		Name:     "alice",
		Channels: []subscriber.ChannelBinding{{Channel: "wh"}},
	}
	if err := eng.Subscribers().Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := eng.Subscriptions().Create(ctx, &subscriber.Subscription{
		ID:              uuid.NewString(),
		SubscriberID:    s.ID,
		RuleID:          r.ID,
		NotifyOnResolve: false,
	}); err != nil {
		t.Fatal(err)
	}

	if err := eng.Submit(ctx, "events", rule.Record{"level": "ERROR"}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) == 1
	})
	mu.Lock()
	body := received[0]
	mu.Unlock()
	if body["rule_name"] != "errors" {
		t.Errorf("rule_name in payload: %v", body["rule_name"])
	}
	if body["kind"] != "firing" {
		t.Errorf("kind in payload: %v", body["kind"])
	}
	if body["severity"] != "critical" {
		t.Errorf("severity in payload: %v", body["severity"])
	}
}

// ---- helpers ----

func mustPostJSON(t *testing.T, url string, body any) []byte {
	t.Helper()
	buf, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", strings.NewReader(string(buf)))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("POST %s: status %d", url, resp.StatusCode)
	}
	out := make([]byte, 0, 1024)
	buffer := make([]byte, 1024)
	for {
		n, _ := resp.Body.Read(buffer)
		out = append(out, buffer[:n]...)
		if n == 0 {
			break
		}
	}
	return out
}

func ruleID(t *testing.T, eng *engine.Engine) string {
	t.Helper()
	rs, err := eng.Rules().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) == 0 {
		t.Fatal("no rules")
	}
	return rs[0].ID
}

func waitFor(t *testing.T, max time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for condition")
}
