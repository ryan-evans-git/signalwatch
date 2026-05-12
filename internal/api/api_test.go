package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ryan-evans-git/signalwatch/engine"
	"github.com/ryan-evans-git/signalwatch/internal/api"
	"github.com/ryan-evans-git/signalwatch/internal/channel"
	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/input/event"
	"github.com/ryan-evans-git/signalwatch/internal/store/sqlite"
	"github.com/ryan-evans-git/signalwatch/internal/subscriber"
)

// fixture wires an in-memory store + minimal engine + httptest.Server with
// the api routes mounted. Tests interact via HTTP only.
type fixture struct {
	srv    *httptest.Server
	eng    *engine.Engine
	store  *sqlite.Store
	cancel context.CancelFunc
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := sqlite.Open("file::memory:?cache=shared&mode=memory&_pragma=foreign_keys(1)&test_id=" + t.Name())
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	evInput := event.New("events")
	eng, err := engine.New(engine.Options{
		Store:      st,
		Channels:   map[string]channel.Channel{},
		Inputs:     []input.Input{evInput},
		EventInput: evInput,
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
	api.Mount(mux, eng, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Tiny UI stub so the / route mounts and Mount's ui != nil branch
		// is exercised.
		_, _ = w.Write([]byte("ui"))
	}))
	srv := httptest.NewServer(mux)

	t.Cleanup(func() {
		srv.Close()
		cancel()
		_ = eng.Close()
		_ = st.Close()
	})

	return &fixture{srv: srv, eng: eng, store: st, cancel: cancel}
}

// response is the (status, resp.Body) the helpers return. We deliberately
// don't surface *http.Response — the helpers close the body internally,
// and returning the struct itself confuses the bodyclose linter into
// thinking callers should close it.
type response struct {
	StatusCode int
	Body       []byte
}

func (f *fixture) get(t *testing.T, path string) response {
	t.Helper()
	resp, err := http.Get(f.srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return response{StatusCode: resp.StatusCode, Body: body}
}

func (f *fixture) do(t *testing.T, method, path string, body any) response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		if raw, ok := body.([]byte); ok {
			rdr = bytes.NewReader(raw)
		} else {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			rdr = bytes.NewReader(b)
		}
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return response{StatusCode: resp.StatusCode, Body: respBody}
}

// ---------------- mount / health / ui ----------------

func TestMount_HealthAndUI(t *testing.T) {
	f := newFixture(t)

	resp := f.get(t, "/healthz")
	if resp.StatusCode != http.StatusOK || string(resp.Body) != "ok" {
		t.Fatalf("healthz: status=%d body=%q", resp.StatusCode, resp.Body)
	}
	resp = f.get(t, "/")
	if resp.StatusCode != http.StatusOK || string(resp.Body) != "ui" {
		t.Fatalf("/: status=%d body=%q", resp.StatusCode, resp.Body)
	}
}

