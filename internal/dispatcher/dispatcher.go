// Package dispatcher applies per-subscription delivery rules (dwell, dedup,
// repeat, notify-on-resolve) to rule state transitions and routes the
// resulting notifications to channels.
package dispatcher

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ryan-evans-git/signalwatch/internal/channel"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
	"github.com/ryan-evans-git/signalwatch/internal/store"
	"github.com/ryan-evans-git/signalwatch/internal/subscriber"
)

// tracerName matches observability.TracerName. Duplicated as a string here
// to avoid an import cycle (observability imports channel; dispatcher
// imports channel).
const tracerName = "github.com/ryan-evans-git/signalwatch"

func tracer() trace.Tracer { return otel.Tracer(tracerName) }

// IDFunc returns a fresh unique identifier. Defaults to uuid.NewString.
type IDFunc func() string

// Dispatcher routes evaluator outcomes to channels through subscriptions.
type Dispatcher struct {
	store    store.Store
	channels map[string]channel.Channel
	logger   *slog.Logger
	now      func() time.Time
	id       IDFunc
}

// Options configures a Dispatcher.
type Options struct {
	Store    store.Store
	Channels map[string]channel.Channel
	Logger   *slog.Logger
	Now      func() time.Time
	IDFunc   IDFunc
}

func New(opts Options) *Dispatcher {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.IDFunc == nil {
		opts.IDFunc = uuid.NewString
	}
	return &Dispatcher{
		store:    opts.Store,
		channels: opts.Channels,
		logger:   opts.Logger,
		now:      opts.Now,
		id:       opts.IDFunc,
	}
}

// Tick applies a fresh evaluation outcome for ruleID. triggered/value come
// directly from the rule's compiled condition. Tick is idempotent on stable
// state (a non-firing rule that stays non-firing produces no work).
//
// Tick is the single integration point between evaluators and the
// dispatcher. Both push and scheduled evaluators call it.
func (d *Dispatcher) Tick(ctx context.Context, r *rule.Rule, triggered bool, value string) (err error) {
	ctx, span := tracer().Start(ctx, "signalwatch.dispatcher.tick",
		trace.WithAttributes(
			attribute.String("signalwatch.rule.id", r.ID),
			attribute.String("signalwatch.rule.name", r.Name),
			attribute.String("signalwatch.severity", string(r.Severity)),
			attribute.Bool("signalwatch.rule.triggered", triggered),
		),
	)
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	now := d.now()

	// 1. Load current live state (or seed an OK one).
	ls, err := d.store.LiveStates().Get(ctx, r.ID)
	if err != nil {
		return fmt.Errorf("dispatcher: load live state: %w", err)
	}
	if ls == nil {
		ls = &rule.LiveState{RuleID: r.ID, State: rule.StateOK}
	}

	// 2. Apply the transition. If we're opening a new incident, mint an id
	// up front so the live-state row carries it for downstream consumers.
	var newIncidentID string
	if triggered && ls.State != rule.StateFiring {
		newIncidentID = d.id()
	}
	t := rule.Apply(ls, triggered, value, now, newIncidentID)

	// 3. Persist the updated live state.
	if err := d.store.LiveStates().Upsert(ctx, ls); err != nil {
		return fmt.Errorf("dispatcher: upsert live state: %w", err)
	}

	// 4. Open or close incidents as needed.
	switch {
	case t.Opened():
		inc := &subscriber.Incident{
			ID:          t.IncidentID,
			RuleID:      r.ID,
			TriggeredAt: t.TriggeredAt,
			LastValue:   value,
		}
		if err := d.store.Incidents().Open(ctx, inc); err != nil {
			return fmt.Errorf("dispatcher: open incident: %w", err)
		}
		d.logger.Info("incident.opened", "rule", r.Name, "incident", inc.ID)
	case t.Closed():
		if err := d.store.Incidents().Resolve(ctx, t.IncidentID, now.UnixMilli()); err != nil {
			return fmt.Errorf("dispatcher: resolve incident: %w", err)
		}
		d.logger.Info("incident.resolved", "rule", r.Name, "incident", t.IncidentID)
	}

	// 5. Find subscriptions matching this rule.
	subs, err := d.store.Subscriptions().ListForRule(ctx, r.ID, r.Labels)
	if err != nil {
		return fmt.Errorf("dispatcher: list subscriptions: %w", err)
	}

	// 6. For each subscription, decide whether to deliver and which kind.
	for _, sub := range subs {
		if err := d.evaluateSubscription(ctx, r, ls, sub, t, now); err != nil {
			d.logger.Error("dispatcher.subscription_failed",
				"rule", r.Name, "subscription", sub.ID, "err", err)
		}
	}
	return nil
}

