// Package api exposes the HTTP API for the signalwatch service. The API
// covers full CRUD for rules, subscribers, and subscriptions; read-only
// access to incidents, notifications, and live state; and an event ingest
// endpoint that forwards into the engine.
package api

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ryan-evans-git/signalwatch/engine"
	"github.com/ryan-evans-git/signalwatch/internal/auth"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
	"github.com/ryan-evans-git/signalwatch/internal/subscriber"
)

// Mount registers handlers on the given mux. Pass an embedded UI
// handler (or nil) to mount the SPA at "/". Additional options
// (auth, etc.) come through the variadic MountOption parameter so
// adding optional config doesn't break existing callers.
//
// Routes /healthz and the SPA root stay open regardless of auth
// settings — /healthz so load balancers / readiness checks work,
// and the SPA so the login screen can render. Every /v1/* route is
// gated by WithAPIToken when configured. /v1/auth-status is open so
// the SPA can probe whether auth is on before rendering.
func Mount(mux *http.ServeMux, eng *engine.Engine, ui http.Handler, opts ...MountOption) {
	var s mountSettings
	for _, o := range opts {
		o(&s)
	}

	h := &handlers{eng: eng, settings: s}

	// readGate gates a route requiring at least ScopeRead. writeGate
	// requires ScopeAdmin. Both compose requireAuth so auth failures
	// short-circuit before the scope check runs.
	readGate := func(fn http.HandlerFunc) http.HandlerFunc {
		return requireAuth(&s, requireScope(auth.ScopeRead, fn))
	}
	writeGate := func(fn http.HandlerFunc) http.HandlerFunc {
		return requireAuth(&s, requireScope(auth.ScopeAdmin, fn))
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /v1/auth-status", h.authStatus)
	mux.HandleFunc("POST /v1/events", writeGate(h.postEvent))

	mux.HandleFunc("GET /v1/rules", readGate(h.listRules))
	mux.HandleFunc("POST /v1/rules", writeGate(h.createRule))
	mux.HandleFunc("POST /v1/rules/validate", writeGate(h.validateRule))
	mux.HandleFunc("GET /v1/rules/{id}", readGate(h.getRule))
	mux.HandleFunc("PUT /v1/rules/{id}", writeGate(h.updateRule))
	mux.HandleFunc("DELETE /v1/rules/{id}", writeGate(h.deleteRule))

	mux.HandleFunc("GET /v1/subscribers", readGate(h.listSubscribers))
	mux.HandleFunc("POST /v1/subscribers", writeGate(h.createSubscriber))
	mux.HandleFunc("GET /v1/subscribers/{id}", readGate(h.getSubscriber))
	mux.HandleFunc("PUT /v1/subscribers/{id}", writeGate(h.updateSubscriber))
	mux.HandleFunc("DELETE /v1/subscribers/{id}", writeGate(h.deleteSubscriber))

	mux.HandleFunc("GET /v1/subscriptions", readGate(h.listSubscriptions))
	mux.HandleFunc("POST /v1/subscriptions", writeGate(h.createSubscription))
	mux.HandleFunc("GET /v1/subscriptions/{id}", readGate(h.getSubscription))
	mux.HandleFunc("PUT /v1/subscriptions/{id}", writeGate(h.updateSubscription))
	mux.HandleFunc("DELETE /v1/subscriptions/{id}", writeGate(h.deleteSubscription))

	mux.HandleFunc("GET /v1/incidents", readGate(h.listIncidents))
	mux.HandleFunc("GET /v1/incidents/export", readGate(h.exportIncidents))
	mux.HandleFunc("GET /v1/incidents/{id}", readGate(h.getIncident))
	mux.HandleFunc("GET /v1/notifications", readGate(h.listNotifications))
	mux.HandleFunc("GET /v1/states", readGate(h.listStates))

	// Per-user API-token management. Always admin-scoped. Routes are
	// only mounted when a token store is configured — otherwise the
	// endpoints would have nowhere to write to.
	if s.tokens != nil {
		mux.HandleFunc("GET /v1/auth/tokens", writeGate(h.listTokens))
		mux.HandleFunc("POST /v1/auth/tokens", writeGate(h.issueToken))
		mux.HandleFunc("DELETE /v1/auth/tokens/{id}", writeGate(h.revokeToken))
	}

	if ui != nil {
		mux.Handle("/", ui)
	}
}

type handlers struct {
	eng      *engine.Engine
	settings mountSettings
}

// authStatus reports whether the server requires bearer-token auth.
// Open endpoint (no token required) so the UI can decide whether to
// show its login gate before issuing any /v1/* request.
func (h *handlers) authStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"auth_required": h.settings.authRequired(),
	})
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// ---- events ----

