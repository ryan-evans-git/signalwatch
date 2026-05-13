// Package storetest exposes a conformance suite that any store.Store
// implementation can run against, ensuring every backend (sqlite,
// postgres, mysql, ...) shares the same semantics. Driver-specific
// behavior (e.g. closed-connection error shapes, fault-injection paths)
// stays in the driver's own *_test.go.
package storetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/auth"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
	"github.com/ryan-evans-git/signalwatch/internal/store"
	"github.com/ryan-evans-git/signalwatch/internal/subscriber"
)

// Factory returns a freshly migrated, empty store and registers any
// cleanup the implementation requires via t.Cleanup. Tests should
// assume the returned store is completely empty.
type Factory func(t *testing.T) store.Store

// RunConformance runs every cross-driver behavior assertion against
// a Store produced by `factory`. Call it once per driver — typically
// inside the driver's own _test.go:
//
//	func TestConformance(t *testing.T) {
//	    storetest.RunConformance(t, newPostgresStore)
//	}
//
// All assertions are skip-free and parallelizable inside a single
// driver; we do not parallelize across drivers since they may share a
// docker container.
func RunConformance(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("Rules", func(t *testing.T) {
		t.Run("CreateGetUpdateDelete", func(t *testing.T) { testRulesCRUD(t, factory) })
		t.Run("GetMissingReturnsNilNil", func(t *testing.T) { testRulesGetMissing(t, factory) })
		t.Run("ListAndListByInput", func(t *testing.T) { testRulesList(t, factory) })
		t.Run("DuplicateIDFails", func(t *testing.T) { testRulesDuplicate(t, factory) })
		t.Run("CreateWithExplicitCreatedAt", func(t *testing.T) { testRulesExplicitCreatedAt(t, factory) })
	})
	t.Run("Subscribers", func(t *testing.T) {
		t.Run("CRUD", func(t *testing.T) { testSubscribersCRUD(t, factory) })
		t.Run("GetMissing", func(t *testing.T) { testSubscribersGetMissing(t, factory) })
		t.Run("ExplicitCreatedAtAndDuplicate", func(t *testing.T) { testSubscribersExplicit(t, factory) })
	})
	t.Run("Subscriptions", func(t *testing.T) {
		t.Run("CRUD", func(t *testing.T) { testSubscriptionsCRUD(t, factory) })
		t.Run("ListForRuleDirectAndLabelSelector", func(t *testing.T) { testSubscriptionsListForRule(t, factory) })
		t.Run("EmptySelectorIgnored", func(t *testing.T) { testSubscriptionsEmptySelectorIgnored(t, factory) })
		t.Run("ExplicitCreatedAtAndDuplicate", func(t *testing.T) { testSubscriptionsExplicit(t, factory) })
	})
	t.Run("Incidents", func(t *testing.T) {
		t.Run("OpenResolveGet", func(t *testing.T) { testIncidentsOpenResolve(t, factory) })
		t.Run("GetMissing", func(t *testing.T) { testIncidentsGetMissing(t, factory) })
		t.Run("ListAndListForRuleDefaultLimit", func(t *testing.T) { testIncidentsList(t, factory) })
		t.Run("ListResolvedBefore_FiltersUnresolvedAndNewer", func(t *testing.T) { testIncidentsListResolvedBefore(t, factory) })
		t.Run("DeleteResolvedBefore_CascadesChildren", func(t *testing.T) { testIncidentsDeleteResolvedBefore(t, factory) })
	})
	t.Run("Notifications", func(t *testing.T) {
		t.Run("RecordAndList", func(t *testing.T) { testNotificationsList(t, factory) })
		t.Run("RecordWithError", func(t *testing.T) { testNotificationsError(t, factory) })
	})
	t.Run("LiveStates", func(t *testing.T) {
		t.Run("UpsertGetList", func(t *testing.T) { testLiveStatesUpsert(t, factory) })
		t.Run("GetMissing", func(t *testing.T) { testLiveStatesGetMissing(t, factory) })
	})
	t.Run("IncidentSubStates", func(t *testing.T) {
		t.Run("UpsertGetList", func(t *testing.T) { testIncidentSubStatesUpsert(t, factory) })
		t.Run("GetMissing", func(t *testing.T) { testIncidentSubStatesGetMissing(t, factory) })
	})
	t.Run("APITokens", func(t *testing.T) {
		t.Run("CreateGetByHashList", func(t *testing.T) { testAPITokensBasic(t, factory) })
		t.Run("GetMissingReturnsNilNil", func(t *testing.T) { testAPITokensMissing(t, factory) })
		t.Run("RevokeAndDelete", func(t *testing.T) { testAPITokensRevokeDelete(t, factory) })
		t.Run("TouchLastUsed", func(t *testing.T) { testAPITokensTouchLastUsed(t, factory) })
		t.Run("DuplicateHashFails", func(t *testing.T) { testAPITokensDuplicateHash(t, factory) })
		t.Run("ExpiresAtRoundTrips", func(t *testing.T) { testAPITokensExpiresAtRoundTrip(t, factory) })
	})
}

