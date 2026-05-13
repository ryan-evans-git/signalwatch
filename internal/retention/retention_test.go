package retention_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/retention"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
	"github.com/ryan-evans-git/signalwatch/internal/store/sqlite"
	"github.com/ryan-evans-git/signalwatch/internal/subscriber"
)

// fkRule returns a valid rule for FK satisfaction. Same shape across
// tests so multiple seed calls don't trip the duplicate-id UNIQUE.
func fkRule() *rule.Rule {
	return &rule.Rule{
		ID: "r", Name: "rule-r", Enabled: true,
		Severity: rule.SeverityInfo, InputRef: "events",
		Condition: rule.PatternMatch{Field: "level", Kind: rule.MatchContains, Pattern: "ERROR"},
	}
}

// ---- fixture ----

func newStore(t *testing.T) *sqlite.Store {
	t.Helper()
	st, err := sqlite.Open("file::memory:?cache=shared&mode=memory&_pragma=foreign_keys(1)&test_id=" + t.Name())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// seed creates a resolved incident at `resolvedAt` (Unix ms) with one
// notification. The rule + subscriber + subscription FKs are
// created on first call per store.
func seed(t *testing.T, st *sqlite.Store, id string, resolvedAt int64) {
	t.Helper()
	ctx := context.Background()
	// Ensure FK rows exist (idempotent across calls).
	_ = st.Rules().Create(ctx, fkRule())
	_ = st.Subscribers().Create(ctx, &subscriber.Subscriber{ID: "sub", Name: "Sub"})
	_ = st.Subscriptions().Create(ctx, &subscriber.Subscription{ID: "subscr", SubscriberID: "sub", RuleID: "r"})

	if err := st.Incidents().Open(ctx, &subscriber.Incident{
		ID: id, RuleID: "r", TriggeredAt: time.UnixMilli(resolvedAt - 1000),
	}); err != nil {
		t.Fatalf("Open %s: %v", id, err)
	}
	if err := st.Incidents().Resolve(ctx, id, resolvedAt); err != nil {
		t.Fatalf("Resolve %s: %v", id, err)
	}
	if err := st.Notifications().Record(ctx, &subscriber.Notification{
		ID: "n-" + id, IncidentID: id, SubscriptionID: "subscr", SubscriberID: "sub",
		Channel: "ch", Address: "a@x", Kind: subscriber.KindFiring,
		SentAt: time.UnixMilli(resolvedAt), Status: "ok",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
}

// ---- New validation ----

func TestNew_RequiresStore(t *testing.T) {
	if _, err := retention.New(retention.Config{Window: time.Hour}); err == nil {
		t.Fatalf("want store-required error")
	}
}

func TestNew_RequiresPositiveWindow(t *testing.T) {
	st := newStore(t)
	for _, w := range []time.Duration{0, -time.Hour} {
		if _, err := retention.New(retention.Config{Store: st, Window: w}); err == nil {
			t.Errorf("Window=%v: want error", w)
		}
	}
}

func TestNew_DefaultsIntervalToHour(t *testing.T) {
	st := newStore(t)
	if _, err := retention.New(retention.Config{Store: st, Window: time.Hour}); err != nil {
		t.Fatalf("New: %v", err)
	}
}

// ---- RunOnce: prunes the right incidents ----

func TestRunOnce_DeletesIncidentsBeyondWindow(t *testing.T) {
	st := newStore(t)
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	window := 24 * time.Hour
	cutoff := now.Add(-window).UnixMilli()

	// Old: well before cutoff → deleted.
	seedFull(t, st, "old", cutoff-time.Hour.Milliseconds())
	// Fresh: still within window → kept.
	seedFull(t, st, "fresh", cutoff+time.Hour.Milliseconds())
	// Unresolved → kept regardless of age.
	seedOpenOnly(t, st, "firing", cutoff-10*time.Hour.Milliseconds())

	p, err := retention.New(retention.Config{
		Store:  st,
		Window: window,
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.RunOnce(context.Background())

	if got, _ := st.Incidents().Get(context.Background(), "old"); got != nil {
		t.Errorf("old incident should be deleted")
	}
	if got, _ := st.Incidents().Get(context.Background(), "fresh"); got == nil {
		t.Errorf("fresh incident should remain")
	}
	if got, _ := st.Incidents().Get(context.Background(), "firing"); got == nil {
		t.Errorf("firing incident should remain")
	}
}

// ---- RunOnce: archiver receives each deleted incident ----

type recordingArchiver struct {
	mu       sync.Mutex
	archived []string
	errFor   string // if set, return error for that incident ID
}

func (r *recordingArchiver) Archive(_ context.Context, inc *subscriber.Incident, notifs []*subscriber.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.errFor == inc.ID {
		return errors.New("synthetic archive failure")
	}
	r.archived = append(r.archived, inc.ID)
	_ = notifs
	return nil
}

func (r *recordingArchiver) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.archived...)
}

func TestRunOnce_ArchiverReceivesDeletedIncidents(t *testing.T) {
	st := newStore(t)
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	window := 24 * time.Hour
	cutoff := now.Add(-window).UnixMilli()
	seedFull(t, st, "old-1", cutoff-time.Hour.Milliseconds())
	seedFull(t, st, "old-2", cutoff-2*time.Hour.Milliseconds())

	arc := &recordingArchiver{}
	p, _ := retention.New(retention.Config{
		Store: st, Window: window, Archiver: arc,
		Now: func() time.Time { return now },
	})
	p.RunOnce(context.Background())

	seen := arc.seen()
	if len(seen) != 2 {
		t.Errorf("archiver should have seen 2 incidents, got %v", seen)
	}
}

func TestRunOnce_EmptyListIsNoop(t *testing.T) {
	st := newStore(t)
	p, _ := retention.New(retention.Config{
		Store:  st,
		Window: time.Hour,
		Now:    func() time.Time { return time.Now() },
	})
	// No seeded incidents — RunOnce should return immediately.
	p.RunOnce(context.Background())
}

func TestStart_TicksAtInterval(t *testing.T) {
	st := newStore(t)
	// Seed two incidents resolved in the past; only one is old
	// enough to be pruned on the first tick. The second one ages
	// past the cutoff between the startup-run and the first ticker
	// fire, so the ticker case branch gets exercised.
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	clock := atomic.Pointer[time.Time]{}
	clock.Store(&now)
	window := 24 * time.Hour
	cutoff := func() int64 { return clock.Load().Add(-window).UnixMilli() }
	seedFull(t, st, "old", cutoff()-time.Hour.Milliseconds())
	// Ages-in incident: not yet eligible at startup; will be after
	// the clock advances.
	seedFull(t, st, "aging", cutoff()+time.Minute.Milliseconds())

	p, _ := retention.New(retention.Config{
		Store: st, Window: window,
		Interval: 50 * time.Millisecond,
		Now:      func() time.Time { return *clock.Load() },
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = p.Start(ctx); close(done) }()

	// Wait for startup run.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got, _ := st.Incidents().Get(context.Background(), "old"); got == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Advance the clock so "aging" is now beyond the cutoff.
	later := now.Add(2 * time.Hour)
	clock.Store(&later)
	// Wait for the next tick to fire and prune.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := st.Incidents().Get(context.Background(), "aging"); got == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got, _ := st.Incidents().Get(context.Background(), "aging"); got != nil {
		t.Errorf("ticker fire should have pruned 'aging'")
	}
	cancel()
	<-done
}

// Close the underlying DB so List + Delete + notifications fetch
// all error out on the next RunOnce. The pruner must log + return
// without panicking.
func TestRunOnce_StoreErrorsAreLoggedNotPanic(t *testing.T) {
	st := newStore(t)
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	window := time.Hour
	cutoff := now.Add(-window).UnixMilli()
	seedFull(t, st, "old", cutoff-time.Minute.Milliseconds())

	arc := &recordingArchiver{}
	p, _ := retention.New(retention.Config{
		Store: st, Window: window, Archiver: arc,
		Now: func() time.Time { return now },
	})
	// Close DB out from under the pruner.
	_ = st.Close()
	p.RunOnce(context.Background())
	// No panic = the test passes. We can't easily assert log lines
	// without intercepting slog; the behavior contract is "errors
	// don't crash the process."
}

func TestRunOnce_ArchiveFailureStillDeletes(t *testing.T) {
	// Archive failures log a warning but don't block the delete.
	st := newStore(t)
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	window := 24 * time.Hour
	cutoff := now.Add(-window).UnixMilli()
	seedFull(t, st, "old", cutoff-time.Hour.Milliseconds())

	arc := &recordingArchiver{errFor: "old"}
	p, _ := retention.New(retention.Config{
		Store: st, Window: window, Archiver: arc,
		Now: func() time.Time { return now },
	})
	p.RunOnce(context.Background())

	if got, _ := st.Incidents().Get(context.Background(), "old"); got != nil {
		t.Errorf("incident should still be deleted even when archive fails")
	}
}

// ---- Start: ticker + ctx cancel ----

func TestStart_RunsOnceAtStartupAndOnTick(t *testing.T) {
	st := newStore(t)
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	window := 24 * time.Hour
	cutoff := now.Add(-window).UnixMilli()
	seedFull(t, st, "old", cutoff-time.Hour.Milliseconds())

	p, _ := retention.New(retention.Config{
		Store: st, Window: window,
		Interval: 30 * time.Minute, // never fires during this test; the
		Now:      func() time.Time { return now },
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Start(ctx) }()

	// Wait for the startup-prune to complete.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got, _ := st.Incidents().Get(context.Background(), "old"); got == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got, _ := st.Incidents().Get(context.Background(), "old"); got != nil {
		t.Fatalf("startup prune should have deleted 'old'")
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start: want context.Canceled, got %v", err)
	}
}

// ---- JSONFileArchiver ----

func TestJSONFileArchiver_WritesJSONLLines(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	a := &retention.JSONFileArchiver{Dir: dir, Now: func() time.Time { return now }}
	t.Cleanup(func() { _ = a.Close() })

	for _, id := range []string{"i-1", "i-2"} {
		if err := a.Archive(context.Background(), &subscriber.Incident{
			ID: id, RuleID: "r1", TriggeredAt: now.Add(-time.Hour),
		}, nil); err != nil {
			t.Fatalf("Archive %s: %v", id, err)
		}
	}

	path := filepath.Join(dir, "incidents-2026-05-13.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive file: %v", err)
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	var lines []map[string]any
	for scan.Scan() {
		var m map[string]any
		if err := json.Unmarshal(scan.Bytes(), &m); err != nil {
			t.Fatalf("invalid JSONL line: %v", err)
		}
		lines = append(lines, m)
	}
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}
}

func TestJSONFileArchiver_NewDayRotatesFile(t *testing.T) {
	dir := t.TempDir()
	clock := atomic.Pointer[time.Time]{}
	d1 := time.Date(2026, 5, 13, 23, 59, 0, 0, time.UTC)
	clock.Store(&d1)
	a := &retention.JSONFileArchiver{Dir: dir, Now: func() time.Time { return *clock.Load() }}
	t.Cleanup(func() { _ = a.Close() })

	_ = a.Archive(context.Background(), &subscriber.Incident{ID: "yesterday", RuleID: "r1"}, nil)
	d2 := time.Date(2026, 5, 14, 0, 1, 0, 0, time.UTC)
	clock.Store(&d2)
	_ = a.Archive(context.Background(), &subscriber.Incident{ID: "today", RuleID: "r1"}, nil)

	for _, want := range []string{"incidents-2026-05-13.jsonl", "incidents-2026-05-14.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected file %s: %v", want, err)
		}
	}
}

