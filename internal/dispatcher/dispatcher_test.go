package dispatcher_test

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/channel"
	"github.com/ryan-evans-git/signalwatch/internal/dispatcher"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
	"github.com/ryan-evans-git/signalwatch/internal/store/sqlite"
	"github.com/ryan-evans-git/signalwatch/internal/subscriber"
)

// recordingChannel captures sent notifications so tests can assert on them.
type recordingChannel struct {
	name string
	got  []channel.Notification
}

func (r *recordingChannel) Name() string { return r.name }
func (r *recordingChannel) Send(_ context.Context, n channel.Notification) error {
	r.got = append(r.got, n)
	return nil
}

func newTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	st, err := sqlite.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// fixedClock returns a controllable now func.
type fixedClock struct{ t time.Time }

func (c *fixedClock) now() time.Time { return c.t }
func (c *fixedClock) advance(d time.Duration) {
	c.t = c.t.Add(d)
}

func newDispatcher(t *testing.T, channels map[string]channel.Channel, clock *fixedClock) (*dispatcher.Dispatcher, *sqlite.Store) {
	t.Helper()
	st := newTestStore(t)
	var seq atomic.Uint64
	id := func() string {
		seq.Add(1)
		return fmt.Sprintf("id-%d", seq.Load())
	}
	d := dispatcher.New(dispatcher.Options{
		Store:    st,
		Channels: channels,
		Logger:   slog.Default(),
		Now:      clock.now,
		IDFunc:   id,
	})
	return d, st
}

// seedRule, seedSubscriber, seedSubscription create the row(s) the dispatcher
// expects to find in the store.
func seedRule(t *testing.T, st *sqlite.Store) *rule.Rule {
	t.Helper()
	r := &rule.Rule{
		ID:        "rule-1",
		Name:      "Test Rule",
		Enabled:   true,
		Severity:  rule.SeverityWarning,
		InputRef:  "events",
		Condition: rule.PatternMatch{Field: "level", Kind: rule.MatchContains, Pattern: "ERROR"},
	}
	if err := st.Rules().Create(context.Background(), r); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	return r
}

func seedSubscriber(t *testing.T, st *sqlite.Store, channelName, address string) *subscriber.Subscriber {
	t.Helper()
	s := &subscriber.Subscriber{
		ID:   "sub-1",
		Name: "On-call",
		Channels: []subscriber.ChannelBinding{
			{Channel: channelName, Address: address},
		},
	}
	if err := st.Subscribers().Create(context.Background(), s); err != nil {
		t.Fatalf("seed subscriber: %v", err)
	}
	return s
}

func seedSubscription(t *testing.T, st *sqlite.Store, ruleID, subscriberID string, dwell, repeat time.Duration, notifyOnResolve bool) *subscriber.Subscription {
	t.Helper()
	s := &subscriber.Subscription{
		ID:              "subscr-1",
		SubscriberID:    subscriberID,
		RuleID:          ruleID,
		Dwell:           dwell,
		RepeatInterval:  repeat,
		NotifyOnResolve: notifyOnResolve,
	}
	if err := st.Subscriptions().Create(context.Background(), s); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	return s
}

// TestDedup_SingleNotificationPerCycle: a rule that triggers many times in
// rapid succession should produce exactly one notification per cycle.
func TestDedup_SingleNotificationPerCycle(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	rec := &recordingChannel{name: "test"}
	d, st := newDispatcher(t, map[string]channel.Channel{"test": rec}, clock)

	r := seedRule(t, st)
	sub := seedSubscriber(t, st, "test", "ops@example.com")
	seedSubscription(t, st, r.ID, sub.ID, 0, 0, true)

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		clock.advance(100 * time.Millisecond)
		if err := d.Tick(ctx, r, true, fmt.Sprintf("hit %d", i)); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}
	if got := len(rec.got); got != 1 {
		t.Fatalf("expected 1 notification (dedup), got %d", got)
	}
	if rec.got[0].Kind != string(subscriber.KindFiring) {
		t.Errorf("expected firing kind, got %s", rec.got[0].Kind)
	}
}

// TestDedup_NewIncidentRefires: after a rule resolves and re-fires, a fresh
// notification must be sent.
func TestDedup_NewIncidentRefires(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	rec := &recordingChannel{name: "test"}
	d, st := newDispatcher(t, map[string]channel.Channel{"test": rec}, clock)

	r := seedRule(t, st)
	sub := seedSubscriber(t, st, "test", "ops@example.com")
	seedSubscription(t, st, r.ID, sub.ID, 0, 0, true)

	ctx := context.Background()
	if err := d.Tick(ctx, r, true, "fire"); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Second)
	if err := d.Tick(ctx, r, false, "ok"); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Second)
	if err := d.Tick(ctx, r, true, "fire-again"); err != nil {
		t.Fatal(err)
	}
	// Expect: fire + resolved + fire = 3 sends.
	if len(rec.got) != 3 {
		t.Fatalf("expected 3 notifications, got %d", len(rec.got))
	}
	wantKinds := []string{string(subscriber.KindFiring), string(subscriber.KindResolved), string(subscriber.KindFiring)}
	for i, want := range wantKinds {
		if rec.got[i].Kind != want {
			t.Errorf("notification %d: want kind %s, got %s", i, want, rec.got[i].Kind)
		}
	}
}