type eventPayload struct {
	InputRef string      `json:"input_ref"`
	Record   rule.Record `json:"record"`
}

func (h *handlers) postEvent(w http.ResponseWriter, r *http.Request) {
	var p eventPayload
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if p.Record == nil {
		writeError(w, http.StatusBadRequest, errors.New("record is required"))
		return
	}
	if err := h.eng.Submit(r.Context(), p.InputRef, p.Record); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// ---- rules ----

// rulePayload is the wire form of a rule. The condition field uses the
// discriminated form {type:..., spec:...} that internal/rule supports.
type rulePayload struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Enabled     bool              `json:"enabled"`
	Severity    rule.Severity     `json:"severity"`
	Labels      map[string]string `json:"labels"`
	InputRef    string            `json:"input_ref"`
	Condition   json.RawMessage   `json:"condition"`
	ScheduleSec float64           `json:"schedule_seconds,omitempty"`
}

func (p *rulePayload) toRule() (*rule.Rule, error) {
	cond, err := rule.UnmarshalCondition(p.Condition)
	if err != nil {
		return nil, fmt.Errorf("condition: %w", err)
	}
	r := &rule.Rule{
		ID:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		Enabled:     p.Enabled,
		Severity:    p.Severity,
		Labels:      p.Labels,
		InputRef:    p.InputRef,
		Condition:   cond,
		Schedule:    time.Duration(p.ScheduleSec * float64(time.Second)),
	}
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if r.Severity == "" {
		r.Severity = rule.SeverityInfo
	}
	return r, nil
}

func ruleToPayload(r *rule.Rule) rulePayload {
	condJSON, _ := r.Condition.MarshalCondition()
	return rulePayload{
		ID:          r.ID,
		Name:        r.Name,
		Description: r.Description,
		Enabled:     r.Enabled,
		Severity:    r.Severity,
		Labels:      r.Labels,
		InputRef:    r.InputRef,
		Condition:   condJSON,
		ScheduleSec: r.Schedule.Seconds(),
	}
}

func (h *handlers) listRules(w http.ResponseWriter, r *http.Request) {
	rules, err := h.eng.Rules().List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]rulePayload, 0, len(rules))
	for _, x := range rules {
		out = append(out, ruleToPayload(x))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) createRule(w http.ResponseWriter, r *http.Request) {
	var p rulePayload
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	rec, err := p.toRule()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := h.eng.Rules().Create(r.Context(), rec); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, ruleToPayload(rec))
}

func (h *handlers) getRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, err := h.eng.Rules().Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, errors.New("rule not found"))
		return
	}
	writeJSON(w, http.StatusOK, ruleToPayload(rec))
}

func (h *handlers) updateRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p rulePayload
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	p.ID = id
	rec, err := p.toRule()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := h.eng.Rules().Update(r.Context(), rec); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, ruleToPayload(rec))
}

