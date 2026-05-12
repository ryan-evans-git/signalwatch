package eval

import (
	"testing"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/rule"
)

// fixedClock holds a fixed time so tests can replace time.Now without
// using real clocks.
type fixedClock struct{ t time.Time }

func (c *fixedClock) now() time.Time { return c.t }

func TestNewWindowBuffers_DefaultsNowToTimeNow(t *testing.T) {
	w := NewWindowBuffers(time.Hour, nil)
	if w.now == nil {
		t.Fatalf("expected default now func")
	}
	// Smoke-test that the default now returns a non-zero, recent time.
	got := w.now()
	if got.IsZero() {
		t.Fatalf("default now() returned zero time")
	}
}

func TestWindowBuffers_ObserveSkipsNonNumericFields(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)}
	w := NewWindowBuffers(time.Hour, clk.now)

	w.Observe("events", rule.Record{
		"v":      3.0,
		"msg":    "hello",
		"struct": map[string]any{"nested": 1},
	}, clk.t)

	// Only the numeric "v" should be stored.
	h := w.Helpers("events")
	if n, ok := h.CountOver("v", time.Hour); !ok || n != 1 {
		t.Fatalf("CountOver v: want 1, got %d ok=%v", n, ok)
	}
	if n, ok := h.CountOver("msg", time.Hour); !ok || n != 0 {
		t.Fatalf("CountOver msg: want 0 (non-numeric not stored), got %d ok=%v", n, ok)
	}
}

func TestWindowBuffers_HelpersAggregations(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)}
	w := NewWindowBuffers(time.Hour, clk.now)

	// Observe 4 samples with v = 10, 20, 30, 40 at the same instant.
	for _, v := range []float64{10, 20, 30, 40} {
		w.Observe("events", rule.Record{"v": v}, clk.t)
	}

	h := w.Helpers("events")

	if avg, ok := h.AvgOver("v", time.Minute); !ok || avg != 25 {
		t.Fatalf("AvgOver: want 25, got %v ok=%v", avg, ok)
	}
	if sum, ok := h.SumOver("v", time.Minute); !ok || sum != 100 {
		t.Fatalf("SumOver: want 100, got %v", sum)
	}
	if mn, ok := h.MinOver("v", time.Minute); !ok || mn != 10 {
		t.Fatalf("MinOver: want 10, got %v", mn)
	}
	if mx, ok := h.MaxOver("v", time.Minute); !ok || mx != 40 {
		t.Fatalf("MaxOver: want 40, got %v", mx)
	}
	if c, ok := h.CountOver("v", time.Minute); !ok || c != 4 {
		t.Fatalf("CountOver: want 4, got %d", c)
	}

	// Missing field returns the zero/empty result.
	if _, ok := h.AvgOver("missing", time.Minute); ok {
		t.Fatalf("AvgOver missing: want ok=false")
	}
	if _, ok := h.SumOver("missing", time.Minute); ok {
		t.Fatalf("SumOver missing: want ok=false")
	}
	if _, ok := h.MinOver("missing", time.Minute); ok {
		t.Fatalf("MinOver missing: want ok=false")
	}
	if _, ok := h.MaxOver("missing", time.Minute); ok {
		t.Fatalf("MaxOver missing: want ok=false")
	}
	if c, _ := h.CountOver("missing", time.Minute); c != 0 {
		t.Fatalf("CountOver missing: want 0, got %d", c)
	}
}

func TestWindowBuffers_WindowExcludesOutOfRangeSamples(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)}
	w := NewWindowBuffers(24*time.Hour, clk.now)

	// Sample at T-2m (out of a 1m window).
	w.Observe("events", rule.Record{"v": 99.0}, clk.t.Add(-2*time.Minute))
	// Sample at T (in window).
	w.Observe("events", rule.Record{"v": 5.0}, clk.t)

	h := w.Helpers("events")
	avg, ok := h.AvgOver("v", time.Minute)
	if !ok || avg != 5 {
		t.Fatalf("expected only the in-window sample: avg=%v ok=%v", avg, ok)
	}
}

