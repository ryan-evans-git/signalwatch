package pagerduty_test

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
	"github.com/ryan-evans-git/signalwatch/internal/channel/pagerduty"
)

// ---- fake PagerDuty events endpoint ----

type fakePD struct {
	mu       sync.Mutex
	requests []recordedRequest
	status   int
	body     string
}

type recordedRequest struct {
	Method string
	Path   string
	Body   map[string]any
}

func newFakePD(t *testing.T, status int, body string) (*fakePD, *httptest.Server) {
	t.Helper()
	pd := &fakePD{status: status, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var parsed map[string]any
		_ = json.Unmarshal(raw, &parsed)
		pd.mu.Lock()
		pd.requests = append(pd.requests, recordedRequest{Method: r.Method, Path: r.URL.Path, Body: parsed})
		pd.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return pd, srv
}

func (p *fakePD) snapshot() []recordedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recordedRequest(nil), p.requests...)
}

// ---- constructor ----

func TestNew_Defaults(t *testing.T) {
	c := pagerduty.New(pagerduty.Config{RoutingKey: "rk"})
	if c.Name() != "pagerduty" {
		t.Errorf("Name default: want pagerduty, got %q", c.Name())
	}
}

func TestNew_CustomName(t *testing.T) {
	c := pagerduty.New(pagerduty.Config{Name: "oncall", RoutingKey: "rk"})
	if c.Name() != "oncall" {
		t.Errorf("Name: want oncall, got %q", c.Name())
	}
}

// ---- Send: routing-key precedence ----

