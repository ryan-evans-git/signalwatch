// Package engine is the public library API for signalwatch. Embed this in
// your own Go program to evaluate rules in-process, or use the
// cmd/signalwatch service binary which wraps engine with an HTTP API and a
// bundled UI.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/channel"
	"github.com/ryan-evans-git/signalwatch/internal/dispatcher"
	"github.com/ryan-evans-git/signalwatch/internal/eval"
	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/input/event"
	"github.com/ryan-evans-git/signalwatch/internal/input/sqlquery"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
	"github.com/ryan-evans-git/signalwatch/internal/store"
)

// Re-export commonly-used types so library callers don't need to import
// internal packages.
type (
	Rule         = rule.Rule
	Severity     = rule.Severity
	Condition    = rule.Condition
	Threshold    = rule.Threshold
	WindowAgg    = rule.WindowAggregate
	PatternMatch = rule.PatternMatch
	SQLRule      = rule.SQLReturnsRows
	Op           = rule.Op
	Record       = rule.Record
)

// Options configures a new Engine.
type Options struct {
	// Store is the persistence layer. Required.
	Store store.Store
	// Channels is the channel registry keyed by configured channel name.
	Channels map[string]channel.Channel
	// Inputs is the list of input sources started by Engine.Start. The
	// EventInput, if you want library Submit, must be one of these.
	Inputs []input.Input
	// EventInput is the event input that backs library Submit and HTTP
	// /v1/events. It must also appear in Inputs to be Started.
	EventInput *event.Input
	// SQLDatasources backs SQLReturnsRows rules.
	SQLDatasources *sqlquery.Registry
	// MaxWindowSpan caps the longest window any window_aggregate rule can
	// look back over. Defaults to 24h.
	MaxWindowSpan time.Duration
	// Logger receives engine logs. Defaults to slog.Default().
	Logger *slog.Logger
}

// Engine ties the store, evaluator, dispatcher, and inputs together.
type Engine struct {
	opts        Options
	cache       *eval.Cache
	dispatcher  *dispatcher.Dispatcher
	pushEval    *eval.PushEvaluator
	scheduled   *eval.ScheduledEvaluator
	helpers     *eval.WindowBuffers
	logger      *slog.Logger

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	sink    chan input.EvaluationRecord
}

// New constructs an Engine. It does not start any goroutines; call Start.
func New(opts Options) (*Engine, error) {
	if opts.Store == nil {
		return nil, errors.New("engine: Store is required")
	}
	if opts.Channels == nil {
		opts.Channels = map[string]channel.Channel{}
	}
	if opts.SQLDatasources == nil {
		opts.SQLDatasources = sqlquery.NewRegistry()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.MaxWindowSpan <= 0 {
		opts.MaxWindowSpan = 24 * time.Hour
	}

	cache := eval.NewCache()
	helpers := eval.NewWindowBuffers(opts.MaxWindowSpan, time.Now)
	disp := dispatcher.New(dispatcher.Options{
		Store:    opts.Store,
		Channels: opts.Channels,
		Logger:   opts.Logger,
	})

	return &Engine{
		opts:       opts,
		cache:      cache,
		dispatcher: disp,
		pushEval:   eval.NewPushEvaluator(cache, disp, helpers, opts.Logger),
		scheduled:  eval.NewScheduledEvaluator(cache, disp, helpers, opts.SQLDatasources, opts.Store, opts.Logger),
		helpers:    helpers,
		logger:     opts.Logger,
	}, nil
}

// Start migrates the store, loads & compiles all rules, and launches the
// goroutines for inputs + evaluators. Start blocks only until those
// goroutines are running. The returned engine runs until ctx is cancelled
// or Close is called.
func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return errors.New("engine: already started")
	}

	if err := e.opts.Store.Migrate(ctx); err != nil {
		e.mu.Unlock()
		return fmt.Errorf("engine: migrate: %w", err)
	}

	if err := e.reloadAll(ctx); err != nil {
		e.mu.Unlock()
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.sink = make(chan input.EvaluationRecord, 256)
	e.started = true
	e.mu.Unlock()

	// Push evaluator.
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.pushEval.Run(runCtx, e.sink)
	}()

	// Scheduled evaluator.
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.scheduled.Run(runCtx)
	}()

	// Inputs.
	for _, in := range e.opts.Inputs {
		in := in
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			if err := in.Start(runCtx, e.sink); err != nil && !errors.Is(err, context.Canceled) {
				e.logger.Error("engine.input_failed", "input", in.Name(), "err", err)
			}
		}()
	}
	return nil
}

