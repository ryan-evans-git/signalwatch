// Package teams delivers signalwatch notifications to Microsoft Teams
// via an incoming-webhook URL. The wire format is an Adaptive Card
// embedded in the MessageCard envelope Teams' incoming-webhook
// connectors expect.
//
// Subscriber bindings can carry their own webhook URL via the
// Address field; channel-level WebhookURL is the fallback.
package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/channel"
)

type Config struct {
	Name       string
	WebhookURL string
	HTTPClient *http.Client
}

type Channel struct {
	cfg Config
}

func New(cfg Config) *Channel {
	if cfg.Name == "" {
		cfg.Name = "teams"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Channel{cfg: cfg}
}

func (c *Channel) Name() string { return c.cfg.Name }

func (c *Channel) Send(ctx context.Context, n channel.Notification) error {
	url := strings.TrimSpace(n.Address)
	if url == "" {
		url = c.cfg.WebhookURL
	}
	if url == "" {
		return errors.New("teams: no webhook url configured")
	}

	payload := buildMessageCard(n)
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("teams: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("teams: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("teams: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// buildMessageCard returns the Adaptive Card / MessageCard envelope
// Teams' incoming-webhook connector accepts. Exposed for tests.
func buildMessageCard(n channel.Notification) map[string]any {
	severity := strings.ToLower(strings.TrimSpace(n.Severity))
	if severity == "" {
		severity = "info"
	}
	title := titleFor(n)
	color := colorFor(severity, n.Kind)

	facts := []map[string]any{
		{"name": "Rule", "value": fallback(n.RuleName, n.RuleID)},
		{"name": "Kind", "value": strings.ToUpper(fallback(n.Kind, "firing"))},
		{"name": "Severity", "value": strings.ToUpper(severity)},
	}
	if n.Value != "" {
		facts = append(facts, map[string]any{"name": "Value", "value": n.Value})
	}
	if n.IncidentID != "" {
		facts = append(facts, map[string]any{"name": "Incident", "value": n.IncidentID})
	}
	if !n.TriggeredAt.IsZero() {
		facts = append(facts, map[string]any{"name": "Triggered", "value": n.TriggeredAt.UTC().Format(time.RFC3339)})
	}

	return map[string]any{
		"@type":      "MessageCard",
		"@context":   "https://schema.org/extensions",
		"summary":    title,
		"themeColor": color,
		"title":      title,
		"text":       n.Description,
		"sections": []map[string]any{
			{"facts": facts},
		},
	}
}

func titleFor(n channel.Notification) string {
	prefix := strings.ToUpper(fallback(n.Kind, "firing"))
	name := fallback(n.RuleName, n.RuleID)
	return fmt.Sprintf("[%s] %s", prefix, name)
}

// colorFor returns a hex Teams accepts as themeColor.
func colorFor(severity, kind string) string {
	if kind == "resolved" {
		return "2EB67D"
	}
	switch severity {
	case "critical":
		return "E01E5A"
	case "warning":
		return "ECB22E"
	default:
		return "439FE0"
	}
}

func fallback(s, fb string) string {
	if s == "" {
		return fb
	}
	return s
}