func (h *handlers) deleteRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.eng.Rules().Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateRule compiles a candidate rule (mainly the condition) without
// persisting it. Returns 200 + {"ok": true} on success, 400 + {error}
// on failure. Lets the UI surface compile errors (especially handy for
// the Expression condition's expr-lang programs) before submit.
func (h *handlers) validateRule(w http.ResponseWriter, r *http.Request) {
	var p rulePayload
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	rec, err := p.toRule()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Validate the rule shape (name, schedule, severity, etc.) and
	// then compile the condition. Both must succeed for the rule to
	// be persistable.
	if err := rec.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if _, err := rec.Condition.Compile(nil); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- subscribers ----

func (h *handlers) listSubscribers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.eng.Subscribers().List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *handlers) createSubscriber(w http.ResponseWriter, r *http.Request) {
	var s subscriber.Subscriber
	if err := decodeJSON(r, &s); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	if err := h.eng.Subscribers().Create(r.Context(), &s); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, s)
}

func (h *handlers) getSubscriber(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, err := h.eng.Subscribers().Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if s == nil {
		writeError(w, http.StatusNotFound, errors.New("subscriber not found"))
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *handlers) updateSubscriber(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var s subscriber.Subscriber
	if err := decodeJSON(r, &s); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.ID = id
	if err := h.eng.Subscribers().Update(r.Context(), &s); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *handlers) deleteSubscriber(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.eng.Subscribers().Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- subscriptions ----

// subscriptionPayload accepts dwell/repeat as seconds for friendlier API
// ergonomics; the internal struct holds them as time.Duration.
type subscriptionPayload struct {
	ID                string            `json:"id"`
	SubscriberID      string            `json:"subscriber_id"`
	RuleID            string            `json:"rule_id,omitempty"`
	LabelSelector     map[string]string `json:"label_selector,omitempty"`
	DwellSec          float64           `json:"dwell_seconds"`
	RepeatIntervalSec float64           `json:"repeat_interval_seconds"`
	NotifyOnResolve   bool              `json:"notify_on_resolve"`
	ChannelFilter     []string          `json:"channel_filter,omitempty"`
}

func (p *subscriptionPayload) toSubscription() *subscriber.Subscription {
	s := &subscriber.Subscription{
		ID:              p.ID,
		SubscriberID:    p.SubscriberID,
		RuleID:          p.RuleID,
		LabelSelector:   p.LabelSelector,
		Dwell:           time.Duration(p.DwellSec * float64(time.Second)),
		RepeatInterval:  time.Duration(p.RepeatIntervalSec * float64(time.Second)),
		NotifyOnResolve: p.NotifyOnResolve,
		ChannelFilter:   p.ChannelFilter,
	}
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	return s
}

func subscriptionToPayload(s *subscriber.Subscription) subscriptionPayload {
	return subscriptionPayload{
		ID:                s.ID,
		SubscriberID:      s.SubscriberID,
		RuleID:            s.RuleID,
		LabelSelector:     s.LabelSelector,
		DwellSec:          s.Dwell.Seconds(),
		RepeatIntervalSec: s.RepeatInterval.Seconds(),
		NotifyOnResolve:   s.NotifyOnResolve,
		ChannelFilter:     s.ChannelFilter,
	}
}

func (h *handlers) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	rows, err := h.eng.Subscriptions().List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]subscriptionPayload, 0, len(rows))
	for _, s := range rows {
		out = append(out, subscriptionToPayload(s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) createSubscription(w http.ResponseWriter, r *http.Request) {
	var p subscriptionPayload
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s := p.toSubscription()
	if err := s.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := h.eng.Subscriptions().Create(r.Context(), s); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, subscriptionToPayload(s))
}

func (h *handlers) getSubscription(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, err := h.eng.Subscriptions().Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if s == nil {
		writeError(w, http.StatusNotFound, errors.New("subscription not found"))
		return
	}
	writeJSON(w, http.StatusOK, subscriptionToPayload(s))
}

func (h *handlers) updateSubscription(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p subscriptionPayload
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	p.ID = id
	s := p.toSubscription()
	if err := s.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := h.eng.Subscriptions().Update(r.Context(), s); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, subscriptionToPayload(s))
}

func (h *handlers) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.eng.Subscriptions().Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- read-only ----

