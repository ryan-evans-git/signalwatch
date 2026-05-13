package sms_test

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ryan-evans-git/signalwatch/internal/channel"
	"github.com/ryan-evans-git/signalwatch/internal/channel/sms"
)

// ---- fake Twilio Messages endpoint ----

type fakeTwilio struct {
	mu       sync.Mutex
	requests []recordedRequest
	status   int
}

type recordedRequest struct {
	Method string
	Path   string
	Auth   string
	Form   url.Values
}

func newFakeTwilio(t *testing.T, status int) (*fakeTwilio, *httptest.Server) {
	t.Helper()
	f := &fakeTwilio{status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		form, _ := url.ParseQuery(string(raw))
		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{
			Method: r.Method, Path: r.URL.Path,
			Auth: r.Header.Get("Authorization"), Form: form,
		})
		f.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"sid":"SMxxxx","status":"queued"}`))
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeTwilio) snapshot() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

// ---- constructor ----

func TestNew_Defaults(t *testing.T) {
	c := sms.New(sms.Config{AccountSID: "AC", AuthToken: "tok", FromNumber: "+1"})
	if c.Name() != "sms" {
		t.Errorf("Name default: want sms, got %q", c.Name())
	}
}

func TestNew_CustomName(t *testing.T) {
	c := sms.New(sms.Config{Name: "oncall-sms", AccountSID: "AC", AuthToken: "tok", FromNumber: "+1"})
	if c.Name() != "oncall-sms" {
		t.Errorf("Name: %q", c.Name())
	}
}

func TestNew_PullsCredsFromEnvWhenEmpty(t *testing.T) {
	t.Setenv("SIGNALWATCH_TWILIO_ACCOUNT_SID", "AC-from-env")
	t.Setenv("SIGNALWATCH_TWILIO_AUTH_TOKEN", "tok-from-env")
	f, srv := newFakeTwilio(t, http.StatusCreated)
	c := sms.New(sms.Config{
		FromNumber: "+15555550100",
		APIBase:    srv.URL,
	})
	err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing", Severity: "info",
		Address: "+15555551234",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := f.snapshot()[0]
	if !strings.Contains(got.Path, "/Accounts/AC-from-env/Messages.json") {
		t.Errorf("path should use env SID: %s", got.Path)
	}
	expectedAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("AC-from-env:tok-from-env"))
	if got.Auth != expectedAuth {
		t.Errorf("Authorization: want %q, got %q", expectedAuth, got.Auth)
	}
}

// ---- Send: missing-config errors ----

func TestSend_NoAccountSIDErrors(t *testing.T) {
	t.Setenv("SIGNALWATCH_TWILIO_ACCOUNT_SID", "")
	t.Setenv("SIGNALWATCH_TWILIO_AUTH_TOKEN", "")
	c := sms.New(sms.Config{FromNumber: "+1"})
	err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing", Address: "+1",
	})
	if err == nil || !strings.Contains(err.Error(), "AccountSID") {
		t.Fatalf("want AccountSID error, got %v", err)
	}
}

func TestSend_NoFromNumberErrors(t *testing.T) {
	c := sms.New(sms.Config{AccountSID: "AC", AuthToken: "tok"})
	err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing", Address: "+1",
	})
	if err == nil || !strings.Contains(err.Error(), "FromNumber") {
		t.Fatalf("want FromNumber error, got %v", err)
	}
}

func TestSend_NoAddressErrors(t *testing.T) {
	c := sms.New(sms.Config{AccountSID: "AC", AuthToken: "tok", FromNumber: "+1"})
	err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing", // no Address
	})
	if err == nil || !strings.Contains(err.Error(), "destination phone number") {
		t.Fatalf("want destination-phone-number error, got %v", err)
	}
}

// ---- Send: wire shape ----

func TestSend_WireShape(t *testing.T) {
	f, srv := newFakeTwilio(t, http.StatusCreated)
	c := sms.New(sms.Config{
		AccountSID: "ACtest", AuthToken: "tok",
		FromNumber: "+15555550100", APIBase: srv.URL,
	})
	err := c.Send(context.Background(), channel.Notification{
		IncidentID: "inc-9", RuleID: "r1", RuleName: "Order failures",
		Severity: "critical", Kind: "firing", Value: "5 in 1m",
		Address: "+15555551234",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := f.snapshot()[0]
	if got.Method != http.MethodPost {
		t.Errorf("method: %s", got.Method)
	}
	if got.Path != "/Accounts/ACtest/Messages.json" {
		t.Errorf("path: %s", got.Path)
	}
	expectedAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("ACtest:tok"))
	if got.Auth != expectedAuth {
		t.Errorf("Authorization: want %q, got %q", expectedAuth, got.Auth)
	}
	if got.Form.Get("From") != "+15555550100" {
		t.Errorf("From: %s", got.Form.Get("From"))
	}
	if got.Form.Get("To") != "+15555551234" {
		t.Errorf("To: %s", got.Form.Get("To"))
	}
	body := got.Form.Get("Body")
	for _, want := range []string{"CRITICAL", "FIRING", "Order failures", "5 in 1m", "inc-9"} {
		if !strings.Contains(body, want) {
			t.Errorf("Body missing %q: %s", want, body)
		}
	}
}

func TestSend_BodyDefaultsKindAndSeverity(t *testing.T) {
	f, srv := newFakeTwilio(t, http.StatusCreated)
	c := sms.New(sms.Config{
		AccountSID: "AC", AuthToken: "tok",
		FromNumber: "+1", APIBase: srv.URL,
	})
	_ = c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r1", Address: "+2", // no kind/severity/name
	})
	body := f.snapshot()[0].Form.Get("Body")
	if !strings.Contains(body, "INFO") || !strings.Contains(body, "FIRING") {
		t.Errorf("defaults: body should include INFO + FIRING, got %s", body)
	}
	if !strings.Contains(body, "r1") {
		t.Errorf("body should fall back to RuleID, got %s", body)
	}
}

func TestSend_BodyTruncatesLong(t *testing.T) {
	f, srv := newFakeTwilio(t, http.StatusCreated)
	c := sms.New(sms.Config{
		AccountSID: "AC", AuthToken: "tok", FromNumber: "+1", APIBase: srv.URL,
	})
	long := strings.Repeat("x", 1000)
	_ = c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r1", Kind: "firing",
		Description: long, Address: "+2",
	})
	body := f.snapshot()[0].Form.Get("Body")
	if len(body) > 480 {
		t.Errorf("body should be byte-capped at 480, got %d bytes", len(body))
	}
	if !strings.Contains(body, "…") {
		t.Errorf("body should include ellipsis when truncated")
	}
}

// ---- Send: error paths ----

func TestSend_Non2xxReturnsError(t *testing.T) {
	_, srv := newFakeTwilio(t, http.StatusBadRequest)
	c := sms.New(sms.Config{
		AccountSID: "AC", AuthToken: "tok",
		FromNumber: "+1", APIBase: srv.URL,
	})
	err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing", Address: "+2",
	})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("want 400, got %v", err)
	}
}

func TestSend_NetworkErrorPropagates(t *testing.T) {
	c := sms.New(sms.Config{
		AccountSID: "AC", AuthToken: "tok",
		FromNumber: "+1", APIBase: "http://127.0.0.1:1",
	})
	err := c.Send(context.Background(), channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing", Address: "+2",
	})
	if err == nil {
		t.Fatalf("want network error")
	}
}

func TestSend_ContextCancellation(t *testing.T) {
	_, srv := newFakeTwilio(t, http.StatusCreated)
	c := sms.New(sms.Config{
		AccountSID: "AC", AuthToken: "tok",
		FromNumber: "+1", APIBase: srv.URL,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.Send(ctx, channel.Notification{
		IncidentID: "i", RuleID: "r", Kind: "firing", Address: "+2",
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}
