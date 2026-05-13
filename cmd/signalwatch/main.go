// signalwatch is the standalone service binary for the signalwatch alert
// and notifications framework. It loads a YAML config, wires up the engine,
// mounts the HTTP API + bundled UI, and runs until interrupted.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ryan-evans-git/signalwatch/engine"
	"github.com/ryan-evans-git/signalwatch/internal/api"
	"github.com/ryan-evans-git/signalwatch/internal/channel"
	"github.com/ryan-evans-git/signalwatch/internal/channel/discord"
	"github.com/ryan-evans-git/signalwatch/internal/channel/pagerduty"
	"github.com/ryan-evans-git/signalwatch/internal/channel/slack"
	"github.com/ryan-evans-git/signalwatch/internal/channel/sms"
	"github.com/ryan-evans-git/signalwatch/internal/channel/smtp"
	"github.com/ryan-evans-git/signalwatch/internal/channel/teams"
	"github.com/ryan-evans-git/signalwatch/internal/channel/webhook"
	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/input/event"
	"github.com/ryan-evans-git/signalwatch/internal/input/scrape"
	"github.com/ryan-evans-git/signalwatch/internal/input/sqlquery"
	"github.com/ryan-evans-git/signalwatch/internal/observability"
	"github.com/ryan-evans-git/signalwatch/internal/retention"
	"github.com/ryan-evans-git/signalwatch/internal/store/sqlite"
	"github.com/ryan-evans-git/signalwatch/internal/ui"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type config struct {
	HTTP struct {
		Addr string `yaml:"addr"`
	} `yaml:"http"`
	Store struct {
		Driver string `yaml:"driver"`
		DSN    string `yaml:"dsn"`
	} `yaml:"store"`
	Channels []struct {
		Name string `yaml:"name"`
		Type string `yaml:"type"`
		SMTP *struct {
			Host     string `yaml:"host"`
			Port     int    `yaml:"port"`
			Username string `yaml:"username"`
			Password string `yaml:"password"`
			From     string `yaml:"from"`
			UseTLS   bool   `yaml:"use_tls"`
		} `yaml:"smtp,omitempty"`
		Slack *struct {
			WebhookURL string `yaml:"webhook_url"`
		} `yaml:"slack,omitempty"`
		Webhook *struct {
			URL     string            `yaml:"url"`
			Headers map[string]string `yaml:"headers"`
		} `yaml:"webhook,omitempty"`
		PagerDuty *struct {
			RoutingKey string `yaml:"routing_key"`
			EventsURL  string `yaml:"events_url,omitempty"`
		} `yaml:"pagerduty,omitempty"`
		Teams *struct {
			WebhookURL string `yaml:"webhook_url"`
		} `yaml:"teams,omitempty"`
		Discord *struct {
			WebhookURL string `yaml:"webhook_url"`
		} `yaml:"discord,omitempty"`
		// SMS credentials never go in YAML — only the From number
		// (and an optional API-base override for testing). AccountSID
		// + AuthToken are read from SIGNALWATCH_TWILIO_* env vars
		// when the channel is constructed.
		SMS *struct {
			FromNumber string `yaml:"from_number"`
			APIBase    string `yaml:"api_base,omitempty"`
		} `yaml:"sms,omitempty"`
	} `yaml:"channels"`
	// Retention runs an in-process pruner that deletes resolved
	// incidents (and cascades their notifications + sub-states)
	// older than Window. An optional archive sink keeps a copy of
	// each deleted incident before the row goes away. See
	// docs/RETENTION.md for tuning guidance.
	Retention *struct {
		Window   string `yaml:"window"`             // "90d" / "168h" / "0s" disables
		Interval string `yaml:"interval,omitempty"` // defaults to 1h
		Archive  *struct {
			Type string `yaml:"type"`          // "json" | "webhook"
			Dir  string `yaml:"dir,omitempty"` // for type=json
			URL  string `yaml:"url,omitempty"` // for type=webhook
		} `yaml:"archive,omitempty"`
	} `yaml:"retention,omitempty"`
	Inputs struct {
		Event struct {
			Name string `yaml:"name"`
		} `yaml:"event"`
		Scrape []scrape.Target `yaml:"scrape"`
	} `yaml:"inputs"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "signalwatch:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	if cfg.Store.Driver != "sqlite" && cfg.Store.Driver != "" {
		return fmt.Errorf("store driver %q not supported in v0.1 (only sqlite)", cfg.Store.Driver)
	}
	dsn := cfg.Store.DSN
	if dsn == "" {
		dsn = "file:signalwatch.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	}

	st, err := sqlite.Open(dsn)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	channels := map[string]channel.Channel{}
	for _, c := range cfg.Channels {
		if c.Name == "" {
			return errors.New("channel: name required")
		}
		switch c.Type {
		case "smtp":
			if c.SMTP == nil {
				return fmt.Errorf("channel %s: smtp section required", c.Name)
			}
			channels[c.Name] = smtp.New(smtp.Config{
				Name: c.Name, Host: c.SMTP.Host, Port: c.SMTP.Port,
				Username: c.SMTP.Username, Password: c.SMTP.Password,
				From: c.SMTP.From, UseTLS: c.SMTP.UseTLS,
			})
		case "slack":
			if c.Slack == nil {
				return fmt.Errorf("channel %s: slack section required", c.Name)
			}
			channels[c.Name] = slack.New(slack.Config{Name: c.Name, WebhookURL: c.Slack.WebhookURL})
		case "webhook":
			cfgw := webhook.Config{Name: c.Name}
			if c.Webhook != nil {
				cfgw.URL = c.Webhook.URL
				cfgw.Headers = c.Webhook.Headers
			}
			channels[c.Name] = webhook.New(cfgw)
		case "pagerduty":
			if c.PagerDuty == nil {
				return fmt.Errorf("channel %s: pagerduty section required", c.Name)
			}
			channels[c.Name] = pagerduty.New(pagerduty.Config{
				Name:       c.Name,
				RoutingKey: c.PagerDuty.RoutingKey,
				EventsURL:  c.PagerDuty.EventsURL,
			})
		case "teams":
			if c.Teams == nil {
				return fmt.Errorf("channel %s: teams section required", c.Name)
			}
			channels[c.Name] = teams.New(teams.Config{Name: c.Name, WebhookURL: c.Teams.WebhookURL})
		case "discord":
			if c.Discord == nil {
				return fmt.Errorf("channel %s: discord section required", c.Name)
			}
			channels[c.Name] = discord.New(discord.Config{Name: c.Name, WebhookURL: c.Discord.WebhookURL})
		case "sms":
			if c.SMS == nil {
				return fmt.Errorf("channel %s: sms section required", c.Name)
			}
			channels[c.Name] = sms.New(sms.Config{
				Name:       c.Name,
				FromNumber: c.SMS.FromNumber,
				APIBase:    c.SMS.APIBase,
				// AccountSID + AuthToken come from env vars inside New().
			})
		default:
			return fmt.Errorf("channel %s: unknown type %q", c.Name, c.Type)
		}
	}

	eventInput := event.New(cfg.Inputs.Event.Name)
	inputs := []input.Input{eventInput}
	if len(cfg.Inputs.Scrape) > 0 {
		inputs = append(inputs, scrape.New(cfg.Inputs.Scrape))
	}

	sqlReg := sqlquery.NewRegistry()
	// The store's own DB is registered as the "default" datasource so
	// SQLReturnsRows rules can query it without extra config. Production
	// users will typically register their own datasources programmatically.
	sqlReg.Register("default", st.DB())

	eng, err := engine.New(engine.Options{
		Store:          st,
		Channels:       channels,
		Inputs:         inputs,
		EventInput:     eventInput,
		SQLDatasources: sqlReg,
		Logger:         logger,
	})
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// OpenTelemetry tracing is opt-in via OTEL_TRACES_EXPORTER; when unset
	// the SDK stays no-op. shutdownOtel always flushes, even on Setup error.
	shutdownOtel, err := observability.Setup(ctx, observability.SetupOptions{})
	if err != nil {
		return fmt.Errorf("observability: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownOtel(shutdownCtx); err != nil {
			logger.Warn("observability.shutdown_error", "err", err)
		}
	}()

	if err := eng.Start(ctx); err != nil {
		return fmt.Errorf("engine start: %w", err)
	}

	if cfg.Retention != nil {
		pruner, err := buildPruner(cfg.Retention, st, logger)
		if err != nil {
			return fmt.Errorf("retention: %w", err)
		}
		if pruner != nil {
			go func() {
				if err := pruner.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
					logger.Warn("retention.start_error", "err", err)
				}
			}()
			logger.Info("retention.enabled",
				"window", cfg.Retention.Window,
				"archive", retentionArchiveType(cfg.Retention))
		}
	}

	mux := http.NewServeMux()
	// Auth wiring. signalwatch supports two mechanisms simultaneously:
	//   1. Legacy single shared token via SIGNALWATCH_API_TOKEN — back-
	//      compat with v0.1-0.3 deployments. Treated as admin scope.
	//   2. Per-user tokens stored in the api_tokens table — managed via
	//      POST /v1/auth/tokens. Each token carries a scope set.
	// Either or both can be active; auth is enforced whenever ANY
	// mechanism is configured. Empty env var + token store still mounted
	// (the store always exists once Migrate has run) means: if any token
	// row exists in the DB, auth is required.
	apiToken := os.Getenv("SIGNALWATCH_API_TOKEN")
	mountOpts := []api.MountOption{
		api.WithAPIToken(apiToken),
		api.WithTokenStore(st.APITokens()),
		api.WithAuthLogger(logger),
	}
	api.Mount(mux, eng, ui.Handler(), mountOpts...)
	switch {
	case apiToken != "":
		logger.Info("signalwatch.auth_enabled", "scheme", "shared-token+per-user")
	default:
		logger.Info("signalwatch.auth_enabled", "scheme", "per-user-only")
	}

	addr := cfg.HTTP.Addr
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	// otelhttp wraps the mux with span creation + W3C trace-context
	// propagation. The wrapper is harmless when OTel is in no-op mode.
	tracedMux := otelhttp.NewHandler(mux, "signalwatch.http",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)
	srv := &http.Server{
		Addr:              addr,
		Handler:           withLogging(logger, tracedMux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverDone := make(chan error, 1)
	go func() {
		logger.Info("signalwatch.listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverDone <- err
			return
		}
		serverDone <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("signalwatch.shutting_down")
	case err := <-serverDone:
		if err != nil {
			return err
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	_ = eng.Close()
	return nil
}

func loadConfig(path string) (*config, error) {
	// #nosec G304 -- path is a CLI flag set by the operator running the
	// binary, not user input crossing a trust boundary.
	body, err := os.ReadFile(path)
	if err != nil {
		// Missing config file is OK — start with defaults.
		if errors.Is(err, os.ErrNotExist) {
			return &config{}, nil
		}
		return nil, err
	}
	var cfg config
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func withLogging(logger *slog.Logger, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h.ServeHTTP(w, r)
		logger.Info("http", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}

// buildPruner constructs a retention.Pruner from the YAML config.
// Returns (nil, nil) when the configured window is 0/empty so
// operators can include the section but disable it at runtime.
func buildPruner(
	rc *struct {
		Window   string `yaml:"window"`
		Interval string `yaml:"interval,omitempty"`
		Archive  *struct {
			Type string `yaml:"type"`
			Dir  string `yaml:"dir,omitempty"`
			URL  string `yaml:"url,omitempty"`
		} `yaml:"archive,omitempty"`
	},
	st *sqlite.Store,
	logger *slog.Logger,
) (*retention.Pruner, error) {
	window, err := parseFlexDuration(rc.Window)
	if err != nil {
		return nil, fmt.Errorf("window: %w", err)
	}
	if window <= 0 {
		return nil, nil
	}
	var interval time.Duration
	if rc.Interval != "" {
		interval, err = parseFlexDuration(rc.Interval)
		if err != nil {
			return nil, fmt.Errorf("interval: %w", err)
		}
	}
	var arc retention.Archiver
	if rc.Archive != nil {
		switch rc.Archive.Type {
		case "json":
			if rc.Archive.Dir == "" {
				return nil, errors.New("archive.dir required for type=json")
			}
			arc = &retention.JSONFileArchiver{Dir: rc.Archive.Dir}
		case "webhook":
			if rc.Archive.URL == "" {
				return nil, errors.New("archive.url required for type=webhook")
			}
			arc = &retention.WebhookArchiver{URL: rc.Archive.URL}
		case "":
		default:
			return nil, fmt.Errorf("archive.type %q not supported (use json, webhook, or omit)", rc.Archive.Type)
		}
	}
	return retention.New(retention.Config{
		Store:    st,
		Window:   window,
		Interval: interval,
		Archiver: arc,
		Logger:   logger,
	})
}

func retentionArchiveType(rc *struct {
	Window   string `yaml:"window"`
	Interval string `yaml:"interval,omitempty"`
	Archive  *struct {
		Type string `yaml:"type"`
		Dir  string `yaml:"dir,omitempty"`
		URL  string `yaml:"url,omitempty"`
	} `yaml:"archive,omitempty"`
}) string {
	if rc.Archive == nil {
		return "none"
	}
	return rc.Archive.Type
}

// parseFlexDuration accepts everything time.ParseDuration accepts
// plus "Xd" (days) and "Xw" (weeks). Mirrored from
// internal/rule/expression.go so the YAML reader doesn't need a
// dependency on the rule package.
func parseFlexDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if n := len(s); n > 1 {
		last := s[n-1]
		if last == 'd' || last == 'w' {
			head := s[:n-1]
			x, err := strconv.ParseFloat(head, 64)
			if err != nil || x < 0 {
				return 0, fmt.Errorf("invalid duration %q", s)
			}
			unit := time.Hour * 24
			if last == 'w' {
				unit *= 7
			}
			return time.Duration(x * float64(unit)), nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("duration %q must be non-negative", s)
	}
	return d, nil
}