func TestSend_UsesAddressOverride(t *testing.T) {
	pd, srv := newFakePD(t, http.StatusAccepted, "")
	c := pagerduty.New(pagerduty.Config{
		RoutingKey: "channel-level-key",
		EventsURL:  srv.URL,
	})
	err := c.Send(context.Background(), channel.Notification{
		IncidentID: "inc-1", RuleID: "r1", RuleName: "Test",
		Kind: "firing", Severity: "warning",
		Address: "subscriber-key",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := pd.snapshot()
	if len(got) != 1 {
		t.Fatalf("requests: want 1, got %d", len(got))
	}
	if got[0].Body["routing_key"] != "subscriber-key" {
		t.Errorf("routing_key: subscriber Address should win, got %v", got[0].Body["routing_key"])
	}
}

func TestSend_FallsBackToConfigRoutingKey(t *testing.T) {
	pd, srv := newFakePD(t, http.StatusAccepted, "")
	c := pagerduty.New(pagerduty.Config{RoutingKey: "rk-cfg", EventsURL: srv.URL})
	if err := c.Send(context.Background(), channel.Notification{
		IncidentID: "inc-1", RuleID: "r1", RuleName: "Test",
		Kind: "firing", Severity: "warning",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := pd.snapshot()
	if got[0].Body["routing_key"] != "rk-cfg" {
		t.Errorf("routing_key: want rk-cfg, got %v", got[0].Body["routing_key"])
	}
}

func TestSend_NoRoutingKeyErrors(t *testing.T) {
	c := pagerduty.New(pagerduty.Config{})
	err := c.Send(context.Background(), channel.Notification{
		IncidentID: "inc-1", RuleID: "r1", Kind: "firing",
	})
	if err == nil || !strings.Contains(err.Error(), "routing key") {
		t.Fatalf("want routing-key error, got %v", err)
	}
}

// ---- Send: event_action mapping ----

func TestSend_FiringIsTrigger(t *testing.T) {
	pd, srv := newFakePD(t, http.StatusAccepted, "")
	c := pagerduty.New(pagerduty.Config{RoutingKey: "rk", EventsURL: srv.URL})
	_ = c.Send(context.Background(), channel.Notification{
		IncidentID: "inc-1", RuleID: "r1", Kind: "firing", Severity: "critical",
	})
	if pd.snapshot()[0].Body["event_action"] != "trigger" {
		t.Errorf("event_action: want trigger")
	}
}

func TestSend_RepeatIsTrigger(t *testing.T) {
	// Repeats reuse trigger + same dedup_key, which PD treats as a no-
	// op refresh. Asserting the wire shape preserves that behavior.
	pd, srv := newFakePD(t, http.StatusAccepted, "")
	c := pagerduty.New(pagerduty.Config{RoutingKey: "rk", EventsURL: srv.URL})
	_ = c.Send(context.Background(), channel.Notification{
		IncidentID: "inc-1", RuleID: "r1", Kind: "repeat", Severity: "warning",
	})
	got := pd.snapshot()[0].Body
	if got["event_action"] != "trigger" {
		t.Errorf("repeat → event_action: want trigger, got %v", got["event_action"])
	}
	if got["dedup_key"] != "signalwatch:inc-1" {
		t.Errorf("dedup_key: want signalwatch:inc-1, got %v", got["dedup_key"])
	}
}

func TestSend_ResolvedIsResolve(t *testing.T) {
	pd, srv := newFakePD(t, http.StatusAccepted, "")
	c := pagerduty.New(pagerduty.Config{RoutingKey: "rk", EventsURL: srv.URL})
	_ = c.Send(context.Background(), channel.Notification{
		IncidentID: "inc-1", RuleID: "r1", Kind: "resolved",
	})
	got := pd.snapshot()[0].Body
	if got["event_action"] != "resolve" {
		t.Errorf("event_action: want resolve")
	}
	// resolve events MUST NOT include a payload — PD rejects them.
	if _, has := got["payload"]; has {
		t.Errorf("resolve event should not carry payload")
	}
}

// ---- Send: wire-payload shape ----

func TestSend_TriggerPayloadShape(t *testing.T) {
	pd, srv := newFakePD(t, http.StatusAccepted, "")
	c := pagerduty.New(pagerduty.Config{RoutingKey: "rk", EventsURL: srv.URL})
	when := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	_ = c.Send(context.Background(), channel.Notification{
		IncidentID: "inc-2", RuleID: "r1", RuleName: "CPU high",
		Severity: "critical", Kind: "firing",
		Description: "host=web-1", Value: "value=5",
		Now: when, TriggeredAt: when,
	})
	pl, ok := pd.snapshot()[0].Body["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload missing: %+v", pd.snapshot()[0].Body)
	}
	if pl["severity"] != "critical" {
		t.Errorf("severity: %v", pl["severity"])
	}
	if !strings.Contains(pl["summary"].(string), "CPU high") {
		t.Errorf("summary missing rule name: %v", pl["summary"])
	}
	if pl["source"] != "r1" {
		t.Errorf("source: %v", pl["source"])
	}
	if pl["timestamp"] != "2026-05-13T10:00:00Z" {
		t.Errorf("timestamp: %v", pl["timestamp"])
	}
	cd, _ := pl["custom_details"].(map[string]any)
	if cd["incident_id"] != "inc-2" {
		t.Errorf("custom_details.incident_id: %v", cd["incident_id"])
	}
}

func TestSend_DefaultsSeverityToInfoWhenMissingOrUnknown(t *testing.T) {
	for _, sev := range []string{"", "weird"} {
		t.Run("sev="+sev, func(t *testing.T) {
			pd, srv := newFakePD(t, http.StatusAccepted, "")
			c := pagerduty.New(pagerduty.Config{RoutingKey: "rk", EventsURL: srv.URL})
			_ = c.Send(context.Background(), channel.Notification{
				IncidentID: "i", RuleID: "r", Kind: "firing", Severity: sev,
			})
			pl, _ := pd.snapshot()[0].Body["payload"].(map[string]any)
			if pl["severity"] != "info" {
				t.Errorf("severity: want info, got %v", pl["severity"])
			}
		})
	}
}

func TestSend_SummaryFallsBackToRuleIDWhenRuleNameEmpty(t *testing.T) {
	pd, srv := newFakePD(t, http.StatusAccepted, "")
	c := pagerduty.New(pagerduty.Config{RoutingKey: "rk", EventsURL: srv.URL})
	_ = c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "rule-id-x", Kind: "firing",
	})
	pl, _ := pd.snapshot()[0].Body["payload"].(map[string]any)
	if !strings.Contains(pl["summary"].(string), "rule-id-x") {
		t.Errorf("summary should fall back to RuleID: %v", pl["summary"])
	}
}

func TestSend_TriggeredAtMissingUsesNow(t *testing.T) {
	pd, srv := newFakePD(t, http.StatusAccepted, "")
	c := pagerduty.New(pagerduty.Config{RoutingKey: "rk", EventsURL: srv.URL})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing", Now: now,
	})
	pl, _ := pd.snapshot()[0].Body["payload"].(map[string]any)
	if pl["timestamp"] != "2026-01-01T00:00:00Z" {
		t.Errorf("timestamp should fall back to Now: %v", pl["timestamp"])
	}
}

// ---- Send: error paths ----

func TestSend_Non2xxReturnsError(t *testing.T) {
	_, srv := newFakePD(t, http.StatusBadRequest, `{"error":"invalid key"}`)
	c := pagerduty.New(pagerduty.Config{RoutingKey: "rk", EventsURL: srv.URL})
	err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing",
	})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("want 400 error, got %v", err)
	}
}

func TestSend_NetworkErrorPropagates(t *testing.T) {
	c := pagerduty.New(pagerduty.Config{
		RoutingKey: "rk",
		EventsURL:  "http://127.0.0.1:1", // refused
	})
	err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing",
	})
	if err == nil {
		t.Fatalf("want network error")
	}
}

func TestSend_ContextCancellation(t *testing.T) {
	_, srv := newFakePD(t, http.StatusAccepted, "")
	c := pagerduty.New(pagerduty.Config{RoutingKey: "rk", EventsURL: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate
	err := c.Send(ctx, channel.Notification{IncidentID: "i", RuleID: "r", Kind: "firing"})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}