// TestDwell_DelaysNotification: with dwell=2m, no notification fires until
// the rule has been continuously firing for 2m.
func TestDwell_DelaysNotification(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	rec := &recordingChannel{name: "test"}
	d, st := newDispatcher(t, map[string]channel.Channel{"test": rec}, clock)

	r := seedRule(t, st)
	sub := seedSubscriber(t, st, "test", "ops@example.com")
	seedSubscription(t, st, r.ID, sub.ID, 2*time.Minute, 0, true)

	ctx := context.Background()
	// First trigger — starts the firing cycle but dwell hasn't elapsed.
	if err := d.Tick(ctx, r, true, "fire-1"); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 0 {
		t.Fatalf("dwell not honored on first tick: %d sends", len(rec.got))
	}
	// 30s later, still firing — still under dwell.
	clock.advance(30 * time.Second)
	if err := d.Tick(ctx, r, true, "fire-2"); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 0 {
		t.Fatalf("dwell breached at 30s: %d sends", len(rec.got))
	}
	// 2 minutes total — dwell elapsed.
	clock.advance(91 * time.Second) // total elapsed = 30 + 91 = 121s, just past 2m
	if err := d.Tick(ctx, r, true, "fire-3"); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 1 {
		t.Fatalf("expected 1 send after dwell, got %d", len(rec.got))
	}
}

// TestDwell_ResolveBeforeDwell: a rule that fires and resolves within the
// dwell window must NOT notify (neither firing nor resolved). This is the
// "transient blip not worth waking anyone for" case in the plan.
func TestDwell_ResolveBeforeDwell(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	rec := &recordingChannel{name: "test"}
	d, st := newDispatcher(t, map[string]channel.Channel{"test": rec}, clock)

	r := seedRule(t, st)
	sub := seedSubscriber(t, st, "test", "ops@example.com")
	seedSubscription(t, st, r.ID, sub.ID, 5*time.Minute, 0, true)

	ctx := context.Background()
	if err := d.Tick(ctx, r, true, "fire"); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	if err := d.Tick(ctx, r, false, "ok"); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 0 {
		t.Fatalf("expected silence on transient blip, got %d sends", len(rec.got))
	}
}

// TestRepeat_RenotifiesOnInterval: with repeat=1m, a rule that stays firing
// for 3m should produce notifications at 0m, 1m, 2m, 3m.
func TestRepeat_RenotifiesOnInterval(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	rec := &recordingChannel{name: "test"}
	d, st := newDispatcher(t, map[string]channel.Channel{"test": rec}, clock)

	r := seedRule(t, st)
	sub := seedSubscriber(t, st, "test", "ops@example.com")
	seedSubscription(t, st, r.ID, sub.ID, 0, time.Minute, true)

	ctx := context.Background()
	if err := d.Tick(ctx, r, true, "fire"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		clock.advance(time.Minute)
		if err := d.Tick(ctx, r, true, "still-fire"); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.got) != 4 {
		t.Fatalf("expected 4 sends (1 initial + 3 repeats), got %d", len(rec.got))
	}
	if rec.got[0].Kind != string(subscriber.KindFiring) {
		t.Errorf("first kind want firing, got %s", rec.got[0].Kind)
	}
	for i := 1; i < 4; i++ {
		if rec.got[i].Kind != string(subscriber.KindRepeat) {
			t.Errorf("send %d kind want repeat, got %s", i, rec.got[i].Kind)
		}
	}
}

// TestNotifyOnResolve_OnlyAfterFiringNotification: when dwell is long and
// the rule resolves before any firing notification went out, the resolution
// notification must also be suppressed.
func TestNotifyOnResolve_OnlyAfterFiringNotification(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	rec := &recordingChannel{name: "test"}
	d, st := newDispatcher(t, map[string]channel.Channel{"test": rec}, clock)

	r := seedRule(t, st)
	sub := seedSubscriber(t, st, "test", "ops@example.com")
	seedSubscription(t, st, r.ID, sub.ID, 5*time.Minute, 0, true)

	ctx := context.Background()
	if err := d.Tick(ctx, r, true, "fire"); err != nil {
		t.Fatal(err)
	}
	clock.advance(30 * time.Second)
	if err := d.Tick(ctx, r, false, "ok"); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 0 {
		t.Fatalf("expected no resolve notification when firing was suppressed, got %d", len(rec.got))
	}
}

// TestNotifyOnResolve_FiresAfterFiringNotification: full happy path —
// fire → notify → resolve → notify-resolved.
func TestNotifyOnResolve_FiresAfterFiringNotification(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	rec := &recordingChannel{name: "test"}
	d, st := newDispatcher(t, map[string]channel.Channel{"test": rec}, clock)

	r := seedRule(t, st)
	sub := seedSubscriber(t, st, "test", "ops@example.com")
	seedSubscription(t, st, r.ID, sub.ID, 0, 0, true)

	ctx := context.Background()
	if err := d.Tick(ctx, r, true, "fire"); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	if err := d.Tick(ctx, r, false, "ok"); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 2 {
		t.Fatalf("expected 2 sends, got %d", len(rec.got))
	}
	if rec.got[1].Kind != string(subscriber.KindResolved) {
		t.Errorf("last kind want resolved, got %s", rec.got[1].Kind)
	}
}

// TestNotifyOnResolve_Disabled: when notify_on_resolve=false, no resolution
// notification fires even after a successful firing notification.
func TestNotifyOnResolve_Disabled(t *testing.T) {
	clock := &fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	rec := &recordingChannel{name: "test"}
	d, st := newDispatcher(t, map[string]channel.Channel{"test": rec}, clock)

	r := seedRule(t, st)
	sub := seedSubscriber(t, st, "test", "ops@example.com")
	seedSubscription(t, st, r.ID, sub.ID, 0, 0, false)

	ctx := context.Background()
	if err := d.Tick(context.Background(), r, true, "fire"); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	if err := d.Tick(ctx, r, false, "ok"); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 1 {
		t.Fatalf("expected 1 send (firing only), got %d", len(rec.got))
	}
}
