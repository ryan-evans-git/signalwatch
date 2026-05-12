package eval_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ryan-evans-git/signalwatch/internal/channel"
	"github.com/ryan-evans-git/signalwatch/internal/dispatcher"
	"github.com/ryan-evans-git/signalwatch/internal/eval"
	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/input/sqlquery"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
	"github.com/ryan-evans-git/signalwatch/internal/store/sqlite"
	"github.com/ryan-evans-git/signalwatch/internal/subscriber"
)

// ---------------- shared fixtures ----------------

// recordingChannel captures every notification it gets sent so a test can
// assert delivery happened (or didn't).
type recordingChannel struct {
	name string

	mu  sync.Mutex
	got []channel.Notification
}

func (r *recordingChannel) Name() string { return r.name }
func (r *recordingChannel) Send(_ context.Context, n channel.Notification) error {
	r.mu.Lock()
	r.got = append(r.got, n)
	r.mu.Unlock()
	return nil
}

func (r *recordingChannel) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func newStore(t *testing.T) *sqlite.Store {
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

func newDispatcher(t *testing.T, st *sqlite.Store, channels map[string]channel.Channel) *dispatcher.Dispatcher {
	t.Helper()
	var seq atomic.Uint64
	return dispatcher.New(dispatcher.Options{
		Store:    st,
		Channels: channels,
		Logger:   slog.Default(),
		Now:      func() time.Time { return time.Now() },
		IDFunc: func() string {
			seq.Add(1)
			return fmt.Sprintf("id-%d", seq.Load())
		},
	})
}

func mustCompile(t *testing.T, c rule.Condition) rule.Compiled {
	t.Helper()
	compiled, err := c.Compile(nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return compiled
}

// ---------------- Cache ----------------

func TestCache_SetGetDelete(t *testing.T) {
	c := eval.NewCache()
	r := &rule.Rule{ID: "r1", Name: "x", Enabled: true, InputRef: "events",
		Condition: rule.PatternMatch{Field: "msg", Kind: rule.MatchContains, Pattern: "x"}}
	cr := &eval.CompiledRule{Rule: r, Compiled: mustCompile(t, r.Condition)}

	if _, ok := c.Get("r1"); ok {
		t.Fatalf("expected empty cache")
	}
	c.Set(cr)
	got, ok := c.Get("r1")
	if !ok || got != cr {
		t.Fatalf("Get after Set: want hit")
	}
	c.Delete("r1")
	if _, ok := c.Get("r1"); ok {
		t.Fatalf("Get after Delete: want miss")
	}
}

func TestCache_ByInput_FiltersDisabledAndUncompiled(t *testing.T) {
	c := eval.NewCache()
	enabled := &rule.Rule{ID: "1", Enabled: true, InputRef: "events",
		Condition: rule.PatternMatch{Field: "msg", Kind: rule.MatchContains, Pattern: "x"}}
	disabled := &rule.Rule{ID: "2", Enabled: false, InputRef: "events", Condition: enabled.Condition}
	wrongInput := &rule.Rule{ID: "3", Enabled: true, InputRef: "other", Condition: enabled.Condition}
	uncompiled := &rule.Rule{ID: "4", Enabled: true, InputRef: "events"}

	c.Set(&eval.CompiledRule{Rule: enabled, Compiled: mustCompile(t, enabled.Condition)})
	c.Set(&eval.CompiledRule{Rule: disabled, Compiled: mustCompile(t, disabled.Condition)})
	c.Set(&eval.CompiledRule{Rule: wrongInput, Compiled: mustCompile(t, wrongInput.Condition)})
	c.Set(&eval.CompiledRule{Rule: uncompiled, Compiled: nil})

	got := c.ByInput("events")
	if len(got) != 1 || got[0].Rule.ID != "1" {
		ids := make([]string, len(got))
		for i, cr := range got {
			ids[i] = cr.Rule.ID
		}
		t.Fatalf("ByInput: want [1], got %v", ids)
	}
}

func TestCache_Scheduled_FiltersByModeAndEnabled(t *testing.T) {
	c := eval.NewCache()
	pushRule := &rule.Rule{ID: "push", Enabled: true, InputRef: "ev",
		Condition: rule.PatternMatch{Field: "msg", Kind: rule.MatchContains, Pattern: "x"}}
	schedRule := &rule.Rule{ID: "sched", Enabled: true, InputRef: "ev",
		Condition: rule.WindowAggregate{Field: "v", Agg: rule.AggAvg, Window: time.Minute, Op: rule.OpLT, Value: 5}}
	schedDisabled := &rule.Rule{ID: "sched-off", Enabled: false, InputRef: "ev",
		Condition: schedRule.Condition}

	c.Set(&eval.CompiledRule{Rule: pushRule, Compiled: mustCompile(t, pushRule.Condition)})
	c.Set(&eval.CompiledRule{Rule: schedRule, Compiled: mustCompile(t, schedRule.Condition)})
	c.Set(&eval.CompiledRule{Rule: schedDisabled, Compiled: mustCompile(t, schedDisabled.Condition)})

	got := c.Scheduled()
	if len(got) != 1 || got[0].Rule.ID != "sched" {
		ids := make([]string, len(got))
		for i, cr := range got {
			ids[i] = cr.Rule.ID
		}
		t.Fatalf("Scheduled: want [sched], got %v", ids)
	}
}

// ---------------- PushEvaluator ----------------

func TestNewPushEvaluator_DefaultsLogger(t *testing.T) {
	p := eval.NewPushEvaluator(eval.NewCache(), nil, eval.NewWindowBuffers(time.Hour, time.Now), nil)
	if p == nil {
		t.Fatal("nil evaluator")
	}
}

// seedPushRule creates a push-mode rule in both the store and the eval
// cache so the evaluator picks it up.
func seedPushRule(t *testing.T, st *sqlite.Store, c *eval.Cache) *rule.Rule {
	t.Helper()
	r := &rule.Rule{
		ID: "r-push", Name: "push-test", Enabled: true,
		Severity: rule.SeverityWarning, InputRef: "events",
		Condition: rule.PatternMatch{Field: "level", Kind: rule.MatchContains, Pattern: "ERROR"},
	}
	if err := st.Rules().Create(context.Background(), r); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	c.Set(&eval.CompiledRule{Rule: r, Compiled: mustCompile(t, r.Condition)})
	return r
}

func TestPushEvaluator_DispatchesMatchingRule(t *testing.T) {
	st := newStore(t)
	ch := &recordingChannel{name: "ch"}
	disp := newDispatcher(t, st, map[string]channel.Channel{"ch": ch})

	c := eval.NewCache()
	helpers := eval.NewWindowBuffers(time.Hour, time.Now)
	p := eval.NewPushEvaluator(c, disp, helpers, slog.Default())

	r := seedPushRule(t, st, c)

	// Subscribe a recipient so a notification can actually be delivered.
	if err := st.Subscribers().Create(context.Background(), seedSubscriberRow(t)); err != nil {
		t.Fatalf("subscriber: %v", err)
	}
	if err := st.Subscriptions().Create(context.Background(), seedSubscriptionRow(t, r.ID)); err != nil {
		t.Fatalf("subscription: %v", err)
	}

	in := make(chan input.EvaluationRecord, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { p.Run(ctx, in); close(done) }()

	in <- input.EvaluationRecord{
		InputRef: "events",
		When:     time.Now(),
		Record:   rule.Record{"level": "ERROR: timeout"},
	}

	// Wait for the channel to record a send (with a generous timeout to
	// account for goroutine scheduling on slower CI runners).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ch.count() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ch.count() == 0 {
		t.Fatalf("no notification delivered")
	}

	cancel()
	<-done
}

func TestPushEvaluator_IgnoresNonMatchingRecord(t *testing.T) {
	st := newStore(t)
	ch := &recordingChannel{name: "ch"}
	disp := newDispatcher(t, st, map[string]channel.Channel{"ch": ch})
	c := eval.NewCache()
	helpers := eval.NewWindowBuffers(time.Hour, time.Now)
	p := eval.NewPushEvaluator(c, disp, helpers, slog.Default())

	r := seedPushRule(t, st, c)
	_ = st.Subscribers().Create(context.Background(), seedSubscriberRow(t))
	_ = st.Subscriptions().Create(context.Background(), seedSubscriptionRow(t, r.ID))

	in := make(chan input.EvaluationRecord, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx, in)

	in <- input.EvaluationRecord{
		InputRef: "events",
		When:     time.Now(),
		Record:   rule.Record{"level": "INFO"},
	}

	// Briefly let the evaluator process before cancelling — we expect no delivery.
	time.Sleep(150 * time.Millisecond)
	if ch.count() != 0 {
		t.Fatalf("non-matching event should not deliver, got %d", ch.count())
	}
}

func TestPushEvaluator_SkipsScheduledModeRules(t *testing.T) {
	st := newStore(t)
	ch := &recordingChannel{name: "ch"}
	disp := newDispatcher(t, st, map[string]channel.Channel{"ch": ch})
	c := eval.NewCache()
	helpers := eval.NewWindowBuffers(time.Hour, time.Now)
	p := eval.NewPushEvaluator(c, disp, helpers, slog.Default())

	// Cache a scheduled-mode rule on this input. Its evaluation should be
	// skipped by the push evaluator (it's the scheduled evaluator's job).
	sched := &rule.Rule{
		ID: "sched", Name: "win", Enabled: true,
		Severity: rule.SeverityWarning, InputRef: "events", Schedule: time.Second,
		Condition: rule.WindowAggregate{Field: "v", Agg: rule.AggAvg, Window: time.Minute, Op: rule.OpLT, Value: 5},
	}
	if err := st.Rules().Create(context.Background(), sched); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c.Set(&eval.CompiledRule{Rule: sched, Compiled: mustCompile(t, sched.Condition)})

	in := make(chan input.EvaluationRecord, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx, in)

	in <- input.EvaluationRecord{
		InputRef: "events",
		When:     time.Now(),
		Record:   rule.Record{"v": 1.0},
	}

	time.Sleep(100 * time.Millisecond)
	if ch.count() != 0 {
		t.Fatalf("push evaluator must not run scheduled-mode rules, got %d", ch.count())
	}
}

func TestPushEvaluator_ExitsOnClosedChannel(t *testing.T) {
	st := newStore(t)
	disp := newDispatcher(t, st, map[string]channel.Channel{})
	p := eval.NewPushEvaluator(eval.NewCache(), disp,
		eval.NewWindowBuffers(time.Hour, time.Now), slog.Default())

	in := make(chan input.EvaluationRecord)
	done := make(chan struct{})
	go func() { p.Run(context.Background(), in); close(done) }()

	close(in)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Run did not return after channel close")
	}
}

func TestPushEvaluator_ExitsOnContextDone(t *testing.T) {
	st := newStore(t)
	disp := newDispatcher(t, st, map[string]channel.Channel{})
	p := eval.NewPushEvaluator(eval.NewCache(), disp,
		eval.NewWindowBuffers(time.Hour, time.Now), slog.Default())

	in := make(chan input.EvaluationRecord) // never closed
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx, in); close(done) }()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}

