package webhook_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/channel"
	"github.com/ryan-evans-git/signalwatch/internal/channel/webhook"
)

func TestNew_DefaultsNameAndClient(t *testing.T) {
	c := webhook.New(webhook.Config{})
	if c.Name() != "webhook" {
		t.Fatalf("default Name(): want webhook, got %q", c.Name())
	}
}

func TestNew_RespectsConfigName(t *testing.T) {
	c := webhook.New(webhook.Config{Name: "custom"})
	if c.Name() != "custom" {
		t.Fatalf("custom Name(): want custom, got %q", c.Name())
	}
}

// receiver captures the body, headers, and method of every request it
// receives so tests can assert on the wire-level payload.
type receiver struct {
	mu      sync.Mutex
	method  string
	headers http.Header
	body    []byte
	status  int // status to return; 0 means 200
}

func (r *receiver) handler(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.method = req.Method
	r.headers = req.Header.Clone()
	r.body = body
	status := r.status
	r.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
}

func newReceiver(t *testing.T, status int) (*receiver, *httptest.Server) {
	t.Helper()
	r := &receiver{status: status}
	srv := httptest.NewServer(http.HandlerFunc(r.handler))
	t.Cleanup(srv.Close)
	return r, srv
}

func sampleNotification(address string) channel.Notification {
	return channel.Notification{
		IncidentID:  "inc-1",
		RuleID:      "r-1",
		RuleName:    "cpu high",
		Severity:    "warning",
		Description: "CPU > 90 for 5m",
		Value:       "95",
		Kind:        "firing",
		Address:     address,
		Now:         time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC),
		TriggeredAt: time.Date(2026, 5, 12, 11, 59, 0, 0, time.UTC),
	}
}

func TestSend_PostsJSONPayload(t *testing.T) {
	r, srv := newReceiver(t, http.StatusOK)
	c := webhook.New(webhook.Config{URL: srv.URL})

	if err := c.Send(context.Background(), sampleNotification("")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.method != http.MethodPost {
		t.Fatalf("method: want POST, got %s", r.method)
	}
	if got := r.headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type: want application/json, got %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(r.body, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	for _, key := range []string{
		"incident_id", "rule_id", "rule_name", "severity",
		"description", "value", "kind", "now", "triggered_at",
	} {
		if _, ok := payload[key]; !ok {
			t.Errorf("payload missing %q", key)
		}
	}
	if payload["rule_name"] != "cpu high" {
		t.Errorf("rule_name round-trip: got %v", payload["rule_name"])
	}
}

// Notification.Address must override the Config.URL: this is the per-
// subscriber routing path (e.g. each subscriber's own webhook).
func TestSend_NotificationAddressOverridesConfigURL(t *testing.T) {
	configURL, configSrv := newReceiver(t, http.StatusOK)
	override, overrideSrv := newReceiver(t, http.StatusOK)
	c := webhook.New(webhook.Config{URL: configSrv.URL})

	if err := c.Send(context.Background(), sampleNotification(overrideSrv.URL)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	override.mu.Lock()
	got := override.body
	override.mu.Unlock()
	if len(got) == 0 {
		t.Fatalf("override server received no body")
	}

	configURL.mu.Lock()
	gotConfig := configURL.body
	configURL.mu.Unlock()
	if len(gotConfig) != 0 {
		t.Fatalf("config URL should NOT have been hit when Address is set")
	}
}

func TestSend_NoURLConfiguredErrors(t *testing.T) {
	c := webhook.New(webhook.Config{})
	err := c.Send(context.Background(), sampleNotification(""))
	if err == nil || !strings.Contains(err.Error(), "no url configured") {
		t.Fatalf("want no-url error, got %v", err)
	}
}

func TestSend_NonSuccessStatusErrors(t *testing.T) {
	_, srv := newReceiver(t, http.StatusInternalServerError)
	c := webhook.New(webhook.Config{URL: srv.URL})
	err := c.Send(context.Background(), sampleNotification(""))
	if err == nil || !strings.Contains(err.Error(), "unexpected status 500") {
		t.Fatalf("want 500 error, got %v", err)
	}
}

func TestSend_AddsConfiguredHeaders(t *testing.T) {
	r, srv := newReceiver(t, http.StatusOK)
	c := webhook.New(webhook.Config{
		URL: srv.URL,
		Headers: map[string]string{
			"X-Trace-Id":    "abc",
			"Authorization": "Bearer secret",
		},
	})
	if err := c.Send(context.Background(), sampleNotification("")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.headers.Get("X-Trace-Id") != "abc" {
		t.Errorf("X-Trace-Id: want abc, got %q", r.headers.Get("X-Trace-Id"))
	}
	if r.headers.Get("Authorization") != "Bearer secret" {
		t.Errorf("Authorization: want Bearer secret, got %q", r.headers.Get("Authorization"))
	}
}

func TestSend_InvalidURLErrors(t *testing.T) {
	// A control character in the URL fails url.Parse inside
	// NewRequestWithContext, exercising the err != nil branch.
	c := webhook.New(webhook.Config{URL: "http://example.com/\x7f"})
	err := c.Send(context.Background(), sampleNotification(""))
	if err == nil {
		t.Fatalf("want error from malformed URL")
	}
}

func TestSend_HTTPClientErrorPropagates(t *testing.T) {
	c := webhook.New(webhook.Config{
		URL: "http://127.0.0.1:1", // unlikely to be listening
		HTTPClient: &http.Client{
			Timeout: 50 * time.Millisecond,
		},
	})
	err := c.Send(context.Background(), sampleNotification(""))
	if err == nil {
		t.Fatalf("want transport error")
	}
	// We don't pin the exact error string — net errors vary by platform.
	if errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("unexpected ErrServerClosed")
	}
}
