// Package discord delivers signalwatch notifications via a Discord
// webhook URL.
//
// Discord webhooks accept a small JSON body with `content` (plain text
// up to 2000 chars) and `embeds` (rich cards). signalwatch sends one
// embed per notification with the rule name, kind, severity, value,
// and incident ID — same shape as Slack's, in Discord's vocabulary.
package discord

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
		cfg.Name = "discord"
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
		return errors.New("discord: no webhook url configured")
	}

	body, err := json.Marshal(buildPayload(n))
	if err != nil {
		return fmt.Errorf("discord: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("discord: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("discord: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// buildPayload returns the Discord webhook JSON payload. Exposed for
// tests.
func buildPayload(n channel.Notification) map[string]any {
	severity := strings.ToLower(strings.TrimSpace(n.Severity))
	if severity == "" {
		severity = "info"
	}
	embed := map[string]any{
		"title": titleFor(n),
		"color": colorFor(severity, n.Kind),
	}
	if n.Description != "" {
		embed["description"] = n.Description
	}
	fields := []map[string]any{
		{"name": "Rule", "value": fallback(n.RuleName, n.RuleID), "inline": true},
		{"name": "Severity", "value": strings.ToUpper(severity), "inline": true},
	}
	if n.Value != "" {
		fields = append(fields, map[string]any{"name": "Value", "value": n.Value, "inline": true})
	}
	if n.IncidentID != "" {
		fields = append(fields, map[string]any{"name": "Incident", "value": n.IncidentID, "inline": true})
	}
	embed["fields"] = fields
	if !n.TriggeredAt.IsZero() {
		embed["timestamp"] = n.TriggeredAt.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"username": "signalwatch",
		"embeds":   []map[string]any{embed},
	}
}

func titleFor(n channel.Notification) string {
	prefix := strings.ToUpper(fallback(n.Kind, "firing"))
	return fmt.Sprintf("[%s] %s", prefix, fallback(n.RuleName, n.RuleID))
}

// colorFor returns a 24-bit Discord embed color matching the (severity,
// kind) signal: green for resolved, red for critical, amber for
// warning, blue otherwise.
func colorFor(severity, kind string) int {
	if kind == "resolved" {
		return 0x2EB67D
	}
	switch severity {
	case "critical":
		return 0xE01E5A
	case "warning":
		return 0xECB22E
	default:
		return 0x439FE0
	}
}

func fallback(s, fb string) string {
	if s == "" {
		return fb
	}
	return s
}