// Close cancels the engine's context and waits for all goroutines to exit.
func (e *Engine) Close() error {
	e.mu.Lock()
	if !e.started {
		e.mu.Unlock()
		return nil
	}
	e.cancel()
	e.started = false
	e.mu.Unlock()
	e.wg.Wait()
	return nil
}

// Submit pushes a record into the engine's event input. inputRef may be
// empty to use the configured event input's default.
func (e *Engine) Submit(ctx context.Context, inputRef string, r Record) error {
	if e.opts.EventInput == nil {
		return errors.New("engine: no EventInput configured")
	}
	return e.opts.EventInput.Submit(ctx, inputRef, r)
}

// Rules returns the rule repository, augmented to keep the compiled-rule
// cache in sync.
func (e *Engine) Rules() RuleAPI { return &ruleAPI{e: e} }

// Subscribers returns the subscriber repo (passthrough to the store).
func (e *Engine) Subscribers() store.SubscriberRepo { return e.opts.Store.Subscribers() }

// Subscriptions returns the subscription repo (passthrough to the store).
func (e *Engine) Subscriptions() store.SubscriptionRepo { return e.opts.Store.Subscriptions() }

// LiveStates returns the live-state repo (read-only consumer for UIs).
func (e *Engine) LiveStates() store.LiveStateRepo { return e.opts.Store.LiveStates() }

// Incidents returns the incident repo.
func (e *Engine) Incidents() store.IncidentRepo { return e.opts.Store.Incidents() }

// Notifications returns the notification audit repo.
func (e *Engine) Notifications() store.NotificationRepo { return e.opts.Store.Notifications() }

// Store exposes the underlying Store for advanced use.
func (e *Engine) Store() store.Store { return e.opts.Store }

// reloadAll loads every rule from the store and recompiles the cache.
func (e *Engine) reloadAll(ctx context.Context) error {
	rules, err := e.opts.Store.Rules().List(ctx)
	if err != nil {
		return err
	}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if err := e.compileAndCache(r); err != nil {
			e.logger.Warn("engine.rule_compile_failed", "rule", r.Name, "err", err)
		}
	}
	return nil
}

func (e *Engine) compileAndCache(r *Rule) error {
	compiled, err := r.Condition.Compile(nil)
	if err != nil {
		return err
	}
	e.cache.Set(&eval.CompiledRule{Rule: r, Compiled: compiled})
	return nil
}

// RuleAPI mirrors store.RuleRepo but recompiles cached rules on mutation.
type RuleAPI interface {
	Create(ctx context.Context, r *Rule) error
	Update(ctx context.Context, r *Rule) error
	Delete(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (*Rule, error)
	List(ctx context.Context) ([]*Rule, error)
}

type ruleAPI struct{ e *Engine }

func (a *ruleAPI) Create(ctx context.Context, r *Rule) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if err := a.e.opts.Store.Rules().Create(ctx, r); err != nil {
		return err
	}
	if r.Enabled {
		return a.e.compileAndCache(r)
	}
	return nil
}

func (a *ruleAPI) Update(ctx context.Context, r *Rule) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if err := a.e.opts.Store.Rules().Update(ctx, r); err != nil {
		return err
	}
	if r.Enabled {
		return a.e.compileAndCache(r)
	}
	a.e.cache.Delete(r.ID)
	return nil
}

func (a *ruleAPI) Delete(ctx context.Context, id string) error {
	if err := a.e.opts.Store.Rules().Delete(ctx, id); err != nil {
		return err
	}
	a.e.cache.Delete(id)
	return nil
}

func (a *ruleAPI) Get(ctx context.Context, id string) (*Rule, error) {
	return a.e.opts.Store.Rules().Get(ctx, id)
}

func (a *ruleAPI) List(ctx context.Context) ([]*Rule, error) {
	return a.e.opts.Store.Rules().List(ctx)
}
