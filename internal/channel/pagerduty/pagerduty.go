// Package pagerduty delivers signalwatch notifications via PagerDuty
// Events API v2.
//
// The Notification.Kind field maps to the PagerDuty event_action:
//
//	firing   -> trigger
//	repeat   -> trigger   (repeats reuse the same dedup_key so PD groups them)
//	resolved -> resolve
//
// dedup_key is derived from the incident ID, so a single firing → resolve
// pair always maps to one PagerDuty incident regardless of how many
// repeat-notifications were sent in between.
package pagerduty

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/channel"
)

// Default events endpoint; overridable for tests.
const defaultEventsURL = "https://events.pagerduty.com/v2/enqueue"

type Config struct {
	// Name is the channel name shown in subscriber bindings. Defaults
	// to "pagerduty".
	Name string
	// RoutingKey is the PagerDuty service integration key (sometimes
	// called the routing key). 32-char hex string. Required unless a
	// subscriber binding provides an override via Notification.Address.
	RoutingKey string
	// EventsURL overrides the PagerDuty endpoint. Tests use this to
	// point at an httptest.Server; production callers should leave it
	// empty for the default v2 endpoint.
	EventsURL string
	// HTTPClient overrides the default 10s-timeout client.
	HTTPClient *http.Client
}

type Channel struct {
	cfg Config
}

func New(cfg Config) *Channel {
	if cfg.Name == "" {
		cfg.Name = "pagerduty"
	}
	if cfg.EventsURL == "" {
		cfg.EventsURL = defaultEventsURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Channel{cfg: cfg}
}

func (c *Channel) Name() string { return c.cfg.Name }

// Send maps a signalwatch Notification to a PagerDuty Events API v2
// trigger or resolve and POSTs it. The routing key comes from the
// subscriber binding's Address (so subscribers can route to different
// PagerDuty services), falling back to the channel-level default.
func (c *Channel) Send(ctx context.Context, n channel.Notification) error {
	routingKey := strings.TrimSpace(n.Address)
	if routingKey == "" {
		routingKey = c.cfg.RoutingKey
	}
	if routingKey == "" {
		return errors.New("pagerduty: no routing key configured")
	}

	action := eventActionFor(n.Kind)
	payload := buildEventPayload(routingKey, action, n)

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("pagerduty: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.EventsURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("pagerduty: %w", err)
	}
	defer resp.Body.Close()
	// PD returns 202 Accepted for successful enqueue. Anything 2xx is
	// safe to consider success; 4xx/5xx surface the body for triage.
	if resp.StatusCode/100 != 2 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("pagerduty: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(buf)))
	}
	return nil
}

// eventActionFor maps signalwatch's Notification.Kind to the
// PagerDuty event_action. Repeat-firing notifications reuse the
// trigger action with the same dedup_key, which PD treats as a
// no-op alert refresh — exactly the right behavior.
func eventActionFor(kind string) string {
	if kind == "resolved" {
		return "resolve"
	}
	return "trigger"
}

// buildEventPayload formats the v2 wire payload. Exposed for tests.
func buildEventPayload(routingKey, action string, n channel.Notification) map[string]any {
	severity := strings.ToLower(strings.TrimSpace(n.Severity))
	if severity == "" {
		severity = "info"
	}
	// PD only accepts critical/error/warning/info. Map our three:
	if severity == "warning" || severity == "info" || severity == "critical" {
		// matches PD vocabulary, no change.
	} else {
		severity = "info"
	}

	source := n.RuleID
	if source == "" {
		source = "signalwatch"
	}

	payload := map[string]any{
		"routing_key":  routingKey,
		"event_action": action,
		"dedup_key":    "signalwatch:" + n.IncidentID,
	}
	if action == "trigger" {
		ts := n.TriggeredAt
		if ts.IsZero() {
			ts = n.Now
		}
		payloadBody := map[string]any{
			"summary":   summary(n),
			"source":    source,
			"severity":  severity,
			"timestamp": ts.UTC().Format(time.RFC3339),
			"component": "signalwatch-rule",
			"group":     n.RuleID,
			"class":     n.Kind,
			"custom_details": map[string]any{
				"rule_id":     n.RuleID,
				"rule_name":   n.RuleName,
				"incident_id": n.IncidentID,
				"value":       n.Value,
				"description": n.Description,
			},
		}
		payload["payload"] = payloadBody
	}
	return payload
}

func summary(n channel.Notification) string {
	name := n.RuleName
	if name == "" {
		name = n.RuleID
	}
	prefix := strings.ToUpper(n.Kind)
	if prefix == "" {
		prefix = "FIRING"
	}
	if n.Value != "" {
		return fmt.Sprintf("[%s] %s — %s", prefix, name, n.Value)
	}
	return fmt.Sprintf("[%s] %s", prefix, name)
}
