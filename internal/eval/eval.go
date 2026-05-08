// Package eval implements the push and scheduled rule evaluators. Both call
// Dispatcher.Tick with the boolean outcome of each rule's compiled
// condition; the dispatcher owns state-machine transitions and notification
// delivery.
package eval

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/dispatcher"
	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/input/sqlquery"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
	"github.com/ryan-evans-git/signalwatch/internal/store"
)

// CompiledRule pairs a stored rule with its compiled condition. The engine
// owns the cache and updates it whenever rules are mutated through the API.
type CompiledRule struct {
	Rule     *rule.Rule
	Compiled rule.Compiled
}

// Cache holds compiled rules indexed by id. Goroutine-safe.
type Cache struct {
	mu sync.RWMutex
	m  map[string]*CompiledRule
}

func NewCache() *Cache { return &Cache{m: map[string]*CompiledRule{}} }

func (c *Cache) Set(r *CompiledRule) {
	c.mu.Lock()
	c.m[r.Rule.ID] = r
	c.mu.Unlock()
}

func (c *Cache) Delete(id string) {
	c.mu.Lock()
	delete(c.m, id)
	c.mu.Unlock()
}

func (c *Cache) Get(id string) (*CompiledRule, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.m[id]
	return r, ok
}

func (c *Cache) ByInput(inputRef string) []*CompiledRule {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []*CompiledRule
	for _, r := range c.m {
		if r.Rule.InputRef == inputRef && r.Rule.Enabled && r.Compiled != nil {
			out = append(out, r)
		}
	}
	return out
}

func (c *Cache) Scheduled() []*CompiledRule {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []*CompiledRule
	for _, r := range c.m {
		if r.Rule.Enabled && r.Rule.Condition.Mode() == rule.ModeScheduled {
			out = append(out, r)
		}
	}
	return out
}

// PushEvaluator runs in a goroutine that consumes records from inputs and
// evaluates push-mode rules.
type PushEvaluator struct {
	cache    *Cache
	disp     *dispatcher.Dispatcher
	helpers  *WindowBuffers
	logger   *slog.Logger
}

func NewPushEvaluator(cache *Cache, disp *dispatcher.Dispatcher, helpers *WindowBuffers, logger *slog.Logger) *PushEvaluator {
	if logger == nil {
		logger = slog.Default()
	}
	return &PushEvaluator{cache: cache, disp: disp, helpers: helpers, logger: logger}
}

// Run consumes records from in and dispatches matched push-mode rules until
// in is closed or ctx is cancelled.
func (p *PushEvaluator) Run(ctx context.Context, in <-chan input.EvaluationRecord) {
	for {
		select {
		case <-ctx.Done():
			return
		case rec, ok := <-in:
			if !ok {
				return
			}
			p.helpers.Observe(rec.InputRef, rec.Record, rec.When)

			for _, cr := range p.cache.ByInput(rec.InputRef) {
				if cr.Rule.Condition.Mode() != rule.ModePush {
					continue
				}
				triggered, value, err := cr.Compiled.Eval(rule.EvalContext{Now: rec.When}, rec.Record)
				if err != nil {
					p.logger.Warn("eval.push_error", "rule", cr.Rule.Name, "err", err)
					continue
				}
				if err := p.disp.Tick(ctx, cr.Rule, triggered, value); err != nil {
					p.logger.Error("eval.push_dispatch_error", "rule", cr.Rule.Name, "err", err)
				}
			}
		}
	}
}

// ScheduledEvaluator runs scheduled rules on their cadence.
type ScheduledEvaluator struct {
	cache   *Cache
	disp    *dispatcher.Dispatcher
	helpers *WindowBuffers
	sql     *sqlquery.Registry
	logger  *slog.Logger
	store   store.Store
}

func NewScheduledEvaluator(cache *Cache, disp *dispatcher.Dispatcher, helpers *WindowBuffers, sql *sqlquery.Registry, st store.Store, logger *slog.Logger) *ScheduledEvaluator {
	if logger == nil {
		logger = slog.Default()
	}
	return &ScheduledEvaluator{cache: cache, disp: disp, helpers: helpers, sql: sql, store: st, logger: logger}
}

// Run wakes up at a fixed cadence and ticks any scheduled rule whose own
// schedule has elapsed since its last tick. The default tick is 1s.
func (s *ScheduledEvaluator) Run(ctx context.Context) {
	tick := time.NewTicker(1 * time.Second)
	defer tick.Stop()

	last := map[string]time.Time{}

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			for _, cr := range s.cache.Scheduled() {
				prev := last[cr.Rule.ID]
				if !prev.IsZero() && now.Sub(prev) < cr.Rule.Schedule {
					continue
				}
				last[cr.Rule.ID] = now
				s.evalOne(ctx, cr, now)
			}
		}
	}
}

func (s *ScheduledEvaluator) evalOne(ctx context.Context, cr *CompiledRule, now time.Time) {
	var (
		triggered bool
		value     string
		err       error
	)

	switch cond := cr.Compiled.(type) {
	case rule.SQLSpec:
		ds, query, minRows := cond.Spec()
		var n int
		n, err = s.sql.CountRows(ctx, ds, query)
		if err == nil {
			triggered = n >= minRows
			value = fmt.Sprintf("rows=%d (>=%d?)", n, minRows)
		}
	default:
		triggered, value, err = cr.Compiled.Eval(rule.EvalContext{
			Now:     now,
			Helpers: s.helpers.Helpers(cr.Rule.InputRef),
		}, nil)
	}

	if err != nil {
		s.logger.Warn("eval.scheduled_error", "rule", cr.Rule.Name, "err", err)
		// Surface the error in live state so the UI can show it. We don't
		// flip the rule's state on errors to avoid alert storms during
		// transient outages of an external datasource.
		ls, _ := s.store.LiveStates().Get(ctx, cr.Rule.ID)
		if ls == nil {
			ls = &rule.LiveState{RuleID: cr.Rule.ID, State: rule.StateOK}
		}
		ls.LastEvalAt = now
		ls.LastError = err.Error()
		_ = s.store.LiveStates().Upsert(ctx, ls)
		return
	}

	if err := s.disp.Tick(ctx, cr.Rule, triggered, value); err != nil {
		s.logger.Error("eval.scheduled_dispatch_error", "rule", cr.Rule.Name, "err", err)
	}
}
