package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/rule"
	"github.com/ryan-evans-git/signalwatch/internal/store/sqlite"
	"github.com/ryan-evans-git/signalwatch/internal/subscriber"
)

// newStore opens an in-memory SQLite store and runs migrations. Each test
// gets its own database via a unique DSN so they don't share rows.
func newStore(t *testing.T) *sqlite.Store {
	t.Helper()
	// Use a unique in-memory DSN per test. The `cache=shared` mode keeps
	// the single-connection pool alive even though it's memory-only.
	st, err := sqlite.Open("file::memory:?cache=shared&mode=memory&_pragma=foreign_keys(1)&" + uniq(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func uniq(t *testing.T) string {
	t.Helper()
	return "test_id=" + t.Name()
}

// ---------------- Open / Close / Migrate ----------------

func TestOpen_RejectsBadDSN(t *testing.T) {
	// A path under a directory that doesn't exist makes sqlite's Ping
	// fail with a filesystem error. Pinging is what Open uses to
	// surface bad DSNs early.
	_, err := sqlite.Open("file:/nonexistent/dir-" + t.Name() + "/x.db")
	if err == nil {
		t.Fatalf("expected error from unreachable path")
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	st := newStore(t)
	// Run again — should be a no-op because all migrations are applied.
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate (idempotent): %v", err)
	}
}

func TestDB_ReturnsHandle(t *testing.T) {
	st := newStore(t)
	if st.DB() == nil {
		t.Fatal("DB() returned nil")
	}
}

// ---------------- Rules ----------------

func makeRule(id string) *rule.Rule {
	return &rule.Rule{
		ID:       id,
		Name:     "rule-" + id,
		Enabled:  true,
		Severity: rule.SeverityWarning,
		InputRef: "events",
		Labels:   map[string]string{"team": "ops"},
		Condition: rule.PatternMatch{
			Field: "level", Kind: rule.MatchContains, Pattern: "ERROR",
		},
	}
}

func TestRules_CreateGetUpdateDelete(t *testing.T) {
	st := newStore(t)
	repo := st.Rules()
	ctx := context.Background()

	r := makeRule("r1")
	if err := repo.Create(ctx, r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.Get(ctx, "r1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.Name != "rule-r1" || got.InputRef != "events" {
		t.Fatalf("Get round-trip: %+v", got)
	}
	if got.Labels["team"] != "ops" {
		t.Errorf("Labels lost in round-trip: %v", got.Labels)
	}

	got.Name = "rule-r1-renamed"
	got.Enabled = false
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got2, _ := repo.Get(ctx, "r1")
	if got2.Name != "rule-r1-renamed" || got2.Enabled {
		t.Fatalf("Update not persisted: %+v", got2)
	}

	if err := repo.Delete(ctx, "r1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	miss, err := repo.Get(ctx, "r1")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if miss != nil {
		t.Fatalf("Get after delete: want nil, got %+v", miss)
	}
}

func TestRules_GetMissingReturnsNilNil(t *testing.T) {
	st := newStore(t)
	got, err := st.Rules().Get(context.Background(), "nope")
	if err != nil {
		t.Fatalf("Get missing: want nil err, got %v", err)
	}
	if got != nil {
		t.Fatalf("Get missing: want nil rule, got %+v", got)
	}
}

func TestRules_ListAndListByInput(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	repo := st.Rules()

	a := makeRule("a")
	a.Name = "a"
	a.InputRef = "events"
	b := makeRule("b")
	b.Name = "b"
	b.InputRef = "metrics"
	c := makeRule("c")
	c.Name = "c"
	c.InputRef = "events"
	c.Enabled = false // ListByInput must filter

	for _, r := range []*rule.Rule{a, b, c} {
		if err := repo.Create(ctx, r); err != nil {
			t.Fatalf("Create %s: %v", r.ID, err)
		}
	}

	all, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List: want 3, got %d", len(all))
	}

	byInput, err := repo.ListByInput(ctx, "events")
	if err != nil {
		t.Fatalf("ListByInput: %v", err)
	}
	// Only enabled events-input rule "a" should come back.
	if len(byInput) != 1 || byInput[0].ID != "a" {
		ids := make([]string, len(byInput))
		for i, r := range byInput {
			ids[i] = r.ID
		}
		t.Fatalf("ListByInput: want [a], got %v", ids)
	}
}

// ---------------- Subscribers ----------------

func TestSubscribers_CRUDAndList(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	repo := st.Subscribers()

	a := &subscriber.Subscriber{ID: "s1", Name: "Alpha", Channels: []subscriber.ChannelBinding{{Channel: "ch", Address: "a@x"}}}
	b := &subscriber.Subscriber{ID: "s2", Name: "Bravo"}
	for _, s := range []*subscriber.Subscriber{a, b} {
		if err := repo.Create(ctx, s); err != nil {
			t.Fatalf("Create %s: %v", s.ID, err)
		}
	}

	got, err := repo.Get(ctx, "s1")
	if err != nil || got == nil {
		t.Fatalf("Get: err=%v got=%+v", err, got)
	}
	if len(got.Channels) != 1 || got.Channels[0].Channel != "ch" {
		t.Fatalf("Channels lost: %+v", got.Channels)
	}

	got.Name = "Alpha v2"
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got2, _ := repo.Get(ctx, "s1")
	if got2.Name != "Alpha v2" {
		t.Fatalf("Update not persisted: %s", got2.Name)
	}

	all, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("List: want 2, got %d", len(all))
	}

	if err := repo.Delete(ctx, "s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, _ := repo.Get(ctx, "s1"); got != nil {
		t.Fatalf("Get after Delete: %+v", got)
	}
}

func TestSubscribers_GetMissing(t *testing.T) {
	st := newStore(t)
	got, err := st.Subscribers().Get(context.Background(), "nope")
	if err != nil || got != nil {
		t.Fatalf("Get missing: want (nil, nil), got (%+v, %v)", got, err)
	}
}

// ---------------- Subscriptions ----------------

func seedRuleSub(t *testing.T, st *sqlite.Store, ruleID string) {
	t.Helper()
	r := makeRule(ruleID)
	if err := st.Rules().Create(context.Background(), r); err != nil {
		t.Fatalf("Rules.Create: %v", err)
	}
	if err := st.Subscribers().Create(context.Background(), &subscriber.Subscriber{
		ID: "sub-" + ruleID, Name: "Sub for " + ruleID,
	}); err != nil {
		t.Fatalf("Subscribers.Create: %v", err)
	}
}

func TestSubscriptions_CRUDAndList(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")
	repo := st.Subscriptions()

	s := &subscriber.Subscription{
		ID:              "subscr-1",
		SubscriberID:    "sub-r1",
		RuleID:          "r1",
		Dwell:           2 * time.Minute,
		RepeatInterval:  5 * time.Minute,
		NotifyOnResolve: true,
		ChannelFilter:   []string{"ch"},
	}
	if err := repo.Create(ctx, s); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.Get(ctx, "subscr-1")
	if err != nil || got == nil {
		t.Fatalf("Get: err=%v got=%+v", err, got)
	}
	if got.Dwell != 2*time.Minute || got.RepeatInterval != 5*time.Minute || !got.NotifyOnResolve {
		t.Fatalf("Subscription fields not round-tripped: %+v", got)
	}
	if len(got.ChannelFilter) != 1 || got.ChannelFilter[0] != "ch" {
		t.Fatalf("ChannelFilter lost: %+v", got.ChannelFilter)
	}

	got.RepeatInterval = 10 * time.Minute
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got2, _ := repo.Get(ctx, "subscr-1")
	if got2.RepeatInterval != 10*time.Minute {
		t.Fatalf("Update not persisted")
	}

	all, err := repo.List(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("List: len=%d err=%v", len(all), err)
	}

	if err := repo.Delete(ctx, "subscr-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, _ := repo.Get(ctx, "subscr-1"); got != nil {
		t.Fatalf("Get after Delete: %+v", got)
	}
}

func TestSubscriptions_ListForRule_DirectAndLabelSelector(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")
	repo := st.Subscriptions()

	// Direct rule_id match.
	direct := &subscriber.Subscription{ID: "d", SubscriberID: "sub-r1", RuleID: "r1"}
	if err := repo.Create(ctx, direct); err != nil {
		t.Fatal(err)
	}
	// Label selector match.
	matching := &subscriber.Subscription{ID: "m", SubscriberID: "sub-r1", LabelSelector: map[string]string{"team": "ops"}}
	if err := repo.Create(ctx, matching); err != nil {
		t.Fatal(err)
	}
	// Label selector that does NOT match.
	mismatch := &subscriber.Subscription{ID: "x", SubscriberID: "sub-r1", LabelSelector: map[string]string{"team": "billing"}}
	if err := repo.Create(ctx, mismatch); err != nil {
		t.Fatal(err)
	}
	// Empty LabelSelector must not match-anything by accident.
	emptySel := &subscriber.Subscription{ID: "e", SubscriberID: "sub-r1", LabelSelector: map[string]string{}}
	// Create with non-rule + non-label fails Subscription.Validate so we
	// don't gate on Validate at the store layer; bypass via setting
	// LabelSelector with one element then removing it isn't possible —
	// instead set a placeholder label and rely on the in-memory match
	// being false. Use one not present in the rule.
	emptySel.LabelSelector = map[string]string{"will": "never-match"}
	if err := repo.Create(ctx, emptySel); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ListForRule(ctx, "r1", map[string]string{"team": "ops"})
	if err != nil {
		t.Fatalf("ListForRule: %v", err)
	}
	ids := map[string]bool{}
	for _, s := range got {
		ids[s.ID] = true
	}
	if !ids["d"] || !ids["m"] {
		t.Errorf("missing matches: got %v", ids)
	}
	if ids["x"] || ids["e"] {
		t.Errorf("non-matching selectors included: got %v", ids)
	}
}

func TestSubscriptions_ListForRule_EmptySelectorIgnored(t *testing.T) {
	// Cover matchLabels' early-return for an empty selector. The current
	// store layer prevents this via JSON encoding (empty map encodes as
	// "{}" then parses to len-0). We exercise it by creating a row whose
	// label_selector JSON column is "{}".
	st := newStore(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")

	if _, err := st.DB().ExecContext(ctx, `INSERT INTO subscriptions
        (id, subscriber_id, rule_id, label_selector, dwell_ns, repeat_interval_ns, notify_on_resolve, channel_filter, created_at, updated_at)
        VALUES (?, ?, NULL, '{}', 0, 0, 0, 'null', 0, 0)`, "empty", "sub-r1"); err != nil {
		t.Fatalf("seed empty: %v", err)
	}

	got, err := st.Subscriptions().ListForRule(ctx, "r1", map[string]string{"team": "ops"})
	if err != nil {
		t.Fatalf("ListForRule: %v", err)
	}
	for _, s := range got {
		if s.ID == "empty" {
			t.Fatalf("empty selector must not match")
		}
	}
}

// ---------------- Incidents ----------------

func TestIncidents_OpenResolveGet(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")
	repo := st.Incidents()

	inc := &subscriber.Incident{
		ID:          "inc-1",
		RuleID:      "r1",
		TriggeredAt: time.UnixMilli(1700_000_000_000),
		LastValue:   "ERROR x3",
	}
	if err := repo.Open(ctx, inc); err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := repo.Get(ctx, "inc-1")
	if err != nil || got == nil {
		t.Fatalf("Get: err=%v got=%+v", err, got)
	}
	if got.RuleID != "r1" || got.LastValue != "ERROR x3" {
		t.Fatalf("Get round-trip: %+v", got)
	}
	if !got.ResolvedAt.IsZero() {
		t.Errorf("Resolved before resolve: %v", got.ResolvedAt)
	}

	if err := repo.Resolve(ctx, "inc-1", time.UnixMilli(1700_000_300_000).UnixMilli()); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got, _ = repo.Get(ctx, "inc-1")
	if got.ResolvedAt.IsZero() {
		t.Fatalf("ResolvedAt not persisted")
	}
}

func TestIncidents_GetMissing(t *testing.T) {
	st := newStore(t)
	got, err := st.Incidents().Get(context.Background(), "nope")
	if err != nil || got != nil {
		t.Fatalf("Get missing: want (nil, nil), got (%+v, %v)", got, err)
	}
}

func TestIncidents_ListAndListForRule_DefaultLimit(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")
	repo := st.Incidents()

	for i := 1; i <= 3; i++ {
		inc := &subscriber.Incident{
			ID:          tcID(i),
			RuleID:      "r1",
			TriggeredAt: time.UnixMilli(int64(1700_000_000_000 + i*1000)),
		}
		if err := repo.Open(ctx, inc); err != nil {
			t.Fatalf("Open: %v", err)
		}
	}

	all, err := repo.List(ctx, 0) // 0 -> default limit (100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List: want 3, got %d", len(all))
	}

	byRule, err := repo.ListForRule(ctx, "r1", 0)
	if err != nil {
		t.Fatalf("ListForRule: %v", err)
	}
	if len(byRule) != 3 {
		t.Fatalf("ListForRule: want 3, got %d", len(byRule))
	}
}

func tcID(i int) string { return "inc-" + string(rune('0'+i)) }

// ---------------- Notifications ----------------

func TestNotifications_RecordAndList(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")
	if err := st.Incidents().Open(ctx, &subscriber.Incident{ID: "inc-1", RuleID: "r1", TriggeredAt: time.UnixMilli(1)}); err != nil {
		t.Fatalf("seed inc: %v", err)
	}
	// Create the subscription this notification references.
	if err := st.Subscriptions().Create(ctx, &subscriber.Subscription{
		ID: "subscr-1", SubscriberID: "sub-r1", RuleID: "r1",
	}); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	repo := st.Notifications()
	for i, kind := range []subscriber.NotificationKind{subscriber.KindFiring, subscriber.KindRepeat} {
		n := &subscriber.Notification{
			ID:             "n-" + string(rune('0'+i)),
			IncidentID:     "inc-1",
			SubscriptionID: "subscr-1",
			SubscriberID:   "sub-r1",
			Channel:        "ch",
			Address:        "ops@x",
			Kind:           kind,
			SentAt:         time.UnixMilli(int64(i + 1)),
			Status:         "ok",
		}
		if err := repo.Record(ctx, n); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	got, err := repo.ListForIncident(ctx, "inc-1")
	if err != nil || len(got) != 2 {
		t.Fatalf("ListForIncident: got %d notifications (err=%v)", len(got), err)
	}

	all, err := repo.List(ctx, 0) // default limit
	if err != nil || len(all) != 2 {
		t.Fatalf("List: got %d (err=%v)", len(all), err)
	}
}

func TestNotifications_RecordWithError(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")
	_ = st.Incidents().Open(ctx, &subscriber.Incident{ID: "inc-1", RuleID: "r1", TriggeredAt: time.UnixMilli(1)})
	_ = st.Subscriptions().Create(ctx, &subscriber.Subscription{ID: "subscr-1", SubscriberID: "sub-r1", RuleID: "r1"})

	n := &subscriber.Notification{
		ID:             "n-err",
		IncidentID:     "inc-1",
		SubscriptionID: "subscr-1",
		SubscriberID:   "sub-r1",
		Channel:        "ch",
		Address:        "ops@x",
		Kind:           subscriber.KindFiring,
		SentAt:         time.UnixMilli(1),
		Status:         "error",
		Error:          "smtp timeout",
	}
	if err := st.Notifications().Record(ctx, n); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err := st.Notifications().ListForIncident(ctx, "inc-1")
	if err != nil || len(got) != 1 || got[0].Error != "smtp timeout" {
		t.Fatalf("ListForIncident: got=%+v err=%v", got, err)
	}
}

// ---------------- LiveStates ----------------

func TestLiveStates_UpsertGetList(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")
	repo := st.LiveStates()

	ls := &rule.LiveState{
		RuleID:      "r1",
		State:       rule.StateFiring,
		TriggeredAt: time.UnixMilli(1700_000_000_000),
		LastEvalAt:  time.UnixMilli(1700_000_010_000),
		LastValue:   "v=5",
		IncidentID:  "inc-1",
	}
	if err := repo.Upsert(ctx, ls); err != nil {
		t.Fatalf("Upsert (insert): %v", err)
	}
	got, err := repo.Get(ctx, "r1")
	if err != nil || got == nil {
		t.Fatalf("Get: err=%v got=%+v", err, got)
	}
	if got.State != rule.StateFiring || got.IncidentID != "inc-1" {
		t.Fatalf("State round-trip: %+v", got)
	}

	// Upsert again, flipping back to OK and clearing TriggeredAt.
	ls.State = rule.StateOK
	ls.TriggeredAt = time.Time{}
	ls.IncidentID = ""
	if err := repo.Upsert(ctx, ls); err != nil {
		t.Fatalf("Upsert (update): %v", err)
	}
	got, _ = repo.Get(ctx, "r1")
	if got.State != rule.StateOK || got.IncidentID != "" {
		t.Fatalf("Upsert-update not persisted: %+v", got)
	}
	if !got.TriggeredAt.IsZero() {
		t.Errorf("TriggeredAt should be zero after clear, got %v", got.TriggeredAt)
	}

	all, err := repo.List(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("List: got %d (err=%v)", len(all), err)
	}
}

func TestLiveStates_GetMissing(t *testing.T) {
	st := newStore(t)
	got, err := st.LiveStates().Get(context.Background(), "nope")
	if err != nil || got != nil {
		t.Fatalf("Get missing: want (nil, nil), got (%+v, %v)", got, err)
	}
}

// ---------------- IncidentSubStates ----------------

func TestIncidentSubStates_UpsertGetList(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")
	_ = st.Incidents().Open(ctx, &subscriber.Incident{ID: "inc-1", RuleID: "r1", TriggeredAt: time.UnixMilli(1)})
	_ = st.Subscriptions().Create(ctx, &subscriber.Subscription{ID: "subscr-1", SubscriberID: "sub-r1", RuleID: "r1"})

	repo := st.IncidentSubStates()
	s := &subscriber.IncidentSubState{
		IncidentID:     "inc-1",
		SubscriptionID: "subscr-1",
		LastNotifiedAt: time.UnixMilli(1700_000_000_000),
		NotifyCount:    1,
	}
	if err := repo.Upsert(ctx, s); err != nil {
		t.Fatalf("Upsert insert: %v", err)
	}
	got, err := repo.Get(ctx, "inc-1", "subscr-1")
	if err != nil || got == nil || got.NotifyCount != 1 {
		t.Fatalf("Get: err=%v got=%+v", err, got)
	}

	s.NotifyCount = 2
	s.ResolutionSent = true
	if err := repo.Upsert(ctx, s); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	got, _ = repo.Get(ctx, "inc-1", "subscr-1")
	if got.NotifyCount != 2 || !got.ResolutionSent {
		t.Fatalf("Upsert update not persisted: %+v", got)
	}

	list, err := repo.ListForIncident(ctx, "inc-1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListForIncident: len=%d err=%v", len(list), err)
	}
}

func TestIncidentSubStates_GetMissing(t *testing.T) {
	st := newStore(t)
	got, err := st.IncidentSubStates().Get(context.Background(), "x", "y")
	if err != nil || got != nil {
		t.Fatalf("Get missing: want (nil, nil), got (%+v, %v)", got, err)
	}
}

// ---------------- malformed-JSON / failure paths ----------------

// A row whose condition column is garbage forces scanRule's
// UnmarshalCondition branch to error. Same JSON column is shared by Get,
// List, and ListByInput so all three propagate the error.
func TestRules_MalformedConditionJSON(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	// Raw insert with garbage in the condition column. The labels column
	// is also "{}" (parses fine) so the scan error is purely the
	// UnmarshalCondition branch.
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO rules
        (id, name, description, enabled, severity, labels, input_ref, condition, schedule_ns, created_at, updated_at)
        VALUES ('bad', 'bad', '', 1, 'warning', '{}', 'events', 'not-json', 0, 0, 0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := st.Rules().Get(ctx, "bad"); err == nil {
		t.Fatalf("Get: want error from malformed condition JSON")
	}
	if _, err := st.Rules().List(ctx); err == nil {
		t.Fatalf("List: want error from malformed condition JSON")
	}
	if _, err := st.Rules().ListByInput(ctx, "events"); err == nil {
		t.Fatalf("ListByInput: want error from malformed condition JSON")
	}
}

// Creating two rules with the same primary-key id fails the second
// INSERT with a UNIQUE constraint violation, covering Create's err
// return path.
func TestRules_DuplicateIDFails(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	r := makeRule("dup")
	if err := st.Rules().Create(ctx, r); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if err := st.Rules().Create(ctx, r); err == nil {
		t.Fatalf("duplicate Create: want UNIQUE constraint error")
	}
}

// Create respects an explicitly-set CreatedAt (covers the !IsZero
// branch in the timestamp setup).
func TestRules_CreateWithExplicitCreatedAt(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	explicit := time.UnixMilli(1700_000_000_000)
	r := makeRule("with-ts")
	r.CreatedAt = explicit
	if err := st.Rules().Create(ctx, r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, _ := st.Rules().Get(ctx, "with-ts")
	if !got.CreatedAt.Equal(explicit) {
		t.Fatalf("CreatedAt: want %v, got %v", explicit, got.CreatedAt)
	}
}

// Same for Subscribers / Subscriptions explicit CreatedAt + duplicate.
func TestSubscribers_CreateWithExplicitCreatedAtAndDuplicate(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	explicit := time.UnixMilli(1700_000_000_000)
	s := &subscriber.Subscriber{ID: "s1", Name: "A", CreatedAt: explicit}
	if err := st.Subscribers().Create(ctx, s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, _ := st.Subscribers().Get(ctx, "s1")
	if !got.CreatedAt.Equal(explicit) {
		t.Fatalf("CreatedAt: want %v, got %v", explicit, got.CreatedAt)
	}
	// Duplicate id.
	if err := st.Subscribers().Create(ctx, s); err == nil {
		t.Fatalf("duplicate Subscriber Create: want UNIQUE constraint error")
	}
}

func TestSubscriptions_CreateWithExplicitCreatedAtAndDuplicate(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")
	explicit := time.UnixMilli(1700_000_000_000)
	s := &subscriber.Subscription{ID: "subscr-1", SubscriberID: "sub-r1", RuleID: "r1", CreatedAt: explicit}
	if err := st.Subscriptions().Create(ctx, s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, _ := st.Subscriptions().Get(ctx, "subscr-1")
	if !got.CreatedAt.Equal(explicit) {
		t.Fatalf("CreatedAt: want %v, got %v", explicit, got.CreatedAt)
	}
	if err := st.Subscriptions().Create(ctx, s); err == nil {
		t.Fatalf("duplicate Subscription Create: want UNIQUE constraint error")
	}
}

// A row in subscriptions with garbage label_selector JSON exercises the
// scanSubscription parseJSON branch. Same logic, different table.
func TestSubscriptions_MalformedJSONIsTolerated(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")
	// Insert with garbage label_selector JSON; parseJSON returns an
	// error which scanSubscription deliberately swallows (the field is
	// best-effort).
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO subscriptions
        (id, subscriber_id, rule_id, label_selector, dwell_ns, repeat_interval_ns, notify_on_resolve, channel_filter, created_at, updated_at)
        VALUES ('bad', 'sub-r1', 'r1', 'not-json', 0, 0, 0, 'null', 0, 0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := st.Subscriptions().Get(ctx, "bad")
	if err != nil {
		t.Fatalf("Get: want nil err (parseJSON is swallowed), got %v", err)
	}
	if got == nil {
		t.Fatalf("Get: want non-nil subscription")
	}
}

// ---------------- closed-DB error paths ----------------

// Closing the underlying DB then calling every repo method drives all
// the "if err := QueryContext/ExecContext...; err != nil { return err }"
// branches in one pass. The exact error text varies; the only
// invariant is that no method returns nil.
func TestAllRepos_ErrorOnClosedDB(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")
	_ = st.Incidents().Open(ctx, &subscriber.Incident{ID: "inc-1", RuleID: "r1", TriggeredAt: time.UnixMilli(1)})
	_ = st.Subscriptions().Create(ctx, &subscriber.Subscription{ID: "subscr-1", SubscriberID: "sub-r1", RuleID: "r1"})

	// Close the DB out from under the repos.
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	calls := []struct {
		name string
		fn   func() error
	}{
		// Rules
		{"Rules.Create", func() error { return st.Rules().Create(ctx, makeRule("z")) }},
		{"Rules.Update", func() error { return st.Rules().Update(ctx, makeRule("z")) }},
		{"Rules.Delete", func() error { return st.Rules().Delete(ctx, "z") }},
		{"Rules.Get", func() error { _, err := st.Rules().Get(ctx, "z"); return err }},
		{"Rules.List", func() error { _, err := st.Rules().List(ctx); return err }},
		{"Rules.ListByInput", func() error { _, err := st.Rules().ListByInput(ctx, "events"); return err }},

		// Subscribers
		{"Subscribers.Create", func() error {
			return st.Subscribers().Create(ctx, &subscriber.Subscriber{ID: "z"})
		}},
		{"Subscribers.Update", func() error {
			return st.Subscribers().Update(ctx, &subscriber.Subscriber{ID: "z"})
		}},
		{"Subscribers.Delete", func() error { return st.Subscribers().Delete(ctx, "z") }},
		{"Subscribers.Get", func() error { _, err := st.Subscribers().Get(ctx, "z"); return err }},
		{"Subscribers.List", func() error { _, err := st.Subscribers().List(ctx); return err }},

		// Subscriptions
		{"Subscriptions.Create", func() error {
			return st.Subscriptions().Create(ctx, &subscriber.Subscription{ID: "z"})
		}},
		{"Subscriptions.Update", func() error {
			return st.Subscriptions().Update(ctx, &subscriber.Subscription{ID: "z"})
		}},
		{"Subscriptions.Delete", func() error { return st.Subscriptions().Delete(ctx, "z") }},
		{"Subscriptions.Get", func() error { _, err := st.Subscriptions().Get(ctx, "z"); return err }},
		{"Subscriptions.List", func() error { _, err := st.Subscriptions().List(ctx); return err }},
		{"Subscriptions.ListForRule", func() error { _, err := st.Subscriptions().ListForRule(ctx, "r", nil); return err }},

		// Incidents
		{"Incidents.Open", func() error {
			return st.Incidents().Open(ctx, &subscriber.Incident{ID: "z", TriggeredAt: time.Now()})
		}},
		{"Incidents.Resolve", func() error { return st.Incidents().Resolve(ctx, "z", time.Now().UnixMilli()) }},
		{"Incidents.Get", func() error { _, err := st.Incidents().Get(ctx, "z"); return err }},
		{"Incidents.List", func() error { _, err := st.Incidents().List(ctx, 0); return err }},
		{"Incidents.ListForRule", func() error { _, err := st.Incidents().ListForRule(ctx, "r", 0); return err }},

		// Notifications
		{"Notifications.Record", func() error {
			return st.Notifications().Record(ctx, &subscriber.Notification{ID: "z", SentAt: time.Now()})
		}},
		{"Notifications.ListForIncident", func() error { _, err := st.Notifications().ListForIncident(ctx, "z"); return err }},
		{"Notifications.List", func() error { _, err := st.Notifications().List(ctx, 0); return err }},

		// LiveStates
		{"LiveStates.Upsert", func() error {
			return st.LiveStates().Upsert(ctx, &rule.LiveState{RuleID: "z"})
		}},
		{"LiveStates.Get", func() error { _, err := st.LiveStates().Get(ctx, "z"); return err }},
		{"LiveStates.List", func() error { _, err := st.LiveStates().List(ctx); return err }},

		// IncidentSubStates
		{"IncidentSubStates.Upsert", func() error {
			return st.IncidentSubStates().Upsert(ctx, &subscriber.IncidentSubState{IncidentID: "z", SubscriptionID: "y"})
		}},
		{"IncidentSubStates.Get", func() error { _, err := st.IncidentSubStates().Get(ctx, "z", "y"); return err }},
		{"IncidentSubStates.ListForIncident", func() error { _, err := st.IncidentSubStates().ListForIncident(ctx, "z"); return err }},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			if err := c.fn(); err == nil {
				t.Fatalf("want error from %s on closed DB", c.name)
			}
		})
	}
}

// ---------------- Close ----------------

func TestClose_NoError(t *testing.T) {
	st, err := sqlite.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