// evaluateSubscription is the per-subscription state machine that decides
// whether to deliver a notification on this evaluation. Splitting the
// resolution and firing branches into helpers would scatter the
// dwell/dedup/repeat logic and obscure the state-machine shape, so the
// gocyclo cap is intentionally relaxed for this function only.
//
//nolint:gocyclo
func (d *Dispatcher) evaluateSubscription(
	ctx context.Context,
	r *rule.Rule,
	ls *rule.LiveState,
	sub *subscriber.Subscription,
	t rule.Transition,
	now time.Time,
) error {
	// Resolution path: rule just closed and the subscription wants resolves.
	if t.Closed() && t.IncidentID != "" {
		state, err := d.store.IncidentSubStates().Get(ctx, t.IncidentID, sub.ID)
		if err != nil {
			return err
		}
		// Only notify on resolve if a firing notification was actually sent
		// for this incident, otherwise the resolve would be the first the
		// subscriber heard of a rule that never crossed their dwell.
		if state == nil || state.NotifyCount == 0 || state.ResolutionSent {
			return nil
		}
		if !sub.NotifyOnResolve {
			return nil
		}
		if err := d.deliver(ctx, r, sub, t.IncidentID, subscriber.KindResolved, ls.LastValue, now); err != nil {
			return err
		}
		state.ResolutionSent = true
		return d.store.IncidentSubStates().Upsert(ctx, state)
	}

	// Firing path: rule is currently FIRING (either just opened or still open).
	if ls.State != rule.StateFiring {
		return nil
	}

	triggeredFor := now.Sub(ls.TriggeredAt)
	if triggeredFor < sub.Dwell {
		return nil
	}

	state, err := d.store.IncidentSubStates().Get(ctx, ls.IncidentID, sub.ID)
	if err != nil {
		return err
	}
	if state == nil {
		state = &subscriber.IncidentSubState{
			IncidentID:     ls.IncidentID,
			SubscriptionID: sub.ID,
		}
	}

	switch {
	case state.NotifyCount == 0:
		// First notification for this incident × subscription.
		if err := d.deliver(ctx, r, sub, ls.IncidentID, subscriber.KindFiring, ls.LastValue, now); err != nil {
			return err
		}
		state.LastNotifiedAt = now
		state.NotifyCount = 1
		return d.store.IncidentSubStates().Upsert(ctx, state)

	case sub.RepeatInterval > 0 && now.Sub(state.LastNotifiedAt) >= sub.RepeatInterval:
		// Renotify because the rule has been firing too long without resolving.
		if err := d.deliver(ctx, r, sub, ls.IncidentID, subscriber.KindRepeat, ls.LastValue, now); err != nil {
			return err
		}
		state.LastNotifiedAt = now
		state.NotifyCount++
		return d.store.IncidentSubStates().Upsert(ctx, state)
	}
	return nil
}

func (d *Dispatcher) deliver(
	ctx context.Context,
	r *rule.Rule,
	sub *subscriber.Subscription,
	incidentID string,
	kind subscriber.NotificationKind,
	value string,
	now time.Time,
) error {
	ctx, span := tracer().Start(ctx, "signalwatch.dispatcher.deliver",
		trace.WithAttributes(
			attribute.String("signalwatch.rule.id", r.ID),
			attribute.String("signalwatch.incident.id", incidentID),
			attribute.String("signalwatch.subscription.id", sub.ID),
			attribute.String("signalwatch.notification.kind", string(kind)),
		),
	)
	defer span.End()

	subscriberRow, err := d.store.Subscribers().Get(ctx, sub.SubscriberID)
	if err != nil {
		return err
	}
	if subscriberRow == nil {
		return fmt.Errorf("subscriber %s not found", sub.SubscriberID)
	}

	allowed := channelFilterSet(sub.ChannelFilter)

	var (
		sentOK  int
		sentErr int
		skipped int
	)
	for _, binding := range subscriberRow.Channels {
		if allowed != nil {
			if _, ok := allowed[binding.Channel]; !ok {
				skipped++
				continue
			}
		}
		ch, ok := d.channels[binding.Channel]
		if !ok {
			skipped++
			d.logger.Warn("dispatcher.channel_not_configured",
				"channel", binding.Channel, "subscriber", subscriberRow.ID)
			continue
		}

		n := channel.Notification{
			IncidentID:  incidentID,
			RuleID:      r.ID,
			RuleName:    r.Name,
			Severity:    string(r.Severity),
			Description: r.Description,
			Value:       value,
			Kind:        string(kind),
			Address:     binding.Address,
			Now:         now,
			TriggeredAt: now, // dispatcher only knows "now"; live state has full incident times if the channel needs them
		}

		audit := &subscriber.Notification{
			ID:             d.id(),
			IncidentID:     incidentID,
			SubscriptionID: sub.ID,
			SubscriberID:   subscriberRow.ID,
			Channel:        binding.Channel,
			Address:        binding.Address,
			Kind:           kind,
			SentAt:         now,
		}

		if err := ch.Send(ctx, n); err != nil {
			sentErr++
			audit.Status = "error"
			audit.Error = err.Error()
			d.logger.Warn("dispatcher.send_failed",
				"channel", binding.Channel, "subscription", sub.ID, "err", err)
		} else {
			sentOK++
			audit.Status = "ok"
		}

		if err := d.store.Notifications().Record(ctx, audit); err != nil {
			d.logger.Error("dispatcher.record_failed", "err", err)
		}
	}
	span.SetAttributes(
		attribute.Int("signalwatch.deliver.sent_ok", sentOK),
		attribute.Int("signalwatch.deliver.sent_err", sentErr),
		attribute.Int("signalwatch.deliver.skipped", skipped),
	)
	if sentErr > 0 {
		span.SetStatus(codes.Error, strconv.Itoa(sentErr)+" channel send(s) failed")
	}
	return nil
}

func channelFilterSet(filter []string) map[string]struct{} {
	if len(filter) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(filter))
	for _, f := range filter {
		out[f] = struct{}{}
	}
	return out
}
