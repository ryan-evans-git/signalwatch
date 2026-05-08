// Package webhook delivers signalwatch notifications by POSTing a JSON
// payload to an arbitrary URL.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/channel"
)

type Config struct {
	Name       string
	URL        string // optional default; subscriber address overrides
	Headers    map[string]string
	HTTPClient *http.Client
}

type Channel struct {
	cfg Config
}

func New(cfg Config) *Channel {
	if cfg.Name == "" {
		cfg.Name = "webhook"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Channel{cfg: cfg}
}

func (c *Channel) Name() string { return c.cfg.Name }

func (c *Channel) Send(ctx context.Context, n channel.Notification) error {
	url := c.cfg.URL
	if n.Address != "" {
		url = n.Address
	}
	if url == "" {
		return fmt.Errorf("webhook: no url configured")
	}

	payload := map[string]any{
		"incident_id":  n.IncidentID,
		"rule_id":      n.RuleID,
		"rule_name":    n.RuleName,
		"severity":     n.Severity,
		"description":  n.Description,
		"value":        n.Value,
		"kind":         n.Kind,
		"now":          n.Now.UTC().Format(time.RFC3339),
		"triggered_at": n.TriggeredAt.UTC().Format(time.RFC3339),
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range c.cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("webhook: unexpected status %d", resp.StatusCode)
	}
	return nil
}