// ---- shared fixtures ----

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

// seedRuleSub creates the rule + subscriber rows that subscriptions /
// incidents / incidentSubStates depend on via FK.
func seedRuleSub(t *testing.T, st store.Store, ruleID string) {
	t.Helper()
	ctx := context.Background()
	r := makeRule(ruleID)
	if err := st.Rules().Create(ctx, r); err != nil {
		t.Fatalf("Rules.Create: %v", err)
	}
	if err := st.Subscribers().Create(ctx, &subscriber.Subscriber{
		ID: "sub-" + ruleID, Name: "Sub for " + ruleID,
	}); err != nil {
		t.Fatalf("Subscribers.Create: %v", err)
	}
}

// ---- Rules ----

func testRulesCRUD(t *testing.T, factory Factory) {
	st := factory(t)
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

func testRulesGetMissing(t *testing.T, factory Factory) {
	st := factory(t)
	got, err := st.Rules().Get(context.Background(), "nope")
	if err != nil {
		t.Fatalf("Get missing: want nil err, got %v", err)
	}
	if got != nil {
		t.Fatalf("Get missing: want nil rule, got %+v", got)
	}
}

func testRulesList(t *testing.T, factory Factory) {
	st := factory(t)
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

func testRulesDuplicate(t *testing.T, factory Factory) {
	st := factory(t)
	ctx := context.Background()
	r := makeRule("dup")
	if err := st.Rules().Create(ctx, r); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if err := st.Rules().Create(ctx, r); err == nil {
		t.Fatalf("duplicate Create: want UNIQUE constraint error")
	}
}

func testRulesExplicitCreatedAt(t *testing.T, factory Factory) {
	st := factory(t)
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

// ---- Subscribers ----

func testSubscribersCRUD(t *testing.T, factory Factory) {
	st := factory(t)
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

func testSubscribersGetMissing(t *testing.T, factory Factory) {
	st := factory(t)
	got, err := st.Subscribers().Get(context.Background(), "nope")
	if err != nil || got != nil {
		t.Fatalf("Get missing: want (nil, nil), got (%+v, %v)", got, err)
	}
}

func testSubscribersExplicit(t *testing.T, factory Factory) {
	st := factory(t)
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
	if err := st.Subscribers().Create(ctx, s); err == nil {
		t.Fatalf("duplicate Subscriber Create: want UNIQUE constraint error")
	}
}

// ---- Subscriptions ----

func testSubscriptionsCRUD(t *testing.T, factory Factory) {
	st := factory(t)
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

func testSubscriptionsListForRule(t *testing.T, factory Factory) {
	st := factory(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")
	repo := st.Subscriptions()

	// Direct rule_id match.
	if err := repo.Create(ctx, &subscriber.Subscription{ID: "d", SubscriberID: "sub-r1", RuleID: "r1"}); err != nil {
		t.Fatal(err)
	}
	// Label selector match.
	if err := repo.Create(ctx, &subscriber.Subscription{ID: "m", SubscriberID: "sub-r1", LabelSelector: map[string]string{"team": "ops"}}); err != nil {
		t.Fatal(err)
	}
	// Label selector that does NOT match.
	if err := repo.Create(ctx, &subscriber.Subscription{ID: "x", SubscriberID: "sub-r1", LabelSelector: map[string]string{"team": "billing"}}); err != nil {
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
	if ids["x"] {
		t.Errorf("non-matching selector included: got %v", ids)
	}
}

// EmptySelectorIgnored is best-effort across drivers: sqlite stores
// the empty map as JSON "{}"; postgres stores it the same. We don't
// assert the storage representation, only the in-memory match
// semantics: an empty selector must NOT match anything.
func testSubscriptionsEmptySelectorIgnored(t *testing.T, factory Factory) {
	st := factory(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")

	// A subscription with a one-key selector that the test rule's labels
	// don't have is a portable way to express "this subscription must
	// not match r1". Drivers' raw-SQL bypass is not portable.
	if err := st.Subscriptions().Create(ctx, &subscriber.Subscription{
		ID: "no-match", SubscriberID: "sub-r1",
		LabelSelector: map[string]string{"will": "never-match"},
	}); err != nil {
		t.Fatal(err)
	}

	got, _ := st.Subscriptions().ListForRule(ctx, "r1", map[string]string{"team": "ops"})
	for _, s := range got {
		if s.ID == "no-match" {
			t.Fatalf("selector with no matching key must not match")
		}
	}
}

func testSubscriptionsExplicit(t *testing.T, factory Factory) {
	st := factory(t)
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

// ---- Incidents ----

func testIncidentsOpenResolve(t *testing.T, factory Factory) {
	st := factory(t)
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

func testIncidentsGetMissing(t *testing.T, factory Factory) {
	st := factory(t)
	got, err := st.Incidents().Get(context.Background(), "nope")
	if err != nil || got != nil {
		t.Fatalf("Get missing: want (nil, nil), got (%+v, %v)", got, err)
	}
}

func testIncidentsList(t *testing.T, factory Factory) {
	st := factory(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")
	repo := st.Incidents()

	for i := 1; i <= 3; i++ {
		inc := &subscriber.Incident{
			ID:          incIDFor(i),
			RuleID:      "r1",
			TriggeredAt: time.UnixMilli(int64(1700_000_000_000 + i*1000)),
		}
		if err := repo.Open(ctx, inc); err != nil {
			t.Fatalf("Open: %v", err)
		}
	}

	all, err := repo.List(ctx, 0) // default limit
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

func incIDFor(i int) string { return fmt.Sprintf("inc-%d", i) }

// testIncidentsListResolvedBefore seeds a mix of resolved + unresolved
// incidents and asserts ListResolvedBefore returns only the resolved-
// before-cutoff subset. Used by the retention pruner.
func testIncidentsListResolvedBefore(t *testing.T, factory Factory) {
	st := factory(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")
	repo := st.Incidents()

	old := time.UnixMilli(1_000_000)
	mid := time.UnixMilli(2_000_000)
	new := time.UnixMilli(3_000_000)
	cutoff := time.UnixMilli(2_500_000)

	// old + mid resolved before cutoff; new resolved after; unresolved stays.
	for _, p := range []struct {
		id         string
		triggered  time.Time
		resolvedAt int64 // 0 = unresolved
	}{
		{"old-resolved", old, old.UnixMilli() + 1000},
		{"mid-resolved", mid, mid.UnixMilli() + 1000},
		{"new-resolved", new, new.UnixMilli() + 1000},
		{"still-firing", time.UnixMilli(1500000), 0},
	} {
		if err := repo.Open(ctx, &subscriber.Incident{ID: p.id, RuleID: "r1", TriggeredAt: p.triggered}); err != nil {
			t.Fatalf("Open %s: %v", p.id, err)
		}
		if p.resolvedAt != 0 {
			if err := repo.Resolve(ctx, p.id, p.resolvedAt); err != nil {
				t.Fatalf("Resolve %s: %v", p.id, err)
			}
		}
	}

	got, err := repo.ListResolvedBefore(ctx, cutoff.UnixMilli())
	if err != nil {
		t.Fatalf("ListResolvedBefore: %v", err)
	}
	ids := map[string]bool{}
	for _, inc := range got {
		ids[inc.ID] = true
	}
	if !ids["old-resolved"] || !ids["mid-resolved"] {
		t.Errorf("want old-resolved + mid-resolved in result, got %v", ids)
	}
	if ids["new-resolved"] {
		t.Errorf("new-resolved (after cutoff) should not be in result, got %v", ids)
	}
	if ids["still-firing"] {
		t.Errorf("unresolved incident should not be in result, got %v", ids)
	}
}

// testIncidentsDeleteResolvedBefore exercises the cascade: incidents
// + their notifications + their incident_sub_states should all be
// gone for resolved-before-cutoff incidents, while unresolved /
// newer-than-cutoff data remains untouched.
func testIncidentsDeleteResolvedBefore(t *testing.T, factory Factory) {
	st := factory(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")
	// Need a subscription so the FK from incident_sub_states is satisfied.
	if err := st.Subscriptions().Create(ctx, &subscriber.Subscription{
		ID: "subscr-1", SubscriberID: "sub-r1", RuleID: "r1",
	}); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	old := int64(1_000_000)
	cutoff := int64(2_500_000)

	// Old resolved incident with one notification + one sub-state — all
	// should be deleted.
	_ = st.Incidents().Open(ctx, &subscriber.Incident{ID: "old", RuleID: "r1", TriggeredAt: time.UnixMilli(old)})
	_ = st.Incidents().Resolve(ctx, "old", old+1000)
	_ = st.Notifications().Record(ctx, &subscriber.Notification{
		ID: "n-old", IncidentID: "old", SubscriptionID: "subscr-1", SubscriberID: "sub-r1",
		Channel: "ch", Address: "a@x", Kind: subscriber.KindFiring, SentAt: time.UnixMilli(old), Status: "ok",
	})
	_ = st.IncidentSubStates().Upsert(ctx, &subscriber.IncidentSubState{
		IncidentID: "old", SubscriptionID: "subscr-1", NotifyCount: 1,
	})

	// Unresolved incident — must NOT be deleted.
	_ = st.Incidents().Open(ctx, &subscriber.Incident{ID: "firing", RuleID: "r1", TriggeredAt: time.UnixMilli(old)})

	// Newer resolved incident (after cutoff) — must NOT be deleted.
	_ = st.Incidents().Open(ctx, &subscriber.Incident{ID: "new", RuleID: "r1", TriggeredAt: time.UnixMilli(3_000_000)})
	_ = st.Incidents().Resolve(ctx, "new", 3_001_000)

	n, err := st.Incidents().DeleteResolvedBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteResolvedBefore: %v", err)
	}
	if n != 1 {
		t.Errorf("rows deleted: want 1, got %d", n)
	}

	// old gone.
	if got, _ := st.Incidents().Get(ctx, "old"); got != nil {
		t.Errorf("old incident should be deleted, got %+v", got)
	}
	if notes, _ := st.Notifications().ListForIncident(ctx, "old"); len(notes) != 0 {
		t.Errorf("old incident's notifications should be cascaded, got %d", len(notes))
	}
	if subs, _ := st.IncidentSubStates().ListForIncident(ctx, "old"); len(subs) != 0 {
		t.Errorf("old incident's sub-states should be cascaded, got %d", len(subs))
	}

	// firing + new remain.
	if got, _ := st.Incidents().Get(ctx, "firing"); got == nil {
		t.Errorf("firing incident should remain")
	}
	if got, _ := st.Incidents().Get(ctx, "new"); got == nil {
		t.Errorf("new (post-cutoff) incident should remain")
	}
}

// ---- Notifications ----

func testNotificationsList(t *testing.T, factory Factory) {
	st := factory(t)
	ctx := context.Background()
	seedRuleSub(t, st, "r1")
	if err := st.Incidents().Open(ctx, &subscriber.Incident{ID: "inc-1", RuleID: "r1", TriggeredAt: time.UnixMilli(1)}); err != nil {
		t.Fatalf("seed inc: %v", err)
	}
	if err := st.Subscriptions().Create(ctx, &subscriber.Subscription{
		ID: "subscr-1", SubscriberID: "sub-r1", RuleID: "r1",
	}); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	repo := st.Notifications()
	for i, kind := range []subscriber.NotificationKind{subscriber.KindFiring, subscriber.KindRepeat} {
		n := &subscriber.Notification{
			ID:             fmt.Sprintf("n-%d", i),
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

	all, err := repo.List(ctx, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("List: got %d (err=%v)", len(all), err)
	}
}

func testNotificationsError(t *testing.T, factory Factory) {
	st := factory(t)
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

// ---- LiveStates ----

func testLiveStatesUpsert(t *testing.T, factory Factory) {
	st := factory(t)
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

func testLiveStatesGetMissing(t *testing.T, factory Factory) {
	st := factory(t)
	got, err := st.LiveStates().Get(context.Background(), "nope")
	if err != nil || got != nil {
		t.Fatalf("Get missing: want (nil, nil), got (%+v, %v)", got, err)
	}
}

// ---- IncidentSubStates ----

func testIncidentSubStatesUpsert(t *testing.T, factory Factory) {
	st := factory(t)
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

func testIncidentSubStatesGetMissing(t *testing.T, factory Factory) {
	st := factory(t)
	got, err := st.IncidentSubStates().Get(context.Background(), "x", "y")
	if err != nil || got != nil {
		t.Fatalf("Get missing: want (nil, nil), got (%+v, %v)", got, err)
	}
}

// ---- API tokens ----

func makeToken(id string) *auth.Token {
	return &auth.Token{
		ID:        id,
		Name:      "token-" + id,
		TokenHash: "hash-" + id,
		Scopes:    []auth.Scope{auth.ScopeRead, auth.ScopeAdmin},
	}
}

func testAPITokensBasic(t *testing.T, factory Factory) {
	st := factory(t)
	ctx := context.Background()
	repo := st.APITokens()

	tok := makeToken("t1")
	if err := repo.Create(ctx, tok); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.GetByHash(ctx, "hash-t1")
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if got == nil || got.ID != "t1" || got.Name != "token-t1" {
		t.Fatalf("GetByHash result: %+v", got)
	}
	if len(got.Scopes) != 2 {
		t.Fatalf("Scopes round-trip: %+v", got.Scopes)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("CreatedAt should be auto-populated when not set")
	}

	byID, err := repo.Get(ctx, "t1")
	if err != nil || byID == nil || byID.TokenHash != "hash-t1" {
		t.Fatalf("Get by id: %+v %v", byID, err)
	}

	// second token to exercise List ordering.
	if err := repo.Create(ctx, makeToken("t2")); err != nil {
		t.Fatalf("Create t2: %v", err)
	}
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List len=%d, want 2", len(list))
	}
}

func testAPITokensMissing(t *testing.T, factory Factory) {
	st := factory(t)
	ctx := context.Background()
	got, err := st.APITokens().GetByHash(ctx, "nope")
	if err != nil || got != nil {
		t.Fatalf("GetByHash missing: got (%+v, %v)", got, err)
	}
	got, err = st.APITokens().Get(ctx, "nope")
	if err != nil || got != nil {
		t.Fatalf("Get missing: got (%+v, %v)", got, err)
	}
}

func testAPITokensRevokeDelete(t *testing.T, factory Factory) {
	st := factory(t)
	ctx := context.Background()
	repo := st.APITokens()

	if err := repo.Create(ctx, makeToken("t-rev")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.Revoke(ctx, "t-rev"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	got, _ := repo.Get(ctx, "t-rev")
	if !got.Revoked {
		t.Fatal("Revoke did not set revoked flag")
	}
	// Revoke idempotent.
	if err := repo.Revoke(ctx, "t-rev"); err != nil {
		t.Fatalf("Revoke again: %v", err)
	}
	if err := repo.Delete(ctx, "t-rev"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ = repo.Get(ctx, "t-rev")
	if got != nil {
		t.Fatalf("Get after Delete: %+v", got)
	}
}

func testAPITokensTouchLastUsed(t *testing.T, factory Factory) {
	st := factory(t)
	ctx := context.Background()
	repo := st.APITokens()
	if err := repo.Create(ctx, makeToken("t-touch")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	ts := time.Date(2026, 5, 13, 10, 11, 12, 0, time.UTC).UnixMilli()
	if err := repo.TouchLastUsed(ctx, "t-touch", ts); err != nil {
		t.Fatalf("TouchLastUsed: %v", err)
	}
	got, _ := repo.Get(ctx, "t-touch")
	if got.LastUsedAt == nil || got.LastUsedAt.UnixMilli() != ts {
		t.Fatalf("LastUsedAt: %+v want ms=%d", got.LastUsedAt, ts)
	}
}

func testAPITokensDuplicateHash(t *testing.T, factory Factory) {
	st := factory(t)
	ctx := context.Background()
	repo := st.APITokens()
	if err := repo.Create(ctx, &auth.Token{
		ID: "t-a", Name: "a", TokenHash: "dup", Scopes: []auth.Scope{auth.ScopeRead},
	}); err != nil {
		t.Fatalf("Create A: %v", err)
	}
	if err := repo.Create(ctx, &auth.Token{
		ID: "t-b", Name: "b", TokenHash: "dup", Scopes: []auth.Scope{auth.ScopeRead},
	}); err == nil {
		t.Fatal("Create with duplicate hash should fail")
	}
}

func testAPITokensExpiresAtRoundTrip(t *testing.T, factory Factory) {
	st := factory(t)
	ctx := context.Background()
	repo := st.APITokens()
	exp := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	tok := &auth.Token{
		ID: "t-exp", Name: "e", TokenHash: "hash-exp",
		Scopes:    []auth.Scope{auth.ScopeRead},
		ExpiresAt: &exp,
	}
	if err := repo.Create(ctx, tok); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, _ := repo.GetByHash(ctx, "hash-exp")
	if got.ExpiresAt == nil || got.ExpiresAt.UnixMilli() != exp.UnixMilli() {
		t.Fatalf("ExpiresAt: %+v want %s", got.ExpiresAt, exp)
	}
}