// Observe runs prune at write time using the configured maxSpan; samples
// older than maxSpan should be evicted, so a subsequent read using a longer
// window doesn't see them.
func TestWindowBuffers_PrunesByMaxSpan(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)}
	w := NewWindowBuffers(time.Hour, clk.now)

	// Pretend a sample arrived 2 hours ago.
	w.Observe("events", rule.Record{"v": 100.0}, clk.t.Add(-2*time.Hour))
	// Now observe a fresh one — Observe calls prune(now), which should
	// evict the old sample because it's older than maxSpan.
	w.Observe("events", rule.Record{"v": 1.0}, clk.t)

	// Even with a 24h window, the old sample is gone.
	h := w.Helpers("events")
	if c, ok := h.CountOver("v", 24*time.Hour); !ok || c != 1 {
		t.Fatalf("CountOver after prune: want 1 ok=true, got %d ok=%v", c, ok)
	}
}

// maxSpan <= 0 disables pruning; this is the path where prune is a no-op.
func TestWindowBuffers_NonPositiveMaxSpanDisablesPrune(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)}
	w := NewWindowBuffers(0, clk.now)

	w.Observe("events", rule.Record{"v": 1.0}, clk.t.Add(-48*time.Hour))
	w.Observe("events", rule.Record{"v": 2.0}, clk.t)

	// All samples persist; 48h window picks them both up.
	h := w.Helpers("events")
	if c, _ := h.CountOver("v", 49*time.Hour); c != 2 {
		t.Fatalf("CountOver with prune disabled: want 2, got %d", c)
	}
}

// RecordRegexMatch is a stub; pin its current contract so a future change
// is intentional.
func TestWindowBuffers_RecordRegexMatchStub(t *testing.T) {
	w := NewWindowBuffers(time.Hour, time.Now)
	h := w.Helpers("events")
	got, err := h.RecordRegexMatch("field", "pat")
	if err != nil || got {
		t.Fatalf("RecordRegexMatch stub: want (false, nil), got (%v, %v)", got, err)
	}
}

// Min/Max must update m on each strictly-better sample. Iterating an
// already-sorted sequence (ascending or descending) only exercises one
// side of the comparison, so verify with an out-of-order series.
func TestWindowBuffers_MinMaxUpdateOnLaterSamples(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)}
	w := NewWindowBuffers(time.Hour, clk.now)

	// Series 50, 10, 30 — first sample is neither min nor max, so the
	// loop body's assignment fires for both helpers.
	for _, v := range []float64{50, 10, 30} {
		w.Observe("events", rule.Record{"v": v}, clk.t)
	}
	h := w.Helpers("events")
	if mn, _ := h.MinOver("v", time.Minute); mn != 10 {
		t.Fatalf("MinOver: want 10, got %v", mn)
	}
	if mx, _ := h.MaxOver("v", time.Minute); mx != 50 {
		t.Fatalf("MaxOver: want 50, got %v", mx)
	}
}

// HelpersBindToInputRef proves the bound-helper key namespacing: a sample
// on input A is invisible from input B's helpers.
func TestWindowBuffers_HelpersBindToInputRef(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)}
	w := NewWindowBuffers(time.Hour, clk.now)

	w.Observe("inputA", rule.Record{"v": 5.0}, clk.t)
	w.Observe("inputB", rule.Record{"v": 10.0}, clk.t)

	a := w.Helpers("inputA")
	b := w.Helpers("inputB")
	if avg, _ := a.AvgOver("v", time.Minute); avg != 5 {
		t.Fatalf("A avg: want 5, got %v", avg)
	}
	if avg, _ := b.AvgOver("v", time.Minute); avg != 10 {
		t.Fatalf("B avg: want 10, got %v", avg)
	}
}

func TestNumericCoercion(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want float64
		ok   bool
	}{
		{"float64", float64(1.5), 1.5, true},
		{"float32", float32(2.5), 2.5, true},
		{"int", int(3), 3, true},
		{"int32", int32(4), 4, true},
		{"int64", int64(5), 5, true},
		{"string", "nope", 0, false},
		{"bool", true, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := numeric(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok: want %v got %v", tc.ok, ok)
			}
			if ok && got != tc.want {
				t.Fatalf("value: want %v got %v", tc.want, got)
			}
		})
	}
}