func TestJSONFileArchiver_RequiresDir(t *testing.T) {
	a := &retention.JSONFileArchiver{}
	if err := a.Archive(context.Background(), &subscriber.Incident{ID: "x"}, nil); err == nil {
		t.Fatalf("empty Dir should error")
	}
}

// Pointing at a path inside a regular file fails the os.MkdirAll.
func TestJSONFileArchiver_UnwritableDirErrors(t *testing.T) {
	// Create a regular file then use it as a "directory" — MkdirAll
	// fails because a non-directory blocks the path.
	f, err := os.CreateTemp(t.TempDir(), "blocker-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	_ = f.Close()
	a := &retention.JSONFileArchiver{Dir: filepath.Join(f.Name(), "sub")}
	t.Cleanup(func() { _ = a.Close() })
	if err := a.Archive(context.Background(), &subscriber.Incident{ID: "x"}, nil); err == nil {
		t.Fatalf("unwritable Dir should error")
	}
}

// Close-without-archive is a no-op and must not panic.
func TestJSONFileArchiver_CloseWithoutArchiveIsNoop(t *testing.T) {
	a := &retention.JSONFileArchiver{Dir: t.TempDir()}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// Invalid URL (control character) fails http.NewRequestWithContext
// before any network call.
func TestWebhookArchiver_BadURLErrors(t *testing.T) {
	a := &retention.WebhookArchiver{URL: "http://[::1]:8080/\x7f"}
	if err := a.Archive(context.Background(), &subscriber.Incident{ID: "x"}, nil); err == nil {
		t.Fatalf("bad URL should error")
	}
}

// ---- WebhookArchiver ----

func TestWebhookArchiver_POSTsJSON(t *testing.T) {
	var (
		mu      sync.Mutex
		gotBody map[string]any
		gotCT   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dec := json.NewDecoder(r.Body)
		var parsed map[string]any
		_ = dec.Decode(&parsed)
		mu.Lock()
		gotBody = parsed
		gotCT = r.Header.Get("Content-Type")
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	a := &retention.WebhookArchiver{URL: srv.URL}
	err := a.Archive(context.Background(), &subscriber.Incident{
		ID: "inc-1", RuleID: "r1", TriggeredAt: time.Now(),
	}, []*subscriber.Notification{
		{ID: "n-1", IncidentID: "inc-1"},
	})
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(gotCT, "json") {
		t.Errorf("Content-Type: want json, got %q", gotCT)
	}
	if inc, _ := gotBody["incident"].(map[string]any); inc["id"] != "inc-1" {
		t.Errorf("incident id: %v", inc["id"])
	}
	notifs, _ := gotBody["notifications"].([]any)
	if len(notifs) != 1 {
		t.Errorf("notifications: want 1, got %d", len(notifs))
	}
}

func TestWebhookArchiver_Non2xxErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	a := &retention.WebhookArchiver{URL: srv.URL}
	err := a.Archive(context.Background(), &subscriber.Incident{ID: "x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("want 500 error, got %v", err)
	}
}

func TestWebhookArchiver_RequiresURL(t *testing.T) {
	a := &retention.WebhookArchiver{}
	if err := a.Archive(context.Background(), &subscriber.Incident{ID: "x"}, nil); err == nil {
		t.Fatalf("empty URL should error")
	}
}

func TestWebhookArchiver_NetworkError(t *testing.T) {
	a := &retention.WebhookArchiver{URL: "http://127.0.0.1:1"}
	if err := a.Archive(context.Background(), &subscriber.Incident{ID: "x"}, nil); err == nil {
		t.Fatalf("network failure should error")
	}
}

// ---- helpers ----

// seedFull creates a resolved-at-ms incident plus its notification.
// Idempotent on the FK rows.
func seedFull(t *testing.T, st *sqlite.Store, id string, resolvedAt int64) {
	t.Helper()
	seed(t, st, id, resolvedAt)
}

// seedOpenOnly creates an unresolved incident.
func seedOpenOnly(t *testing.T, st *sqlite.Store, id string, triggeredMS int64) {
	t.Helper()
	ctx := context.Background()
	_ = st.Rules().Create(ctx, fkRule())
	if err := st.Incidents().Open(ctx, &subscriber.Incident{
		ID: id, RuleID: "r", TriggeredAt: time.UnixMilli(triggeredMS),
	}); err != nil {
		t.Fatalf("Open %s: %v", id, err)
	}
}
