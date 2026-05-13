// Package sms delivers signalwatch notifications via Twilio's
// Programmable Messaging API. The subscriber binding's `address`
// carries the destination phone number; the channel-level `FromNumber`
// is the Twilio-provisioned sender.
//
// Credentials are sourced from env vars by default:
//
//	SIGNALWATCH_TWILIO_ACCOUNT_SID
//	SIGNALWATCH_TWILIO_AUTH_TOKEN
//
// They never live in config files. New() will use the env vars when
// Config.AccountSID / Config.AuthToken are empty. Embedders that
// inject creds programmatically can set them on Config directly.
//
// CI / local tests use a fake Twilio server via httptest. The
// optional SW_TWILIO_LIVE_TEST=1 env var is a hook for maintainers
// who want to do a real billable send before a release — those tests
// are tagged separately and not part of `go test ./...`.
package sms

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/channel"
)

const defaultAPIBase = "https://api.twilio.com/2010-04-01"

type Config struct {
	// Name is the channel name shown in subscriber bindings. Defaults
	// to "sms".
	Name string
	// AccountSID is the Twilio account SID. Empty falls back to
	// SIGNALWATCH_TWILIO_ACCOUNT_SID.
	AccountSID string
	// AuthToken is the Twilio auth token. Empty falls back to
	// SIGNALWATCH_TWILIO_AUTH_TOKEN.
	AuthToken string
	// FromNumber is the Twilio-provisioned sender (E.164 format,
	// e.g. "+15555551234"). Required.
	FromNumber string
	// APIBase overrides the Twilio API base URL. Tests set this to an
	// httptest.Server URL; production callers leave it empty.
	APIBase string
	// HTTPClient overrides the default 10s-timeout client.
	HTTPClient *http.Client
}

type Channel struct {
	cfg Config
}

func New(cfg Config) *Channel {
	if cfg.Name == "" {
		cfg.Name = "sms"
	}
	if cfg.AccountSID == "" {
		cfg.AccountSID = os.Getenv("SIGNALWATCH_TWILIO_ACCOUNT_SID")
	}
	if cfg.AuthToken == "" {
		cfg.AuthToken = os.Getenv("SIGNALWATCH_TWILIO_AUTH_TOKEN")
	}
	if cfg.APIBase == "" {
		cfg.APIBase = defaultAPIBase
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Channel{cfg: cfg}
}

func (c *Channel) Name() string { return c.cfg.Name }

func (c *Channel) Send(ctx context.Context, n channel.Notification) error {
	if c.cfg.AccountSID == "" || c.cfg.AuthToken == "" {
		return errors.New("sms: AccountSID + AuthToken required (set SIGNALWATCH_TWILIO_ACCOUNT_SID / SIGNALWATCH_TWILIO_AUTH_TOKEN)")
	}
	if strings.TrimSpace(c.cfg.FromNumber) == "" {
		return errors.New("sms: FromNumber required")
	}
	to := strings.TrimSpace(n.Address)
	if to == "" {
		return errors.New("sms: subscriber Address (destination phone number) required")
	}

	form := url.Values{}
	form.Set("From", c.cfg.FromNumber)
	form.Set("To", to)
	form.Set("Body", composeBody(n))

	endpoint := fmt.Sprintf("%s/Accounts/%s/Messages.json", strings.TrimRight(c.cfg.APIBase, "/"), c.cfg.AccountSID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+basicAuth(c.cfg.AccountSID, c.cfg.AuthToken))

	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("sms: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("sms: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(buf)))
	}
	return nil
}

// composeBody builds the SMS body text. Twilio's SMS segments are
// 160 chars each (or 70 for non-GSM); we soft-cap at 480 chars
// (3 segments) and add an ellipsis if longer.
func composeBody(n channel.Notification) string {
	kind := strings.ToUpper(strings.TrimSpace(n.Kind))
	if kind == "" {
		kind = "FIRING"
	}
	severity := strings.ToUpper(strings.TrimSpace(n.Severity))
	if severity == "" {
		severity = "INFO"
	}
	name := n.RuleName
	if name == "" {
		name = n.RuleID
	}
	parts := []string{fmt.Sprintf("[%s/%s] %s", severity, kind, name)}
	if n.Value != "" {
		parts = append(parts, n.Value)
	}
	if n.Description != "" {
		parts = append(parts, n.Description)
	}
	if n.IncidentID != "" {
		parts = append(parts, "inc:"+n.IncidentID)
	}
	body := strings.Join(parts, " — ")
	// Soft-cap at 480 BYTES (Twilio bills per 160-char SMS segment;
	// 480 ≈ 3 segments). The ellipsis is 3 bytes in UTF-8, so trim to
	// max-3 to keep the total ≤ max.
	const max = 480
	if len(body) > max {
		body = body[:max-3] + "…"
	}
	return body
}

// basicAuth returns the Base64-encoded "user:pass" used by HTTP Basic
// auth. Twilio's API accepts the AccountSID as user, AuthToken as pass.
func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}
