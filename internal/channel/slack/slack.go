// Package slack delivers signalwatch notifications via a Slack incoming
// webhook URL.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
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
		cfg.Name = "slack"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Channel{cfg: cfg}
}

func (c *Channel) Name() string { return c.cfg.Name }

func (c *Channel) Send(ctx context.Context, n channel.Notification) error {
	url := c.cfg.WebhookURL
	if n.Address != "" {
		// Subscriber-level override: a subscriber may carry their own webhook
		// URL in the binding address (e.g., a personal #channel webhook).
		url = n.Address
	}
	if url == "" {
		return fmt.Errorf("slack: no webhook url configured")
	}

	emoji := emojiFor(n.Kind)
	severity := strings.ToUpper(n.Severity)
	if severity == "" {
		severity = "INFO"
	}
	text := fmt.Sprintf("%s *[%s]* `%s` — %s\n>%s",
		emoji, severity, strings.ToUpper(n.Kind), n.RuleName, escape(n.Description))
	body := map[string]any{
		"text": text,
		"attachments": []map[string]any{{
			"color":  colorFor(n.Severity, n.Kind),
			"fields": []map[string]any{{"title": "Value", "value": n.Value, "short": true}, {"title": "Incident", "value": n.IncidentID, "short": true}},
			"ts":     n.Now.Unix(),
		}},
	}
	payload, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("slack: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func emojiFor(kind string) string {
	switch kind {
	case "resolved":
		return ":white_check_mark:"
	case "repeat":
		return ":repeat:"
	default:
		return ":rotating_light:"
	}
}

func colorFor(severity, kind string) string {
	if kind == "resolved" {
		return "good"
	}
	switch severity {
	case "critical":
		return "danger"
	case "warning":
		return "warning"
	default:
		return "#439FE0"
	}
}

func escape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
