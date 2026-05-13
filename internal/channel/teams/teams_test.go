package teams_test

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
	"github.com/ryan-evans-git/signalwatch/internal/channel/teams"
)

type fakeTeams struct {
	mu       sync.Mutex
	requests []map[string]any
	status   int
}

func newFakeTeams(t *testing.T, status int) (*fakeTeams, *httptest.Server) {
	t.Helper()
	f := &fakeTeams{status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var parsed map[string]any
		_ = json.Unmarshal(raw, &parsed)
		f.mu.Lock()
		f.requests = append(f.requests, parsed)
		f.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte("1"))
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeTeams) snapshot() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.requests...)
}

// ---- constructor ----

func TestNew_Defaults(t *testing.T) {
	c := teams.New(teams.Config{WebhookURL: "u"})
	if c.Name() != "teams" {
		t.Errorf("Name default: want teams, got %q", c.Name())
	}
}

func TestNew_CustomName(t *testing.T) {
	c := teams.New(teams.Config{Name: "ops-teams", WebhookURL: "u"})
	if c.Name() != "ops-teams" {
		t.Errorf("Name: want ops-teams, got %q", c.Name())
	}
}

// ---- Send: URL precedence ----

func TestSend_AddressOverridesConfig(t *testing.T) {
	pd, srv := newFakeTeams(t, http.StatusOK)
	c := teams.New(teams.Config{WebhookURL: "http://channel-default/", HTTPClient: srv.Client()})
	if err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing",
		Address: srv.URL,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := pd.snapshot(); len(got) != 1 {
		t.Fatalf("want 1 request, got %d", len(got))
	}
}

func TestSend_FallsBackToConfigWebhookURL(t *testing.T) {
	pd, srv := newFakeTeams(t, http.StatusOK)
	c := teams.New(teams.Config{WebhookURL: srv.URL})
	if err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(pd.snapshot()) != 1 {
		t.Fatalf("want 1 request")
	}
}

func TestSend_NoURLErrors(t *testing.T) {
	c := teams.New(teams.Config{})
	err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing",
	})
	if err == nil || !strings.Contains(err.Error(), "webhook url") {
		t.Fatalf("want webhook-url error, got %v", err)
	}
}

// ---- Send: payload shape ----

func TestSend_MessageCardShape(t *testing.T) {
	pd, srv := newFakeTeams(t, http.StatusOK)
	c := teams.New(teams.Config{WebhookURL: srv.URL})
	when := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	if err := c.Send(context.Background(), channel.Notification{
		IncidentID: "inc-9", RuleID: "r1", RuleName: "Order failures",
		Severity: "critical", Kind: "firing", Value: "5 in 1m",
		Description: "host=web-1", TriggeredAt: when,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	body := pd.snapshot()[0]
	if body["@type"] != "MessageCard" {
		t.Errorf("@type: want MessageCard, got %v", body["@type"])
	}
	if body["@context"] != "https://schema.org/extensions" {
		t.Errorf("@context: %v", body["@context"])
	}
	if !strings.Contains(body["title"].(string), "Order failures") {
		t.Errorf("title missing rule name: %v", body["title"])
	}
	if body["text"] != "host=web-1" {
		t.Errorf("text: %v", body["text"])
	}
	if body["themeColor"] != "E01E5A" {
		t.Errorf("themeColor for critical-firing: want E01E5A, got %v", body["themeColor"])
	}
	// Facts: rule, kind, severity, value, incident, triggered = 6
	sections, _ := body["sections"].([]any)
	if len(sections) != 1 {
		t.Fatalf("want 1 section, got %d", len(sections))
	}
	facts, _ := sections[0].(map[string]any)["facts"].([]any)
	if len(facts) != 6 {
		t.Errorf("facts: want 6, got %d (%+v)", len(facts), facts)
	}
}

func TestSend_ResolvedThemeColor(t *testing.T) {
	pd, srv := newFakeTeams(t, http.StatusOK)
	c := teams.New(teams.Config{WebhookURL: srv.URL})
	_ = c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", RuleName: "x", Kind: "resolved", Severity: "critical",
	})
	if pd.snapshot()[0]["themeColor"] != "2EB67D" {
		t.Errorf("themeColor for resolved: want green, got %v", pd.snapshot()[0]["themeColor"])
	}
}

func TestSend_DefaultsSeverityToInfoWhenEmpty(t *testing.T) {
	pd, srv := newFakeTeams(t, http.StatusOK)
	c := teams.New(teams.Config{WebhookURL: srv.URL})
	_ = c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", RuleName: "x", Kind: "firing", // no severity
	})
	if pd.snapshot()[0]["themeColor"] != "439FE0" {
		t.Errorf("info themeColor: %v", pd.snapshot()[0]["themeColor"])
	}
}

func TestSend_WarningThemeColor(t *testing.T) {
	pd, srv := newFakeTeams(t, http.StatusOK)
	c := teams.New(teams.Config{WebhookURL: srv.URL})
	_ = c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", RuleName: "x", Kind: "firing", Severity: "warning",
	})
	if pd.snapshot()[0]["themeColor"] != "ECB22E" {
		t.Errorf("warning themeColor: %v", pd.snapshot()[0]["themeColor"])
	}
}

func TestSend_MinimalNotificationOmitsOptionalFacts(t *testing.T) {
	// Notification with no Value / no IncidentID / no TriggeredAt
	// should yield only the three required facts.
	pd, srv := newFakeTeams(t, http.StatusOK)
	c := teams.New(teams.Config{WebhookURL: srv.URL})
	_ = c.Send(context.Background(), channel.Notification{
		RuleID: "r", Kind: "firing", Severity: "info",
	})
	facts, _ := pd.snapshot()[0]["sections"].([]any)[0].(map[string]any)["facts"].([]any)
	if len(facts) != 3 {
		t.Errorf("minimal: want 3 facts, got %d", len(facts))
	}
}

// ---- Send: error paths ----

func TestSend_Non2xxReturnsError(t *testing.T) {
	_, srv := newFakeTeams(t, http.StatusBadRequest)
	c := teams.New(teams.Config{WebhookURL: srv.URL})
	err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing",
	})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("want 400, got %v", err)
	}
}

func TestSend_NetworkErrorPropagates(t *testing.T) {
	c := teams.New(teams.Config{WebhookURL: "http://127.0.0.1:1"})
	err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing",
	})
	if err == nil {
		t.Fatalf("want network error")
	}
}

func TestSend_ContextCancellation(t *testing.T) {
	_, srv := newFakeTeams(t, http.StatusOK)
	c := teams.New(teams.Config{WebhookURL: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.Send(ctx, channel.Notification{IncidentID: "i", RuleID: "r", Kind: "firing"})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}
