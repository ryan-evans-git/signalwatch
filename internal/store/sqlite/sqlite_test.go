package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/rule"
	"github.com/ryan-evans-git/signalwatch/internal/store"
	"github.com/ryan-evans-git/signalwatch/internal/store/sqlite"
	"github.com/ryan-evans-git/signalwatch/internal/store/storetest"
	"github.com/ryan-evans-git/signalwatch/internal/subscriber"
)

// ---- conformance ----

// TestConformance runs the cross-driver behavioral suite against the
// sqlite implementation. Driver-specific behavior (closed-connection
// error semantics, malformed-JSON tolerance, foreign-key bypass) is
// tested separately below.
func TestConformance(t *testing.T) {
	storetest.RunConformance(t, newConformanceStore)
}

func newConformanceStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.Open("file::memory:?cache=shared&mode=memory&_pragma=foreign_keys(1)&test_id=" + uniq(t))
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

// newStore is the sqlite-typed factory used by driver-specific tests
// that need access to *sqlite.Store (e.g. the DB() handle for raw SQL).
func newStore(t *testing.T) *sqlite.Store {
	t.Helper()
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

// ---- driver-specific tests ----

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

// A row whose condition column is garbage forces scanRule's
// UnmarshalCondition branch to error. Same JSON column is shared by Get,
// List, and ListByInput so all three propagate the error. This is
// sqlite-specific because we go through DB() directly to seed it.
func TestRules_MalformedConditionJSON(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
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

// scanSubscription tolerates parseJSON failures on the label_selector
// and channel_filter columns. Inject a row with garbage JSON and
// confirm Get returns a row rather than erroring.
func TestSubscriptions_MalformedJSONIsTolerated(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	// Seed the FK rows the row below references.
	if err := st.Rules().Create(ctx, &rule.Rule{
		ID: "r1", Name: "r", Enabled: true, Severity: rule.SeverityInfo,
		InputRef:  "events",
		Condition: rule.PatternMatch{Field: "level", Kind: rule.MatchContains, Pattern: "ERROR"},
	}); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	if err := st.Subscribers().Create(ctx, &subscriber.Subscriber{ID: "sub-r1"}); err != nil {
		t.Fatalf("seed subscriber: %v", err)
	}
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

// Closing the underlying DB then calling every repo method drives all
// the "if err := QueryContext/ExecContext...; err != nil { return err }"
// branches in one pass. The exact error text varies; the only
// invariant is that no method returns nil.
func TestAllRepos_ErrorOnClosedDB(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	// Seed minimal rows so the deletes/updates have targets.
	if err := st.Rules().Create(ctx, &rule.Rule{
		ID: "r1", Name: "r", Enabled: true, Severity: rule.SeverityInfo,
		InputRef:  "events",
		Condition: rule.PatternMatch{Field: "level", Kind: rule.MatchContains, Pattern: "ERROR"},
	}); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	if err := st.Subscribers().Create(ctx, &subscriber.Subscriber{ID: "sub-r1"}); err != nil {
		t.Fatalf("seed subscriber: %v", err)
	}
	if err := st.Incidents().Open(ctx, &subscriber.Incident{ID: "inc-1", RuleID: "r1", TriggeredAt: time.UnixMilli(1)}); err != nil {
		t.Fatalf("seed incident: %v", err)
	}
	if err := st.Subscriptions().Create(ctx, &subscriber.Subscription{ID: "subscr-1", SubscriberID: "sub-r1", RuleID: "r1"}); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	// Close the DB out from under the repos.
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	makeRule := func(id string) *rule.Rule {
		return &rule.Rule{
			ID: id, Name: "rule-" + id, Enabled: true,
			Severity: rule.SeverityWarning, InputRef: "events",
			Condition: rule.PatternMatch{Field: "level", Kind: rule.MatchContains, Pattern: "ERROR"},
		}
	}

	calls := []struct {
		name string
		fn   func() error
	}{
		{"Rules.Create", func() error { return st.Rules().Create(ctx, makeRule("z")) }},
		{"Rules.Update", func() error { return st.Rules().Update(ctx, makeRule("z")) }},
		{"Rules.Delete", func() error { return st.Rules().Delete(ctx, "z") }},
		{"Rules.Get", func() error { _, err := st.Rules().Get(ctx, "z"); return err }},
		{"Rules.List", func() error { _, err := st.Rules().List(ctx); return err }},
		{"Rules.ListByInput", func() error { _, err := st.Rules().ListByInput(ctx, "events"); return err }},

		{"Subscribers.Create", func() error { return st.Subscribers().Create(ctx, &subscriber.Subscriber{ID: "z"}) }},
		{"Subscribers.Update", func() error { return st.Subscribers().Update(ctx, &subscriber.Subscriber{ID: "z"}) }},
		{"Subscribers.Delete", func() error { return st.Subscribers().Delete(ctx, "z") }},
		{"Subscribers.Get", func() error { _, err := st.Subscribers().Get(ctx, "z"); return err }},
		{"Subscribers.List", func() error { _, err := st.Subscribers().List(ctx); return err }},

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

		{"Incidents.Open", func() error {
			return st.Incidents().Open(ctx, &subscriber.Incident{ID: "z", TriggeredAt: time.Now()})
		}},
		{"Incidents.Resolve", func() error { return st.Incidents().Resolve(ctx, "z", time.Now().UnixMilli()) }},
		{"Incidents.Get", func() error { _, err := st.Incidents().Get(ctx, "z"); return err }},
		{"Incidents.List", func() error { _, err := st.Incidents().List(ctx, 0); return err }},
		{"Incidents.ListForRule", func() error { _, err := st.Incidents().ListForRule(ctx, "r", 0); return err }},

		{"Notifications.Record", func() error {
			return st.Notifications().Record(ctx, &subscriber.Notification{ID: "z", SentAt: time.Now()})
		}},
		{"Notifications.ListForIncident", func() error {
			_, err := st.Notifications().ListForIncident(ctx, "z")
			return err
		}},
		{"Notifications.List", func() error { _, err := st.Notifications().List(ctx, 0); return err }},

		{"LiveStates.Upsert", func() error { return st.LiveStates().Upsert(ctx, &rule.LiveState{RuleID: "z"}) }},
		{"LiveStates.Get", func() error { _, err := st.LiveStates().Get(ctx, "z"); return err }},
		{"LiveStates.List", func() error { _, err := st.LiveStates().List(ctx); return err }},

		{"IncidentSubStates.Upsert", func() error {
			return st.IncidentSubStates().Upsert(ctx, &subscriber.IncidentSubState{IncidentID: "z", SubscriptionID: "y"})
		}},
		{"IncidentSubStates.Get", func() error { _, err := st.IncidentSubStates().Get(ctx, "z", "y"); return err }},
		{"IncidentSubStates.ListForIncident", func() error {
			_, err := st.IncidentSubStates().ListForIncident(ctx, "z")
			return err
		}},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			if err := c.fn(); err == nil {
				t.Fatalf("want error from %s on closed DB", c.name)
			}
		})
	}
}

// Open + Close round-trips without error.
func TestClose_NoError(t *testing.T) {
	st, err := sqlite.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
