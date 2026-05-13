// Package retention implements the alert-history pruner. A
// background goroutine periodically deletes resolved incidents (and
// cascades their notifications + incident_sub_states) older than a
// configured window. An optional Archiver receives the deleted
// incident payload before deletion so cold storage can keep a copy.
package retention

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/store"
	"github.com/ryan-evans-git/signalwatch/internal/subscriber"
)

// Archiver receives an incident + its notifications before retention
// deletes them. Implementations should be idempotent (the pruner may
// retry on subsequent ticks if archival fails).
type Archiver interface {
	Archive(ctx context.Context, inc *subscriber.Incident, notifs []*subscriber.Notification) error
}

// Config configures the Pruner.
type Config struct {
	// Store is required.
	Store store.Store
	// Window is how long resolved incidents live before being
	// pruned. Required.
	Window time.Duration
	// Interval is how often the pruner ticks. Defaults to 1h.
	Interval time.Duration
	// Archiver, when non-nil, receives each deleted incident +
	// notifications before deletion. Archival failures log a
	// warning but don't block the delete — operators who want hard
	// archival guarantees can wrap the Archiver themselves.
	Archiver Archiver
	// Now overrides time.Now for tests. Production callers leave
	// nil.
	Now func() time.Time
	// Logger receives info/warning logs. Defaults to slog.Default.
	Logger *slog.Logger
}

// Pruner runs the retention loop.
type Pruner struct {
	cfg    Config
	logger *slog.Logger
	now    func() time.Time
}

func New(cfg Config) (*Pruner, error) {
	if cfg.Store == nil {
		return nil, errors.New("retention: Store required")
	}
	if cfg.Window <= 0 {
		return nil, errors.New("retention: Window must be > 0")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Hour
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Pruner{cfg: cfg, logger: cfg.Logger, now: cfg.Now}, nil
}

// Start runs the prune loop until ctx is cancelled. Returns ctx.Err()
// on shutdown.
func (p *Pruner) Start(ctx context.Context) error {
	// Run once at startup so a long-running deployment doesn't wait
	// a whole Interval to do its first prune.
	p.RunOnce(ctx)

	t := time.NewTicker(p.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			p.RunOnce(ctx)
		}
	}
}

// RunOnce performs a single prune pass. Exported for tests and for
// any operator who wants to wire a "prune now" admin endpoint.
//
// The flow is:
//  1. List incidents resolved before (now - window).
//  2. For each, fetch its notifications.
//  3. Archive (if configured). Failure is logged + skipped — the
//     incident still gets deleted.
//  4. Delete the batch from the store (cascades to notifications +
//     incident_sub_states).
//
// Pulling the list and the delete with the same cutoff means any
// incident resolved DURING the prune is safe; it'll be picked up on
// the next tick.
func (p *Pruner) RunOnce(ctx context.Context) {
	cutoff := p.now().Add(-p.cfg.Window)
	cutoffMS := cutoff.UnixMilli()
	candidates, err := p.cfg.Store.Incidents().ListResolvedBefore(ctx, cutoffMS)
	if err != nil {
		p.logger.Warn("retention.list_error", "err", err)
		return
	}
	if len(candidates) == 0 {
		return
	}
	if p.cfg.Archiver != nil {
		for _, inc := range candidates {
			notifs, err := p.cfg.Store.Notifications().ListForIncident(ctx, inc.ID)
			if err != nil {
				p.logger.Warn("retention.notifications_fetch_error",
					"incident_id", inc.ID, "err", err)
				// Archive with whatever we have; better partial than nothing.
				notifs = nil
			}
			if err := p.cfg.Archiver.Archive(ctx, inc, notifs); err != nil {
				p.logger.Warn("retention.archive_error",
					"incident_id", inc.ID, "err", err)
			}
		}
	}
	n, err := p.cfg.Store.Incidents().DeleteResolvedBefore(ctx, cutoffMS)
	if err != nil {
		p.logger.Warn("retention.delete_error", "err", err)
		return
	}
	p.logger.Info("retention.pruned",
		"deleted", n, "cutoff", cutoff.UTC().Format(time.RFC3339))
}
