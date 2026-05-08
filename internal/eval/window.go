package eval

import (
	"sync"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/rule"
)

// WindowBuffers holds rolling-window samples per (input_ref, field). Used by
// the helpers that back window_aggregate rules. The buffer is unbounded but
// records older than the largest seen window are pruned at read time.
type WindowBuffers struct {
	mu      sync.Mutex
	now     func() time.Time
	maxSpan time.Duration
	buf     map[string][]sample
}

type sample struct {
	at time.Time
	v  float64
}

// NewWindowBuffers creates an empty buffer set. maxSpan is the longest
// window any rule may need to look at; samples older than maxSpan are
// pruned. Pass 24h or 30d depending on your rule horizons.
func NewWindowBuffers(maxSpan time.Duration, now func() time.Time) *WindowBuffers {
	if now == nil {
		now = time.Now
	}
	return &WindowBuffers{now: now, maxSpan: maxSpan, buf: map[string][]sample{}}
}

// Observe stores a sample for an (inputRef, field). Non-numeric fields are
// silently ignored.
func (w *WindowBuffers) Observe(inputRef string, r rule.Record, when time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for k, v := range r {
		f, ok := numeric(v)
		if !ok {
			continue
		}
		key := inputRef + "/" + k
		w.buf[key] = append(w.buf[key], sample{at: when, v: f})
	}
	w.prune(w.now())
}

func (w *WindowBuffers) prune(now time.Time) {
	if w.maxSpan <= 0 {
		return
	}
	cutoff := now.Add(-w.maxSpan)
	for k, s := range w.buf {
		i := 0
		for ; i < len(s); i++ {
			if s[i].at.After(cutoff) {
				break
			}
		}
		if i > 0 {
			w.buf[k] = s[i:]
		}
	}
}

// Helpers binds a WindowBuffers to a specific inputRef and exposes the
// rule.ExprHelpers interface used by compiled conditions.
func (w *WindowBuffers) Helpers(inputRef string) rule.ExprHelpers {
	return &boundHelpers{buf: w, inputRef: inputRef}
}

type boundHelpers struct {
	buf      *WindowBuffers
	inputRef string
}

func (b *boundHelpers) AvgOver(field string, window time.Duration) (float64, bool) {
	samples := b.buf.samples(b.inputRef, field, window)
	if len(samples) == 0 {
		return 0, false
	}
	var sum float64
	for _, s := range samples {
		sum += s.v
	}
	return sum / float64(len(samples)), true
}

func (b *boundHelpers) SumOver(field string, window time.Duration) (float64, bool) {
	samples := b.buf.samples(b.inputRef, field, window)
	if len(samples) == 0 {
		return 0, false
	}
	var sum float64
	for _, s := range samples {
		sum += s.v
	}
	return sum, true
}

func (b *boundHelpers) MinOver(field string, window time.Duration) (float64, bool) {
	samples := b.buf.samples(b.inputRef, field, window)
	if len(samples) == 0 {
		return 0, false
	}
	m := samples[0].v
	for _, s := range samples[1:] {
		if s.v < m {
			m = s.v
		}
	}
	return m, true
}

func (b *boundHelpers) MaxOver(field string, window time.Duration) (float64, bool) {
	samples := b.buf.samples(b.inputRef, field, window)
	if len(samples) == 0 {
		return 0, false
	}
	m := samples[0].v
	for _, s := range samples[1:] {
		if s.v > m {
			m = s.v
		}
	}
	return m, true
}

func (b *boundHelpers) CountOver(field string, window time.Duration) (int, bool) {
	samples := b.buf.samples(b.inputRef, field, window)
	return len(samples), true
}

func (b *boundHelpers) RecordRegexMatch(_, _ string) (bool, error) {
	// Used only by the expression engine extension — push-mode rules call
	// regex via the typed PatternMatch condition. Stub for now.
	return false, nil
}

func (w *WindowBuffers) samples(inputRef, field string, window time.Duration) []sample {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.prune(w.now())
	cutoff := w.now().Add(-window)
	key := inputRef + "/" + field
	all := w.buf[key]
	out := make([]sample, 0, len(all))
	for _, s := range all {
		if !s.at.Before(cutoff) {
			out = append(out, s)
		}
	}
	return out
}

func numeric(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}
