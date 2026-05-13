package discord_test

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
	"github.com/ryan-evans-git/signalwatch/internal/channel/discord"
)

type fakeDiscord struct {
	mu       sync.Mutex
	requests []map[string]any
	status   int
}

func newFakeDiscord(t *testing.T, status int) (*fakeDiscord, *httptest.Server) {
	t.Helper()
	f := &fakeDiscord{status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var parsed map[string]any
		_ = json.Unmarshal(raw, &parsed)
		f.mu.Lock()
		f.requests = append(f.requests, parsed)
		f.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeDiscord) snapshot() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.requests...)
}

// ---- constructor ----

func TestNew_Defaults(t *testing.T) {
	c := discord.New(discord.Config{WebhookURL: "u"})
	if c.Name() != "discord" {
		t.Errorf("Name default: want discord, got %q", c.Name())
	}
}

func TestNew_CustomName(t *testing.T) {
	c := discord.New(discord.Config{Name: "ops-discord", WebhookURL: "u"})
	if c.Name() != "ops-discord" {
		t.Errorf("Name: %q", c.Name())
	}
}

// ---- Send: URL precedence ----

func TestSend_AddressOverridesConfig(t *testing.T) {
	f, srv := newFakeDiscord(t, http.StatusNoContent)
	c := discord.New(discord.Config{WebhookURL: "http://channel-default/"})
	if err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing",
		Address: srv.URL,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(f.snapshot()) != 1 {
		t.Fatalf("want 1 request to override URL")
	}
}

func TestSend_FallsBackToConfigWebhookURL(t *testing.T) {
	f, srv := newFakeDiscord(t, http.StatusNoContent)
	c := discord.New(discord.Config{WebhookURL: srv.URL})
	if err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(f.snapshot()) != 1 {
		t.Fatalf("want 1 request to default URL")
	}
}

func TestSend_NoURLErrors(t *testing.T) {
	c := discord.New(discord.Config{})
	err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing",
	})
	if err == nil || !strings.Contains(err.Error(), "webhook url") {
		t.Fatalf("want webhook-url error, got %v", err)
	}
}

// ---- Send: payload shape ----

func TestSend_EmbedShape(t *testing.T) {
	f, srv := newFakeDiscord(t, http.StatusNoContent)
	c := discord.New(discord.Config{WebhookURL: srv.URL})
	when := time.Date(2026, 5, 13, 11, 0, 0, 0, time.UTC)
	_ = c.Send(context.Background(), channel.Notification{
		IncidentID: "inc-9", RuleID: "r1", RuleName: "Order failures",
		Severity: "critical", Kind: "firing", Value: "5 in 1m",
		Description: "host=web-1", TriggeredAt: when,
	})
	body := f.snapshot()[0]
	if body["username"] != "signalwatch" {
		t.Errorf("username: %v", body["username"])
	}
	embeds, _ := body["embeds"].([]any)
	if len(embeds) != 1 {
		t.Fatalf("want 1 embed, got %d", len(embeds))
	}
	embed := embeds[0].(map[string]any)
	if !strings.Contains(embed["title"].(string), "Order failures") {
		t.Errorf("title: %v", embed["title"])
	}
	// 0xE01E5A == 14687322 decimal
	if int(embed["color"].(float64)) != 0xE01E5A {
		t.Errorf("color for critical-firing: want 0xE01E5A, got %v", embed["color"])
	}
	if embed["description"] != "host=web-1" {
		t.Errorf("description: %v", embed["description"])
	}
	fields, _ := embed["fields"].([]any)
	if len(fields) != 4 {
		t.Errorf("fields: want 4 (rule, severity, value, incident), got %d", len(fields))
	}
	if embed["timestamp"] != "2026-05-13T11:00:00Z" {
		t.Errorf("timestamp: %v", embed["timestamp"])
	}
}

func TestSend_ResolvedEmbedIsGreen(t *testing.T) {
	f, srv := newFakeDiscord(t, http.StatusNoContent)
	c := discord.New(discord.Config{WebhookURL: srv.URL})
	_ = c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", RuleName: "x", Kind: "resolved", Severity: "critical",
	})
	color := int(f.snapshot()[0]["embeds"].([]any)[0].(map[string]any)["color"].(float64))
	if color != 0x2EB67D {
		t.Errorf("resolved color: want 0x2EB67D, got %#x", color)
	}
}

func TestSend_WarningColor(t *testing.T) {
	f, srv := newFakeDiscord(t, http.StatusNoContent)
	c := discord.New(discord.Config{WebhookURL: srv.URL})
	_ = c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", RuleName: "x", Kind: "firing", Severity: "warning",
	})
	color := int(f.snapshot()[0]["embeds"].([]any)[0].(map[string]any)["color"].(float64))
	if color != 0xECB22E {
		t.Errorf("warning color: want 0xECB22E, got %#x", color)
	}
}

func TestSend_DefaultsSeverityToInfoWhenEmpty(t *testing.T) {
	f, srv := newFakeDiscord(t, http.StatusNoContent)
	c := discord.New(discord.Config{WebhookURL: srv.URL})
	_ = c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", RuleName: "x", Kind: "firing", // no severity
	})
	color := int(f.snapshot()[0]["embeds"].([]any)[0].(map[string]any)["color"].(float64))
	if color != 0x439FE0 {
		t.Errorf("info color: want 0x439FE0, got %#x", color)
	}
}

func TestSend_MinimalNotificationOmitsOptionalFields(t *testing.T) {
	f, srv := newFakeDiscord(t, http.StatusNoContent)
	c := discord.New(discord.Config{WebhookURL: srv.URL})
	_ = c.Send(context.Background(), channel.Notification{
		RuleID: "r", Kind: "firing", Severity: "info",
	})
	embed := f.snapshot()[0]["embeds"].([]any)[0].(map[string]any)
	if _, has := embed["description"]; has {
		t.Errorf("description should be absent when Notification.Description is empty")
	}
	if _, has := embed["timestamp"]; has {
		t.Errorf("timestamp should be absent when TriggeredAt is zero")
	}
	fields := embed["fields"].([]any)
	if len(fields) != 2 { // just Rule + Severity
		t.Errorf("minimal: want 2 fields, got %d", len(fields))
	}
}

// ---- Send: error paths ----

func TestSend_Non2xxReturnsError(t *testing.T) {
	_, srv := newFakeDiscord(t, http.StatusBadRequest)
	c := discord.New(discord.Config{WebhookURL: srv.URL})
	err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing",
	})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("want 400, got %v", err)
	}
}

func TestSend_NetworkErrorPropagates(t *testing.T) {
	c := discord.New(discord.Config{WebhookURL: "http://127.0.0.1:1"})
	err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing",
	})
	if err == nil {
		t.Fatalf("want network error")
	}
}

func TestSend_ContextCancellation(t *testing.T) {
	_, srv := newFakeDiscord(t, http.StatusNoContent)
	c := discord.New(discord.Config{WebhookURL: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.Send(ctx, channel.Notification{IncidentID: "i", RuleID: "r", Kind: "firing"})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}