func parseLimit(r *http.Request) int {
	q := r.URL.Query().Get("limit")
	if q == "" {
		return 100
	}
	n, err := strconv.Atoi(q)
	if err != nil || n <= 0 {
		return 100
	}
	if n > 1000 {
		n = 1000
	}
	return n
}

func (h *handlers) listIncidents(w http.ResponseWriter, r *http.Request) {
	// Optional ?rule_id= filter narrows the list to one rule's
	// timeline — used by the per-rule drill-down UI route.
	if ruleID := strings.TrimSpace(r.URL.Query().Get("rule_id")); ruleID != "" {
		rows, err := h.eng.Incidents().ListForRule(r.Context(), ruleID, parseLimit(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, rows)
		return
	}
	rows, err := h.eng.Incidents().List(r.Context(), parseLimit(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// exportIncidents emits all incidents matching the optional ?rule_id=
// + ?since= filters in either JSON or CSV form. Used by the
// alert-history export workflow.
//
// `since` accepts RFC3339 (preferred) or a Go duration (e.g. "168h")
// which subtracts from now. Omitting `since` exports everything in
// the store.
func (h *handlers) exportIncidents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := strings.ToLower(strings.TrimSpace(q.Get("format")))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" {
		writeError(w, http.StatusBadRequest, errors.New("format must be json or csv"))
		return
	}

	since, err := parseSince(q.Get("since"), time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// Pull a generous slice — exports aren't bounded by parseLimit's
	// default page size. Use 0 to mean "all that the store will
	// return"; the per-driver implementations cap themselves.
	var (
		rows     []*subscriber.Incident
		fetchErr error
	)
	ruleID := strings.TrimSpace(q.Get("rule_id"))
	if ruleID != "" {
		rows, fetchErr = h.eng.Incidents().ListForRule(r.Context(), ruleID, 0)
	} else {
		rows, fetchErr = h.eng.Incidents().List(r.Context(), 0)
	}
	if fetchErr != nil {
		writeError(w, http.StatusInternalServerError, fetchErr)
		return
	}

	if !since.IsZero() {
		filtered := rows[:0]
		for _, inc := range rows {
			if !inc.TriggeredAt.Before(since) {
				filtered = append(filtered, inc)
			}
		}
		rows = filtered
	}

	switch format {
	case "json":
		writeJSON(w, http.StatusOK, rows)
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="incidents.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"id", "rule_id", "triggered_at", "resolved_at", "last_value"})
		for _, inc := range rows {
			resolved := ""
			if !inc.ResolvedAt.IsZero() {
				resolved = inc.ResolvedAt.UTC().Format(time.RFC3339)
			}
			_ = cw.Write([]string{
				inc.ID,
				inc.RuleID,
				inc.TriggeredAt.UTC().Format(time.RFC3339),
				resolved,
				inc.LastValue,
			})
		}
		cw.Flush()
	}
}

// parseSince accepts either an RFC3339 timestamp or a Go duration
// string (e.g. "168h"). A duration is subtracted from `now`. Empty
// returns a zero time which the caller treats as "no lower bound".
func parseSince(raw string, now time.Time) (time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d < 0 {
			return time.Time{}, errors.New("since: duration must be non-negative")
		}
		return now.Add(-d), nil
	}
	return time.Time{}, errors.New("since: expected RFC3339 or Go duration (e.g. 168h)")
}

func (h *handlers) getIncident(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inc, err := h.eng.Incidents().Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if inc == nil {
		writeError(w, http.StatusNotFound, errors.New("incident not found"))
		return
	}
	notes, _ := h.eng.Notifications().ListForIncident(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"incident": inc, "notifications": notes})
}

func (h *handlers) listNotifications(w http.ResponseWriter, r *http.Request) {
	rows, err := h.eng.Notifications().List(r.Context(), parseLimit(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *handlers) listStates(w http.ResponseWriter, r *http.Request) {
	rows, err := h.eng.LiveStates().List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}
