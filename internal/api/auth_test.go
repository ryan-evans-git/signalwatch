package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ryan-evans-git/signalwatch/engine"
	"github.com/ryan-evans-git/signalwatch/internal/api"
	"github.com/ryan-evans-git/signalwatch/internal/channel"
	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/input/event"
	"github.com/ryan-evans-git/signalwatch/internal/store/sqlite"
)

// newAuthFixture is a stripped-down fixture that bundles the same
// engine + httptest.Server setup as the main api_test.go fixture, but
// with an explicit api token and using the raw http.Client so callers
// control the Authorization header.
type authFixture struct {
	srv     *httptest.Server
	cleanup func()
}

func newAuthFixture(t *testing.T, token string) *authFixture {
	t.Helper()
	st, err := sqlite.Open("file::memory:?cache=shared&mode=memory&test_id=" + t.Name())
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ev := event.New("events")
	eng, err := engine.New(engine.Options{
		Store:      st,
		Channels:   map[string]channel.Channel{},
		Inputs:     []input.Input{ev},
		EventInput: ev,
		Logger:     slog.Default(),
	})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}

	mux := http.NewServeMux()
	api.Mount(mux, eng, nil, api.WithAPIToken(token))
	srv := httptest.NewServer(mux)

	return &authFixture{
		srv: srv,
		cleanup: func() {
			srv.Close()
			cancel()
			_ = eng.Close()
			_ = st.Close()
		},
	}
}

// doRaw is a minimal HTTP helper that closes the response body
// internally and returns (status, body). Unlike the main api_test
// fixture's helpers, it lets the caller set arbitrary headers so we
// can test the auth path.
func doRaw(t *testing.T, method, url, token string, body []byte) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// ---------------- /v1/auth-status ----------------

func TestAuthStatus_OpenEndpoint_ReportsWhenEnabled(t *testing.T) {
	f := newAuthFixture(t, "s3cr3t")
	defer f.cleanup()
	status, body := doRaw(t, http.MethodGet, f.srv.URL+"/v1/auth-status", "", nil)
	if status != http.StatusOK {
		t.Fatalf("status: want 200, got %d", status)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["auth_required"] != true {
		t.Fatalf("auth_required: want true, got %v", got["auth_required"])
	}
}

func TestAuthStatus_OpenEndpoint_ReportsWhenDisabled(t *testing.T) {
	f := newAuthFixture(t, "")
	defer f.cleanup()
	status, body := doRaw(t, http.MethodGet, f.srv.URL+"/v1/auth-status", "", nil)
	if status != http.StatusOK {
		t.Fatalf("status: want 200, got %d", status)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["auth_required"] != false {
		t.Fatalf("auth_required: want false, got %v", got["auth_required"])
	}
}

// ---------------- gated /v1/* ----------------

func TestAuth_MissingHeaderIs401(t *testing.T) {
	f := newAuthFixture(t, "s3cr3t")
	defer f.cleanup()
	status, body := doRaw(t, http.MethodGet, f.srv.URL+"/v1/rules", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d (%s)", status, body)
	}
	if !bytes.Contains(body, []byte("missing Authorization")) {
		t.Errorf("error body should call out the missing header: %s", body)
	}
}

func TestAuth_WrongSchemeIs401(t *testing.T) {
	f := newAuthFixture(t, "s3cr3t")
	defer f.cleanup()
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/v1/rules", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz") // not Bearer
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("Bearer")) {
		t.Errorf("error body should call out the wrong scheme: %s", body)
	}
}

func TestAuth_EmptyBearerIs401(t *testing.T) {
	f := newAuthFixture(t, "s3cr3t")
	defer f.cleanup()
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/v1/rules", nil)
	req.Header.Set("Authorization", "Bearer   ") // bearer + whitespace
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d", resp.StatusCode)
	}
}

func TestAuth_WrongTokenIs401(t *testing.T) {
	f := newAuthFixture(t, "s3cr3t")
	defer f.cleanup()
	status, body := doRaw(t, http.MethodGet, f.srv.URL+"/v1/rules", "wrong", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d (%s)", status, body)
	}
	if !bytes.Contains(body, []byte("invalid token")) {
		t.Errorf("error body should call out invalid token: %s", body)
	}
}

func TestAuth_CorrectTokenIsAccepted(t *testing.T) {
	f := newAuthFixture(t, "s3cr3t")
	defer f.cleanup()
	status, body := doRaw(t, http.MethodGet, f.srv.URL+"/v1/rules", "s3cr3t", nil)
	if status != http.StatusOK {
		t.Fatalf("status: want 200, got %d (%s)", status, body)
	}
}

// Constant-time comparison: tokens of differing length must still
// 401 cleanly without panicking or short-circuiting. Pin the
// behavior so a future refactor doesn't accidentally re-introduce a
// length-leak short-circuit.
func TestAuth_DifferentLengthTokensReject(t *testing.T) {
	f := newAuthFixture(t, "s3cr3t")
	defer f.cleanup()
	status, _ := doRaw(t, http.MethodGet, f.srv.URL+"/v1/rules", "s", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("short token: want 401, got %d", status)
	}
	status, _ = doRaw(t, http.MethodGet, f.srv.URL+"/v1/rules", "s3cr3ttoolong", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("long token: want 401, got %d", status)
	}
}

func TestAuth_EmptyTokenConfigured_NoAuthRequired(t *testing.T) {
	f := newAuthFixture(t, "")
	defer f.cleanup()
	status, body := doRaw(t, http.MethodGet, f.srv.URL+"/v1/rules", "", nil)
	if status != http.StatusOK {
		t.Fatalf("status: want 200, got %d (%s)", status, body)
	}
}

func TestAuth_HealthzStaysOpen(t *testing.T) {
	f := newAuthFixture(t, "s3cr3t")
	defer f.cleanup()
	status, body := doRaw(t, http.MethodGet, f.srv.URL+"/healthz", "", nil)
	if status != http.StatusOK || string(body) != "ok" {
		t.Fatalf("healthz: status=%d body=%q", status, body)
	}
}

// /v1/events should also be gated.
func TestAuth_EventsPostGated(t *testing.T) {
	f := newAuthFixture(t, "s3cr3t")
	defer f.cleanup()
	payload, _ := json.Marshal(map[string]any{
		"input_ref": "events",
		"record":    map[string]any{"level": "ERROR"},
	})
	status, _ := doRaw(t, http.MethodPost, f.srv.URL+"/v1/events", "", payload)
	if status != http.StatusUnauthorized {
		t.Fatalf("missing-token POST events: want 401, got %d", status)
	}
	status, _ = doRaw(t, http.MethodPost, f.srv.URL+"/v1/events", "s3cr3t", payload)
	if status != http.StatusAccepted {
		t.Fatalf("authed POST events: want 202, got %d", status)
	}
}
