package slack_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/channel"
	"github.com/ryan-evans-git/signalwatch/internal/channel/slack"
)

// receiver captures the body+headers of every request so tests can assert
// on the rendered Slack payload.
type receiver struct {
	mu      sync.Mutex
	body    []byte
	headers http.Header
	status  int
}

func (r *receiver) handler(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.body = body
	r.headers = req.Header.Clone()
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

func sample(address string, kind, severity string) channel.Notification {
	return channel.Notification{
		IncidentID:  "inc-1",
		RuleID:      "r-1",
		RuleName:    "cpu high",
		Severity:    severity,
		Description: "load > 90 & sustained",
		Value:       "95",
		Kind:        kind,
		Address:     address,
		Now:         time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC),
	}
}

func TestNew_DefaultsName(t *testing.T) {
	c := slack.New(slack.Config{})
	if c.Name() != "slack" {
		t.Fatalf("default Name: want slack, got %q", c.Name())
	}
}

func TestNew_RespectsConfigName(t *testing.T) {
	c := slack.New(slack.Config{Name: "ops-slack"})
	if c.Name() != "ops-slack" {
		t.Fatalf("Name: want ops-slack, got %q", c.Name())
	}
}

func TestSend_NoWebhookConfiguredErrors(t *testing.T) {
	c := slack.New(slack.Config{})
	err := c.Send(context.Background(), sample("", "firing", "warning"))
	if err == nil || !strings.Contains(err.Error(), "no webhook url configured") {
		t.Fatalf("want no-webhook error, got %v", err)
	}
}

func TestSend_PostsExpectedPayload(t *testing.T) {
	r, srv := newReceiver(t, http.StatusOK)
	c := slack.New(slack.Config{WebhookURL: srv.URL})

	if err := c.Send(context.Background(), sample("", "firing", "critical")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.headers.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type: want application/json, got %q", r.headers.Get("Content-Type"))
	}
	var body map[string]any
	if err := json.Unmarshal(r.body, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	text, _ := body["text"].(string)
	if !strings.Contains(text, "CRITICAL") {
		t.Errorf("severity not uppercased in text: %q", text)
	}
	if !strings.Contains(text, "FIRING") {
		t.Errorf("kind not uppercased in text: %q", text)
	}
	if !strings.Contains(text, ":rotating_light:") {
		t.Errorf("firing kind should use :rotating_light: emoji: %q", text)
	}
	// Attachment with color = danger (critical, non-resolved) is in attachments[0].
	atts, _ := body["attachments"].([]any)
	if len(atts) == 0 {
		t.Fatalf("attachments missing")
	}
	first, _ := atts[0].(map[string]any)
	if first["color"] != "danger" {
		t.Errorf("attachment color for critical firing: want danger, got %v", first["color"])
	}
}

func TestSend_NotificationAddressOverridesWebhookURL(t *testing.T) {
	configR, configSrv := newReceiver(t, http.StatusOK)
	addrR, addrSrv := newReceiver(t, http.StatusOK)
	c := slack.New(slack.Config{WebhookURL: configSrv.URL})

	if err := c.Send(context.Background(), sample(addrSrv.URL, "firing", "info")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	addrR.mu.Lock()
	got := addrR.body
	addrR.mu.Unlock()
	if len(got) == 0 {
		t.Fatalf("address override server got no body")
	}
	configR.mu.Lock()
	gotConfig := configR.body
	configR.mu.Unlock()
	if len(gotConfig) != 0 {
		t.Fatalf("config URL should NOT have been hit")
	}
}

func TestSend_NonSuccessStatusErrors(t *testing.T) {
	_, srv := newReceiver(t, http.StatusBadRequest)
	c := slack.New(slack.Config{WebhookURL: srv.URL})
	err := c.Send(context.Background(), sample("", "firing", "info"))
	if err == nil || !strings.Contains(err.Error(), "unexpected status 400") {
		t.Fatalf("want 400 error, got %v", err)
	}
}

func TestSend_InvalidURLErrors(t *testing.T) {
	c := slack.New(slack.Config{WebhookURL: "http://example.com/\x7f"})
	err := c.Send(context.Background(), sample("", "firing", "info"))
	if err == nil {
		t.Fatalf("want url-parse error")
	}
}

// EmojiFor + colorFor cover three kinds and three severities. We exercise
// every branch by walking a small matrix and asserting that the rendered
// payload reflects each combination.
func TestSend_RendersKindAndSeverityMatrix(t *testing.T) {
	cases := []struct {
		kind, severity string
		wantEmoji      string
		wantColor      string
	}{
		{"firing", "critical", ":rotating_light:", "danger"},
		{"firing", "warning", ":rotating_light:", "warning"},
		{"firing", "info", ":rotating_light:", "#439FE0"},
		{"firing", "", ":rotating_light:", "#439FE0"}, // empty -> INFO default
		{"repeat", "warning", ":repeat:", "warning"},
		{"resolved", "critical", ":white_check_mark:", "good"},
		// Unknown kind falls through to the default emoji branch.
		{"unknown-kind", "warning", ":rotating_light:", "warning"},
	}
	for _, tc := range cases {
		t.Run(tc.kind+"/"+tc.severity, func(t *testing.T) {
			r, srv := newReceiver(t, http.StatusOK)
			c := slack.New(slack.Config{WebhookURL: srv.URL})
			if err := c.Send(context.Background(), sample("", tc.kind, tc.severity)); err != nil {
				t.Fatalf("Send: %v", err)
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			var body map[string]any
			_ = json.Unmarshal(r.body, &body)
			text, _ := body["text"].(string)
			if !strings.Contains(text, tc.wantEmoji) {
				t.Errorf("emoji: want %s in %q", tc.wantEmoji, text)
			}
			atts, _ := body["attachments"].([]any)
			if len(atts) == 0 {
				t.Fatalf("attachments missing")
			}
			color, _ := atts[0].(map[string]any)["color"].(string)
			if color != tc.wantColor {
				t.Errorf("color: want %s, got %s", tc.wantColor, color)
			}
		})
	}
}

// escape() converts HTML-special characters in the description. Pin the
// behavior so the Slack payload doesn't accidentally render alert text as
// HTML or break Slack's mrkdwn parsing.
func TestSend_DescriptionIsEscaped(t *testing.T) {
	r, srv := newReceiver(t, http.StatusOK)
	c := slack.New(slack.Config{WebhookURL: srv.URL})
	n := sample("", "firing", "info")
	n.Description = "if x < 10 && y > 0 then & flag"
	if err := c.Send(context.Background(), n); err != nil {
		t.Fatalf("Send: %v", err)
	}
	r.mu.Lock()
	raw := r.body
	r.mu.Unlock()
	// Decode the JSON to inspect the post-decoded text. json.Marshal
	// escapes the leading & to & on the wire; after decoding it's
	// the literal &lt; &gt; &amp; that we put there in escape().
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	text, _ := got["text"].(string)
	for _, want := range []string{"&lt;", "&gt;", "&amp;"} {
		if !strings.Contains(text, want) {
			t.Errorf("escape: missing %s in decoded text %q", want, text)
		}
	}
}
