package dispatcher_test

// Extra tests covering deliver()'s branches that the original
// dispatcher_test.go didn't reach: missing subscriber, channel filter,
// channel-not-configured, channel.Send error, notification audit
// recording.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/channel"
	"github.com/ryan-evans-git/signalwatch/internal/dispatcher"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
	"github.com/ryan-evans-git/signalwatch/internal/store"
	"github.com/ryan-evans-git/signalwatch/internal/store/sqlite"
	"github.com/ryan-evans-git/signalwatch/internal/subscriber"
)

// localStore mirrors the test setup in dispatcher_test.go so this file is
// drop-in additive without modifying the original. Distinct test names so
// no Go-level collision.

func freshStore(t *testing.T) *sqlite.Store {
	t.Helper()
	st, err := sqlite.Open("file::memory:?cache=shared&mode=memory&test_id=" + t.Name())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func freshDispatcher(t *testing.T, st *sqlite.Store, channels map[string]channel.Channel) *dispatcher.Dispatcher {
	t.Helper()
	var seq atomic.Uint64
	return dispatcher.New(dispatcher.Options{
		Store:    st,
		Channels: channels,
		Logger:   slog.Default(),
		Now:      time.Now,
		IDFunc: func() string {
			seq.Add(1)
			return fmt.Sprintf("nid-%d", seq.Load())
		},
	})
}

// New defaults: when Options omits Logger/Now/IDFunc the Dispatcher must
// fill them in. Covering this path lifts dispatcher.New from 85.7% to
// 100%.
func TestNew_DefaultsAllOptionalFields(t *testing.T) {
	st := freshStore(t)
	d := dispatcher.New(dispatcher.Options{Store: st})
	if d == nil {
		t.Fatal("New returned nil")
	}
}

// errorChannel returns an error from Send to drive the audit.Status="error"
// branch of deliver().
type errorChannel struct {
	name string
	err  error
}

func (e *errorChannel) Name() string { return e.name }
func (e *errorChannel) Send(_ context.Context, _ channel.Notification) error {
	return e.err
}

// trackingChannel records each Send for assertion.
type trackingChannel struct {
	name  string
	calls atomic.Int64
}

func (c *trackingChannel) Name() string { return c.name }
func (c *trackingChannel) Send(_ context.Context, _ channel.Notification) error {
	c.calls.Add(1)
	return nil
}

func seedSubscriberMulti(t *testing.T, st *sqlite.Store, channels ...string) *subscriber.Subscriber {
	t.Helper()
	bindings := make([]subscriber.ChannelBinding, len(channels))
	for i, c := range channels {
		bindings[i] = subscriber.ChannelBinding{Channel: c, Address: "x@example.com"}
	}
	s := &subscriber.Subscriber{
		ID:       "sub-X",
		Name:     "Multi",
		Channels: bindings,
	}
	if err := st.Subscribers().Create(context.Background(), s); err != nil {
		t.Fatalf("seed subscriber: %v", err)
	}
	return s
}

func seedRuleAndSubscription(t *testing.T, st *sqlite.Store, filter []string) (*rule.Rule, *subscriber.Subscription) {
	t.Helper()
	r := &rule.Rule{
		ID:        "rule-Y",
		Name:      "Y Rule",
		Enabled:   true,
		Severity:  rule.SeverityWarning,
		InputRef:  "events",
		Condition: rule.PatternMatch{Field: "level", Kind: rule.MatchContains, Pattern: "ERROR"},
	}
	if err := st.Rules().Create(context.Background(), r); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	sub := &subscriber.Subscription{
		ID:            "sub-Y",
		SubscriberID:  "sub-X",
		RuleID:        r.ID,
		ChannelFilter: filter,
	}
	if err := st.Subscriptions().Create(context.Background(), sub); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	return r, sub
}

// Tick with a subscription pointing at a missing subscriber id logs the
// error and Tick still returns nil. The migration enforces a FK so we
// must temporarily disable foreign_keys to insert an orphan row that
// simulates a race where the subscriber is deleted between subscription
// load and delivery.
func TestTick_DeliverHandlesMissingSubscriber(t *testing.T) {
	st := freshStore(t)
	disp := freshDispatcher(t, st, map[string]channel.Channel{})

	r := &rule.Rule{
		ID: "ghost-rule", Name: "Ghost", Enabled: true, Severity: rule.SeverityInfo,
		InputRef: "events", Condition: rule.PatternMatch{Field: "level", Kind: rule.MatchContains, Pattern: "X"},
	}
	if err := st.Rules().Create(context.Background(), r); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	// Bypass the FK constraint to insert an orphan subscription row.
	ctx := context.Background()
	if _, err := st.DB().ExecContext(ctx, `PRAGMA foreign_keys = OFF;`); err != nil {
		t.Fatalf("PRAGMA off: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO subscriptions
        (id, subscriber_id, rule_id, label_selector, dwell_ns, repeat_interval_ns, notify_on_resolve, channel_filter, created_at, updated_at)
        VALUES ('subscr-ghost', 'no-such-subscriber', 'ghost-rule', '{}', 0, 0, 0, '[]', 0, 0)`); err != nil {
		t.Fatalf("orphan insert: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `PRAGMA foreign_keys = ON;`); err != nil {
		t.Fatalf("PRAGMA on: %v", err)
	}

	if err := disp.Tick(ctx, r, true, "ERROR"); err != nil {
		t.Fatalf("Tick: want nil (warnings logged but Tick succeeds), got %v", err)
	}
}

// A subscriber with multiple channel bindings + a ChannelFilter that
// selects only one of them should: deliver only via the allowed channel.
// Covers channelFilterSet's non-empty branch and deliver's continue path.
func TestTick_ChannelFilterRestrictsBindings(t *testing.T) {
	st := freshStore(t)
	chA := &trackingChannel{name: "A"}
	chB := &trackingChannel{name: "B"}
	disp := freshDispatcher(t, st, map[string]channel.Channel{"A": chA, "B": chB})

	seedSubscriberMulti(t, st, "A", "B")
	r, _ := seedRuleAndSubscription(t, st, []string{"A"}) // only A allowed

	if err := disp.Tick(context.Background(), r, true, "ERROR"); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if chA.calls.Load() != 1 {
		t.Errorf("A should have been called once, got %d", chA.calls.Load())
	}
	if chB.calls.Load() != 0 {
		t.Errorf("B should be filtered out, got %d calls", chB.calls.Load())
	}
}

// A subscriber with a channel binding referencing a channel that is not
// configured on the dispatcher should be skipped with a logged warning;
// other bindings still proceed. Covers the channel-not-configured branch.
func TestTick_UnknownChannelBindingIsSkipped(t *testing.T) {
	st := freshStore(t)
	chA := &trackingChannel{name: "A"}
	// dispatcher only knows about A; subscriber binds to A + "Z" (unknown).
	disp := freshDispatcher(t, st, map[string]channel.Channel{"A": chA})

	seedSubscriberMulti(t, st, "A", "Z")
	r, _ := seedRuleAndSubscription(t, st, nil)

	if err := disp.Tick(context.Background(), r, true, "ERROR"); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if chA.calls.Load() != 1 {
		t.Errorf("A should fire even when sibling channel is unknown, got %d", chA.calls.Load())
	}
}

// Closing the store mid-test triggers the err returns from
// LiveStates().Get / Incidents().Open / Subscriptions().ListForRule
// inside Tick(). Tick must surface the error to the caller (it returns
// from the load/upsert paths) so we just assert it errors.
func TestTick_ErrorOnClosedStore(t *testing.T) {
	st := freshStore(t)
	disp := freshDispatcher(t, st, map[string]channel.Channel{})
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	r := &rule.Rule{ID: "r", Name: "r", Enabled: true, InputRef: "events",
		Condition: rule.PatternMatch{Field: "level", Kind: rule.MatchContains, Pattern: "X"}}
	if err := disp.Tick(context.Background(), r, true, "v"); err == nil {
		t.Fatalf("Tick on closed store should error")
	}
}

// Open an incident in advance, then close the store, then Tick with
// triggered=false. The Closed() branch hits Resolve which errors,
// surfacing the dispatcher's Resolve-error return.
func TestTick_ResolveErrorBranch(t *testing.T) {
	st := freshStore(t)
	disp := freshDispatcher(t, st, map[string]channel.Channel{})
	r := &rule.Rule{ID: "r", Name: "r", Enabled: true, InputRef: "events",
		Condition: rule.PatternMatch{Field: "level", Kind: rule.MatchContains, Pattern: "X"}}
	if err := st.Rules().Create(context.Background(), r); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	// Drive the rule into FIRING so the next non-triggered Tick will Close.
	if err := disp.Tick(context.Background(), r, true, "v"); err != nil {
		t.Fatalf("seed Tick FIRING: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Now Tick with not-triggered. The Closed() branch's Resolve call
	// fails against the closed DB.
	if err := disp.Tick(context.Background(), r, false, "ok"); err == nil {
		t.Fatalf("Tick FIRING->OK on closed store should error")
	}
}

// faultStore is a store.Store wrapper that delegates to an inner store
// but can be configured to fail specific repo operations. It lets us
// drive Tick's "if err != nil" branches one at a time even when the
// preceding store calls have to succeed.
type faultStore struct {
	inner store.Store

	failUpsertLiveState bool
	failOpenIncident    bool
	failListForRule     bool
	failIncidentSubGet  bool
	failResolveIncident bool
	failSubscribersGet  bool
}

func (f *faultStore) Rules() store.RuleRepo { return f.inner.Rules() }
func (f *faultStore) Subscribers() store.SubscriberRepo {
	return &failSubscriberRepo{inner: f.inner.Subscribers(), fail: f.failSubscribersGet}
}
func (f *faultStore) Subscriptions() store.SubscriptionRepo {
	return &failSubscriptionRepo{inner: f.inner.Subscriptions(), failListForRule: f.failListForRule}
}
func (f *faultStore) Incidents() store.IncidentRepo {
	return &failIncidentRepo{inner: f.inner.Incidents(), failOpen: f.failOpenIncident, failResolve: f.failResolveIncident}
}
func (f *faultStore) Notifications() store.NotificationRepo { return f.inner.Notifications() }
func (f *faultStore) LiveStates() store.LiveStateRepo {
	return &failLiveStateRepo{inner: f.inner.LiveStates(), failUpsert: f.failUpsertLiveState}
}
func (f *faultStore) IncidentSubStates() store.IncidentSubStateRepo {
	return &failIncidentSubStateRepo{inner: f.inner.IncidentSubStates(), failGet: f.failIncidentSubGet}
}
func (f *faultStore) Migrate(ctx context.Context) error { return f.inner.Migrate(ctx) }
func (f *faultStore) Close() error                      { return f.inner.Close() }

type failLiveStateRepo struct {
	inner      store.LiveStateRepo
	failUpsert bool
}

func (r *failLiveStateRepo) Get(ctx context.Context, id string) (*rule.LiveState, error) {
	return r.inner.Get(ctx, id)
}
func (r *failLiveStateRepo) Upsert(ctx context.Context, s *rule.LiveState) error {
	if r.failUpsert {
		return errors.New("synthetic upsert failure")
	}
	return r.inner.Upsert(ctx, s)
}
func (r *failLiveStateRepo) List(ctx context.Context) ([]*rule.LiveState, error) {
	return r.inner.List(ctx)
}

type failIncidentRepo struct {
	inner       store.IncidentRepo
	failOpen    bool
	failResolve bool
}

func (r *failIncidentRepo) Open(ctx context.Context, inc *subscriber.Incident) error {
	if r.failOpen {
		return errors.New("synthetic open failure")
	}
	return r.inner.Open(ctx, inc)
}
func (r *failIncidentRepo) Resolve(ctx context.Context, id string, ts int64) error {
	if r.failResolve {
		return errors.New("synthetic resolve failure")
	}
	return r.inner.Resolve(ctx, id, ts)
}
func (r *failIncidentRepo) Get(ctx context.Context, id string) (*subscriber.Incident, error) {
	return r.inner.Get(ctx, id)
}
func (r *failIncidentRepo) List(ctx context.Context, limit int) ([]*subscriber.Incident, error) {
	return r.inner.List(ctx, limit)
}
func (r *failIncidentRepo) ListForRule(ctx context.Context, ruleID string, limit int) ([]*subscriber.Incident, error) {
	return r.inner.ListForRule(ctx, ruleID, limit)
}
func (r *failIncidentRepo) ListResolvedBefore(ctx context.Context, t int64) ([]*subscriber.Incident, error) {
	return r.inner.ListResolvedBefore(ctx, t)
}
func (r *failIncidentRepo) DeleteResolvedBefore(ctx context.Context, t int64) (int, error) {
	return r.inner.DeleteResolvedBefore(ctx, t)
}

type failSubscriptionRepo struct {
	inner           store.SubscriptionRepo
	failListForRule bool
}

func (r *failSubscriptionRepo) Create(ctx context.Context, s *subscriber.Subscription) error {
	return r.inner.Create(ctx, s)
}
func (r *failSubscriptionRepo) Update(ctx context.Context, s *subscriber.Subscription) error {
	return r.inner.Update(ctx, s)
}
func (r *failSubscriptionRepo) Delete(ctx context.Context, id string) error {
	return r.inner.Delete(ctx, id)
}
func (r *failSubscriptionRepo) Get(ctx context.Context, id string) (*subscriber.Subscription, error) {
	return r.inner.Get(ctx, id)
}
func (r *failSubscriptionRepo) List(ctx context.Context) ([]*subscriber.Subscription, error) {
	return r.inner.List(ctx)
}
func (r *failSubscriptionRepo) ListForRule(ctx context.Context, ruleID string, labels map[string]string) ([]*subscriber.Subscription, error) {
	if r.failListForRule {
		return nil, errors.New("synthetic listforrule failure")
	}
	return r.inner.ListForRule(ctx, ruleID, labels)
}

type failSubscriberRepo struct {
	inner store.SubscriberRepo
	fail  bool
}

func (r *failSubscriberRepo) Create(ctx context.Context, s *subscriber.Subscriber) error {
	return r.inner.Create(ctx, s)
}
func (r *failSubscriberRepo) Update(ctx context.Context, s *subscriber.Subscriber) error {
	return r.inner.Update(ctx, s)
}
func (r *failSubscriberRepo) Delete(ctx context.Context, id string) error {
	return r.inner.Delete(ctx, id)
}
func (r *failSubscriberRepo) Get(ctx context.Context, id string) (*subscriber.Subscriber, error) {
	if r.fail {
		return nil, errors.New("synthetic subscriber-get failure")
	}
	return r.inner.Get(ctx, id)
}
func (r *failSubscriberRepo) List(ctx context.Context) ([]*subscriber.Subscriber, error) {
	return r.inner.List(ctx)
}

type failIncidentSubStateRepo struct {
	inner   store.IncidentSubStateRepo
	failGet bool
}

func (r *failIncidentSubStateRepo) Get(ctx context.Context, incID, subID string) (*subscriber.IncidentSubState, error) {
	if r.failGet {
		return nil, errors.New("synthetic incidentSubState-get failure")
	}
	return r.inner.Get(ctx, incID, subID)
}
func (r *failIncidentSubStateRepo) Upsert(ctx context.Context, s *subscriber.IncidentSubState) error {
	return r.inner.Upsert(ctx, s)
}
func (r *failIncidentSubStateRepo) ListForIncident(ctx context.Context, incID string) ([]*subscriber.IncidentSubState, error) {
	return r.inner.ListForIncident(ctx, incID)
}

// dispatcherWithFaultStore mirrors freshDispatcher but using the wrapper.
func dispatcherWithFaultStore(t *testing.T, fs *faultStore, channels map[string]channel.Channel) *dispatcher.Dispatcher {
	t.Helper()
	var seq atomic.Uint64
	return dispatcher.New(dispatcher.Options{
		Store:    fs,
		Channels: channels,
		Logger:   slog.Default(),
		Now:      time.Now,
		IDFunc: func() string {
			seq.Add(1)
			return fmt.Sprintf("fid-%d", seq.Load())
		},
	})
}

// Fault-injection matrix: each subtest drives ONE inner-store call to
// fail, covering each "if err != nil { return err }" branch inside Tick
// and evaluateSubscription that closed-store testing couldn't reach.
func TestTick_StoreFailureBranches(t *testing.T) {
	mkRule := func() *rule.Rule {
		return &rule.Rule{
			ID: "r-fault", Name: "Fault", Enabled: true, Severity: rule.SeverityWarning,
			InputRef:  "events",
			Condition: rule.PatternMatch{Field: "level", Kind: rule.MatchContains, Pattern: "ERROR"},
		}
	}

	t.Run("LiveStates.Upsert", func(t *testing.T) {
		st := freshStore(t)
		fs := &faultStore{inner: st, failUpsertLiveState: true}
		d := dispatcherWithFaultStore(t, fs, map[string]channel.Channel{})
		_ = st.Rules().Create(context.Background(), mkRule())
		if err := d.Tick(context.Background(), mkRule(), true, "v"); err == nil {
			t.Fatalf("want Upsert error")
		}
	})

	t.Run("Incidents.Open", func(t *testing.T) {
		st := freshStore(t)
		fs := &faultStore{inner: st, failOpenIncident: true}
		d := dispatcherWithFaultStore(t, fs, map[string]channel.Channel{})
		_ = st.Rules().Create(context.Background(), mkRule())
		if err := d.Tick(context.Background(), mkRule(), true, "v"); err == nil {
			t.Fatalf("want Open error")
		}
	})

	t.Run("Subscriptions.ListForRule", func(t *testing.T) {
		st := freshStore(t)
		fs := &faultStore{inner: st, failListForRule: true}
		d := dispatcherWithFaultStore(t, fs, map[string]channel.Channel{})
		_ = st.Rules().Create(context.Background(), mkRule())
		if err := d.Tick(context.Background(), mkRule(), true, "v"); err == nil {
			t.Fatalf("want ListForRule error")
		}
	})

	t.Run("Incidents.Resolve", func(t *testing.T) {
		st := freshStore(t)
		fs := &faultStore{inner: st, failResolveIncident: true}
		d := dispatcherWithFaultStore(t, fs, map[string]channel.Channel{})
		r := mkRule()
		_ = st.Rules().Create(context.Background(), r)
		// Get into FIRING first with a non-faulting Tick.
		fs.failResolveIncident = false
		if err := d.Tick(context.Background(), r, true, "v"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		fs.failResolveIncident = true
		if err := d.Tick(context.Background(), r, false, "ok"); err == nil {
			t.Fatalf("want Resolve error")
		}
	})
}

// channel.Send returning an error must still produce an audit record with
// Status="error" (covers the failure branch of deliver and the warning
// logger emit).
func TestTick_ChannelSendErrorIsAudited(t *testing.T) {
	st := freshStore(t)
	failure := errors.New("transient SMTP outage")
	disp := freshDispatcher(t, st, map[string]channel.Channel{
		"A": &errorChannel{name: "A", err: failure},
	})

	seedSubscriberMulti(t, st, "A")
	r, _ := seedRuleAndSubscription(t, st, nil)

	if err := disp.Tick(context.Background(), r, true, "ERROR"); err != nil {
		t.Fatalf("Tick: want nil (Tick continues past send error), got %v", err)
	}

	notes, err := st.Notifications().List(context.Background(), 10)
	if err != nil {
		t.Fatalf("List notifications: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("notifications: want 1 audit record, got %d", len(notes))
	}
	if notes[0].Status != "error" || notes[0].Error == "" {
		t.Errorf("audit record: want status=error with error message, got %+v", notes[0])
	}
}