// Eval errors (e.g. nil record path on a pattern_match rule) should be
// logged and the evaluator must keep running. We pump a record that will
// trigger an Eval error path inside the compiled condition, then a valid
// record, and assert the second one still flows.
func TestPushEvaluator_EvalErrorIsTolerated(t *testing.T) {
	st := newStore(t)
	ch := &recordingChannel{name: "ch"}
	disp := newDispatcher(t, st, map[string]channel.Channel{"ch": ch})

	c := eval.NewCache()
	helpers := eval.NewWindowBuffers(time.Hour, time.Now)
	p := eval.NewPushEvaluator(c, disp, helpers, slog.Default())

	r := seedPushRule(t, st, c)
	_ = st.Subscribers().Create(context.Background(), seedSubscriberRow(t))
	_ = st.Subscriptions().Create(context.Background(), seedSubscriptionRow(t, r.ID))

	// Swap the compiled rule for one that errors on first eval, succeeds
	// on second. Replace via Set.
	c.Set(&eval.CompiledRule{Rule: r, Compiled: &flakyCompiled{}})

	in := make(chan input.EvaluationRecord, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { p.Run(ctx, in); close(done) }()

	in <- input.EvaluationRecord{InputRef: "events", When: time.Now(), Record: rule.Record{"level": "ERROR"}}
	in <- input.EvaluationRecord{InputRef: "events", When: time.Now(), Record: rule.Record{"level": "ERROR"}}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ch.count() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ch.count() == 0 {
		t.Fatalf("evaluator should have recovered and delivered the second event")
	}
	cancel()
	<-done
}

// flakyCompiled errors once then triggers.
type flakyCompiled struct{ called atomic.Uint32 }

func (f *flakyCompiled) Eval(_ rule.EvalContext, _ rule.Record) (bool, string, error) {
	if f.called.Add(1) == 1 {
		return false, "", errors.New("synthetic eval error")
	}
	return true, "ok", nil
}

// ---------------- ScheduledEvaluator ----------------

func TestNewScheduledEvaluator_DefaultsLogger(t *testing.T) {
	s := eval.NewScheduledEvaluator(eval.NewCache(), nil,
		eval.NewWindowBuffers(time.Hour, time.Now), sqlquery.NewRegistry(), nil, nil)
	if s == nil {
		t.Fatal("nil evaluator")
	}
}

func TestScheduledEvaluator_ExitsOnContextDone(t *testing.T) {
	st := newStore(t)
	disp := newDispatcher(t, st, map[string]channel.Channel{})
	s := eval.NewScheduledEvaluator(eval.NewCache(), disp,
		eval.NewWindowBuffers(time.Hour, time.Now), sqlquery.NewRegistry(), st, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return")
	}
}

// Run the scheduled evaluator with a single scheduled rule whose schedule
// is short enough to elapse within the test's wait window. Confirms the
// ticker → cache.Scheduled → evalOne → dispatcher.Tick path executes.
func TestScheduledEvaluator_TicksScheduledRule(t *testing.T) {
	st := newStore(t)
	ch := &recordingChannel{name: "ch"}
	disp := newDispatcher(t, st, map[string]channel.Channel{"ch": ch})

	c := eval.NewCache()
	helpers := eval.NewWindowBuffers(time.Hour, time.Now)
	// Pre-seed window samples so the WindowAggregate triggers.
	helpers.Observe("events", rule.Record{"v": 1.0}, time.Now())
	s := eval.NewScheduledEvaluator(c, disp, helpers, sqlquery.NewRegistry(), st, slog.Default())

	r := &rule.Rule{
		ID: "sched", Name: "win-low", Enabled: true,
		Severity: rule.SeverityWarning, InputRef: "events",
		Schedule:  100 * time.Millisecond,
		Condition: rule.WindowAggregate{Field: "v", Agg: rule.AggAvg, Window: time.Minute, Op: rule.OpLT, Value: 5},
	}
	if err := st.Rules().Create(context.Background(), r); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c.Set(&eval.CompiledRule{Rule: r, Compiled: mustCompile(t, r.Condition)})
	_ = st.Subscribers().Create(context.Background(), seedSubscriberRow(t))
	_ = st.Subscriptions().Create(context.Background(), seedSubscriptionRow(t, r.ID))

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	deadline := time.Now().Add(3500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if ch.count() > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done

	if ch.count() == 0 {
		t.Fatalf("scheduled evaluator never ticked the rule")
	}
}

// SQL path: evalOne routes SQLReturnsRows through sqlquery.Registry.
func TestScheduledEvaluator_SQLPath_Triggers(t *testing.T) {
	st := newStore(t)
	ch := &recordingChannel{name: "ch"}
	disp := newDispatcher(t, st, map[string]channel.Channel{"ch": ch})

	// Set up an external sqlite DB the SQL rule queries.
	extDB, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("ext db: %v", err)
	}
	t.Cleanup(func() { _ = extDB.Close() })
	if _, err := extDB.Exec(`CREATE TABLE errors (id INTEGER); INSERT INTO errors VALUES (1), (2), (3);`); err != nil {
		t.Fatalf("seed ext: %v", err)
	}

	reg := sqlquery.NewRegistry()
	reg.Register("ops", extDB)

	c := eval.NewCache()
	s := eval.NewScheduledEvaluator(c, disp,
		eval.NewWindowBuffers(time.Hour, time.Now), reg, st, slog.Default())

	r := &rule.Rule{
		ID: "sql", Name: "any-errors", Enabled: true,
		Severity: rule.SeverityWarning, InputRef: "events",
		Schedule:  100 * time.Millisecond,
		Condition: rule.SQLReturnsRows{DataSource: "ops", Query: "SELECT * FROM errors", MinRows: 1},
	}
	if err := st.Rules().Create(context.Background(), r); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	c.Set(&eval.CompiledRule{Rule: r, Compiled: mustCompile(t, r.Condition)})
	_ = st.Subscribers().Create(context.Background(), seedSubscriberRow(t))
	_ = st.Subscriptions().Create(context.Background(), seedSubscriptionRow(t, r.ID))

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	deadline := time.Now().Add(3500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if ch.count() > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done

	if ch.count() == 0 {
		t.Fatalf("SQL rule never triggered (CountRows path uncovered)")
	}
}

// When the SQL rule's datasource isn't registered, CountRows errors. The
// evaluator must write LastError into live state and not panic.
func TestScheduledEvaluator_SQLPath_ErrorUpdatesLiveState(t *testing.T) {
	st := newStore(t)
	disp := newDispatcher(t, st, map[string]channel.Channel{})

	c := eval.NewCache()
	s := eval.NewScheduledEvaluator(c, disp,
		eval.NewWindowBuffers(time.Hour, time.Now), sqlquery.NewRegistry(), st, slog.Default())

	r := &rule.Rule{
		ID: "sql-err", Name: "missing-ds", Enabled: true,
		Severity: rule.SeverityWarning, InputRef: "events",
		Schedule:  100 * time.Millisecond,
		Condition: rule.SQLReturnsRows{DataSource: "nope", Query: "SELECT 1", MinRows: 1},
	}
	if err := st.Rules().Create(context.Background(), r); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c.Set(&eval.CompiledRule{Rule: r, Compiled: mustCompile(t, r.Condition)})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		ls, _ := st.LiveStates().Get(context.Background(), r.ID)
		if ls != nil && ls.LastError != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done

	ls, err := st.LiveStates().Get(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("get live state: %v", err)
	}
	if ls == nil || ls.LastError == "" {
		t.Fatalf("expected LastError to be recorded on rule, got %+v", ls)
	}
}

// ---------------- helpers ----------------

// seedSubscriberRow returns a minimal subscriber with one channel binding
// pointing at the recordingChannel used by the test fixtures.
func seedSubscriberRow(_ *testing.T) *subscriber.Subscriber {
	return &subscriber.Subscriber{
		ID:   "sub-1",
		Name: "Test",
		Channels: []subscriber.ChannelBinding{
			{Channel: "ch", Address: "user@example.com"},
		},
	}
}

// seedSubscriptionRow returns a Subscription tied to ruleID with no dwell
// or repeat so the dispatcher fires on the very first matching evaluation.
func seedSubscriptionRow(_ *testing.T, ruleID string) *subscriber.Subscription {
	return &subscriber.Subscription{
		ID:              "subscr-1",
		SubscriberID:    "sub-1",
		RuleID:          ruleID,
		NotifyOnResolve: true,
	}
}