func TestMount_NoUIHandler(t *testing.T) {
	// Cover Mount's ui == nil branch via a separate mux/server.
	st, err := sqlite.Open("file::memory:?cache=shared&test_id=" + t.Name())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	defer st.Close()

	ev := event.New("events")
	eng, err := engine.New(engine.Options{
		Store: st, Channels: map[string]channel.Channel{},
		Inputs: []input.Input{ev}, EventInput: ev, Logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_ = eng.Start(ctx)
	defer cancel()
	defer eng.Close()

	mux := http.NewServeMux()
	api.Mount(mux, eng, nil) // no UI

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Healthz still works.
	resp, _ := http.Get(srv.URL + "/healthz")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %d", resp.StatusCode)
	}
}

// ---------------- events ----------------

func TestPostEvent_AcceptsValidRecord(t *testing.T) {
	f := newFixture(t)
	resp := f.do(t, http.MethodPost, "/v1/events", map[string]any{
		"input_ref": "events",
		"record":    map[string]any{"level": "ERROR"},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status: want 202, got %d (%s)", resp.StatusCode, resp.Body)
	}
}

func TestPostEvent_RejectsMalformedJSON(t *testing.T) {
	f := newFixture(t)
	resp := f.do(t, http.MethodPost, "/v1/events", []byte("{not json"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", resp.StatusCode)
	}
}

func TestPostEvent_RejectsMissingRecord(t *testing.T) {
	f := newFixture(t)
	resp := f.do(t, http.MethodPost, "/v1/events", map[string]any{"input_ref": "events"})
	if resp.StatusCode != http.StatusBadRequest || !bytes.Contains(resp.Body, []byte("record is required")) {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}

// ---------------- rules ----------------

func sampleRulePayload(id string) map[string]any {
	return map[string]any{
		"id":        id,
		"name":      "rule-" + id,
		"enabled":   true,
		"input_ref": "events",
		"condition": map[string]any{
			"type": "pattern_match",
			"spec": map[string]any{"field": "level", "kind": "contains", "pattern": "ERROR"},
		},
	}
}

func TestRules_FullCRUD(t *testing.T) {
	f := newFixture(t)

	// Create
	resp := f.do(t, http.MethodPost, "/v1/rules", sampleRulePayload("r1"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /v1/rules: %d %s", resp.StatusCode, resp.Body)
	}

	// List
	resp = f.get(t, "/v1/rules")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET list: %d %s", resp.StatusCode, resp.Body)
	}
	var rules []map[string]any
	_ = json.Unmarshal(resp.Body, &rules)
	if len(rules) != 1 || rules[0]["name"] != "rule-r1" {
		t.Fatalf("list body: %s", resp.Body)
	}

	// Get
	resp = f.get(t, "/v1/rules/r1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET id: %d %s", resp.StatusCode, resp.Body)
	}

	// Update
	patch := sampleRulePayload("r1")
	patch["name"] = "renamed"
	resp = f.do(t, http.MethodPut, "/v1/rules/r1", patch)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: %d %s", resp.StatusCode, resp.Body)
	}

	// Delete
	resp = f.do(t, http.MethodDelete, "/v1/rules/r1", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: %d", resp.StatusCode)
	}

	// 404 after delete
	resp = f.get(t, "/v1/rules/r1")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after-delete GET: %d", resp.StatusCode)
	}
}

func TestRules_PostMalformedJSONIs400(t *testing.T) {
	f := newFixture(t)
	resp := f.do(t, http.MethodPost, "/v1/rules", []byte("{not json"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestRules_PostUnknownConditionTypeIs400(t *testing.T) {
	f := newFixture(t)
	payload := sampleRulePayload("r1")
	payload["condition"] = map[string]any{"type": "weather", "spec": map[string]any{}}
	resp := f.do(t, http.MethodPost, "/v1/rules", payload)
	if resp.StatusCode != http.StatusBadRequest || !bytes.Contains(resp.Body, []byte("condition")) {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
}

func TestRules_PutMalformedJSONIs400(t *testing.T) {
	f := newFixture(t)
	resp := f.do(t, http.MethodPut, "/v1/rules/r1", []byte("{not json"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestRules_PutUnknownConditionIs400(t *testing.T) {
	f := newFixture(t)
	payload := sampleRulePayload("r1")
	payload["condition"] = map[string]any{"type": "weather", "spec": map[string]any{}}
	resp := f.do(t, http.MethodPut, "/v1/rules/r1", payload)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestRules_DefaultIDAndSeverity(t *testing.T) {
	f := newFixture(t)
	payload := sampleRulePayload("")
	delete(payload, "id")
	payload["severity"] = "" // empty -> info default
	resp := f.do(t, http.MethodPost, "/v1/rules", payload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST: %d %s", resp.StatusCode, resp.Body)
	}
	var got map[string]any
	_ = json.Unmarshal(resp.Body, &got)
	if got["id"] == "" {
		t.Fatalf("id default missing: %s", resp.Body)
	}
	if got["severity"] != "info" {
		t.Fatalf("severity default: want info, got %v", got["severity"])
	}
}

func TestRules_GetMissingIs404(t *testing.T) {
	f := newFixture(t)
	resp := f.get(t, "/v1/rules/nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

// ---------------- subscribers ----------------

func sampleSubscriberPayload(id string) *subscriber.Subscriber {
	return &subscriber.Subscriber{
		ID:   id,
		Name: "S-" + id,
		Channels: []subscriber.ChannelBinding{
			{Channel: "ch", Address: "a@x"},
		},
	}
}

func TestSubscribers_FullCRUD(t *testing.T) {
	f := newFixture(t)

	resp := f.do(t, http.MethodPost, "/v1/subscribers", sampleSubscriberPayload("s1"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST: %d", resp.StatusCode)
	}
	resp = f.get(t, "/v1/subscribers")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET list: %d %s", resp.StatusCode, resp.Body)
	}

	resp = f.get(t, "/v1/subscribers/s1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET id: %d %s", resp.StatusCode, resp.Body)
	}

	patch := sampleSubscriberPayload("s1")
	patch.Name = "Updated"
	resp = f.do(t, http.MethodPut, "/v1/subscribers/s1", patch)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}

	resp = f.do(t, http.MethodDelete, "/v1/subscribers/s1", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: %d", resp.StatusCode)
	}
}

func TestSubscribers_PostMalformedJSONIs400(t *testing.T) {
	f := newFixture(t)
	resp := f.do(t, http.MethodPost, "/v1/subscribers", []byte("{not json"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestSubscribers_PutMalformedJSONIs400(t *testing.T) {
	f := newFixture(t)
	resp := f.do(t, http.MethodPut, "/v1/subscribers/s1", []byte("{not json"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestSubscribers_DefaultsID(t *testing.T) {
	f := newFixture(t)
	payload := sampleSubscriberPayload("")
	payload.ID = ""
	resp := f.do(t, http.MethodPost, "/v1/subscribers", payload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var got map[string]any
	_ = json.Unmarshal(resp.Body, &got)
	if got["id"] == "" {
		t.Fatalf("id default missing")
	}
}

func TestSubscribers_GetMissingIs404(t *testing.T) {
	f := newFixture(t)
	resp := f.get(t, "/v1/subscribers/nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

// ---------------- subscriptions ----------------

func sampleSubscriptionPayload(id, subscriberID, ruleID string) map[string]any {
	return map[string]any{
		"id":            id,
		"subscriber_id": subscriberID,
		"rule_id":       ruleID,
		"dwell_seconds": 0,
	}
}

func TestSubscriptions_FullCRUD(t *testing.T) {
	f := newFixture(t)
	// Seed subscriber + rule the subscription references.
	_ = f.do(t, http.MethodPost, "/v1/subscribers", sampleSubscriberPayload("s1"))
	_ = f.do(t, http.MethodPost, "/v1/rules", sampleRulePayload("r1"))

	resp := f.do(t, http.MethodPost, "/v1/subscriptions", sampleSubscriptionPayload("subscr-1", "s1", "r1"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST: %d %s", resp.StatusCode, resp.Body)
	}

	resp = f.get(t, "/v1/subscriptions")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("LIST: %d", resp.StatusCode)
	}
	resp = f.get(t, "/v1/subscriptions/subscr-1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET id: %d", resp.StatusCode)
	}

	patch := sampleSubscriptionPayload("subscr-1", "s1", "r1")
	patch["dwell_seconds"] = 30
	resp = f.do(t, http.MethodPut, "/v1/subscriptions/subscr-1", patch)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}

	resp = f.do(t, http.MethodDelete, "/v1/subscriptions/subscr-1", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: %d", resp.StatusCode)
	}
}

func TestSubscriptions_PostInvalidIs400(t *testing.T) {
	f := newFixture(t)
	// Missing subscriber_id and rule_id together fails Validate.
	resp := f.do(t, http.MethodPost, "/v1/subscriptions", map[string]any{"id": "x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d body=%s", resp.StatusCode, resp.Body)
	}
}

func TestSubscriptions_PostMalformedJSONIs400(t *testing.T) {
	f := newFixture(t)
	resp := f.do(t, http.MethodPost, "/v1/subscriptions", []byte("{not json"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestSubscriptions_PutMalformedJSONIs400(t *testing.T) {
	f := newFixture(t)
	resp := f.do(t, http.MethodPut, "/v1/subscriptions/s", []byte("{not json"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestSubscriptions_PutInvalidIs400(t *testing.T) {
	f := newFixture(t)
	resp := f.do(t, http.MethodPut, "/v1/subscriptions/s", map[string]any{"id": "s"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestSubscriptions_DefaultsID(t *testing.T) {
	f := newFixture(t)
	_ = f.do(t, http.MethodPost, "/v1/subscribers", sampleSubscriberPayload("s1"))
	_ = f.do(t, http.MethodPost, "/v1/rules", sampleRulePayload("r1"))
	payload := sampleSubscriptionPayload("", "s1", "r1")
	resp := f.do(t, http.MethodPost, "/v1/subscriptions", payload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var got map[string]any
	_ = json.Unmarshal(resp.Body, &got)
	if got["id"] == "" {
		t.Fatalf("id default missing")
	}
}

func TestSubscriptions_GetMissingIs404(t *testing.T) {
	f := newFixture(t)
	resp := f.get(t, "/v1/subscriptions/nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

// ---------------- read-only ----------------

func TestReadOnly_ListIncidentsAndNotificationsAndStates(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{"/v1/incidents", "/v1/notifications", "/v1/states"} {
		resp := f.get(t, p)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status=%d body=%s", p, resp.StatusCode, resp.Body)
		}
	}
}

func TestReadOnly_ListSubscribersWorks(t *testing.T) {
	// Was missing in the cross-package coverage report; just ensure list works.
	f := newFixture(t)
	resp := f.get(t, "/v1/subscribers")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestGetIncident_MissingIs404(t *testing.T) {
	f := newFixture(t)
	resp := f.get(t, "/v1/incidents/nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

// parseLimit branches: missing → default, invalid → default, capped at 1000.
func TestParseLimit_Branches(t *testing.T) {
	f := newFixture(t)
	cases := []string{
		"/v1/incidents",
		"/v1/incidents?limit=10",
		"/v1/incidents?limit=invalid",
		"/v1/incidents?limit=999999", // clamps to 1000
		"/v1/incidents?limit=-5",     // <=0 falls back to default
		"/v1/notifications?limit=10",
	}
	for _, p := range cases {
		resp := f.get(t, p)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status=%d", p, resp.StatusCode)
		}
	}
}

// ---------------- 500 paths (broken store) ----------------

// Closing the underlying store mid-test drives every "if err != nil { 500 }"
// path in the read handlers. Some write handlers map errors to 400.
func TestRepoErrors_ProduceErrorStatuses(t *testing.T) {
	f := newFixture(t)

	// Seed a rule so the GET path goes through Get successfully first
	// (no-op for our purposes — we want the Close-induced failure on the
	// next call).
	_ = f.do(t, http.MethodPost, "/v1/rules", sampleRulePayload("r1"))
	_ = f.do(t, http.MethodPost, "/v1/subscribers", sampleSubscriberPayload("s1"))
	_ = f.do(t, http.MethodPost, "/v1/subscriptions", sampleSubscriptionPayload("subscr-1", "s1", "r1"))

	if err := f.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	cases := []struct {
		method, path string
		wantStatus   []int // accept any of these
		body         any
	}{
		{http.MethodGet, "/v1/rules", []int{500}, nil},
		{http.MethodGet, "/v1/rules/r1", []int{500}, nil},
		{http.MethodPost, "/v1/rules", []int{400, 500}, sampleRulePayload("r2")},
		{http.MethodPut, "/v1/rules/r1", []int{400, 500}, sampleRulePayload("r1")},
		{http.MethodDelete, "/v1/rules/r1", []int{500}, nil},

		{http.MethodGet, "/v1/subscribers", []int{500}, nil},
		{http.MethodGet, "/v1/subscribers/s1", []int{500}, nil},
		{http.MethodPost, "/v1/subscribers", []int{400, 500}, sampleSubscriberPayload("s2")},
		{http.MethodPut, "/v1/subscribers/s1", []int{400, 500}, sampleSubscriberPayload("s1")},
		{http.MethodDelete, "/v1/subscribers/s1", []int{500}, nil},

		{http.MethodGet, "/v1/subscriptions", []int{500}, nil},
		{http.MethodGet, "/v1/subscriptions/subscr-1", []int{500}, nil},
		{http.MethodPost, "/v1/subscriptions", []int{400, 500}, sampleSubscriptionPayload("subscr-2", "s1", "r1")},
		{http.MethodPut, "/v1/subscriptions/subscr-1", []int{400, 500}, sampleSubscriptionPayload("subscr-1", "s1", "r1")},
		{http.MethodDelete, "/v1/subscriptions/subscr-1", []int{500}, nil},

		{http.MethodGet, "/v1/incidents", []int{500}, nil},
		{http.MethodGet, "/v1/incidents/nope", []int{500}, nil},
		{http.MethodGet, "/v1/notifications", []int{500}, nil},
		{http.MethodGet, "/v1/states", []int{500}, nil},

		// POST /v1/events: Submit goes through the engine's event input
		// channel which is still alive — it doesn't touch the store —
		// so this returns 202 even with the store closed. Verify that
		// invariant rather than expecting 5xx.
		{http.MethodPost, "/v1/events", []int{202}, map[string]any{"input_ref": "events", "record": map[string]any{"x": 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			resp := f.do(t, tc.method, tc.path, tc.body)
			ok := false
			for _, s := range tc.wantStatus {
				if resp.StatusCode == s {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("status: want one of %v, got %d", tc.wantStatus, resp.StatusCode)
			}
		})
	}
}

// Smoke-test the postEvent 500 path: shut down the engine so Submit fails
// while the HTTP server is still up.
func TestPostEvent_EngineSubmitErrorIs500(t *testing.T) {
	f := newFixture(t)
	// Cancel and close the engine. Submit will return "not started" once
	// Close cleared the sink.
	_ = f.eng.Close()
	// Give the engine a moment to settle.
	time.Sleep(50 * time.Millisecond)

	resp := f.do(t, http.MethodPost, "/v1/events",
		map[string]any{"input_ref": "events", "record": map[string]any{"x": 1}})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d body=%s", resp.StatusCode, resp.Body)
	}
	if !strings.Contains(string(resp.Body), "error") {
		t.Errorf("body should contain error string, got %s", resp.Body)
	}
}
