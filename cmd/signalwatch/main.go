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
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ryan-evans-git/signalwatch/engine"
	"github.com/ryan-evans-git/signalwatch/internal/api"
	"github.com/ryan-evans-git/signalwatch/internal/channel"
	"github.com/ryan-evans-git/signalwatch/internal/channel/slack"
	"github.com/ryan-evans-git/signalwatch/internal/channel/smtp"
	"github.com/ryan-evans-git/signalwatch/internal/channel/webhook"
	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/input/event"
	"github.com/ryan-evans-git/signalwatch/internal/input/scrape"
	"github.com/ryan-evans-git/signalwatch/internal/input/sqlquery"
	"github.com/ryan-evans-git/signalwatch/internal/store/sqlite"
	"github.com/ryan-evans-git/signalwatch/internal/ui"
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
		Name    string `yaml:"name"`
		Type    string `yaml:"type"`
		SMTP    *struct {
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
	} `yaml:"channels"`
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

	if err := eng.Start(ctx); err != nil {
		return fmt.Errorf("engine start: %w", err)
	}

	mux := http.NewServeMux()
	api.Mount(mux, eng, ui.Handler())

	addr := cfg.HTTP.Addr
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           withLogging(logger, mux),
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
