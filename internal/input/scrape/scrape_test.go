package scrape_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/input/scrape"
)

func TestName(t *testing.T) {
	if got := scrape.New(nil).Name(); got != "scrape" {
		t.Fatalf("Name: want scrape, got %q", got)
	}
}

// scrapeServer returns a *httptest.Server that responds with the given
// body and status, and counts the number of GETs it sees.
type scrapeServer struct {
	srv  *httptest.Server
	hits atomic.Int64
}

func newScrapeServer(t *testing.T, status int, body string) *scrapeServer {
	t.Helper()
	s := &scrapeServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.hits.Add(1)
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// Start should fan out one goroutine per target, tick each at its configured
// Interval, and emit one EvaluationRecord per successful scrape.
func TestStart_EmitsRecordsAtInterval(t *testing.T) {
	srv := newScrapeServer(t, http.StatusOK, `{"value": 42}`)

	in := scrape.New([]scrape.Target{{
		Name:     "metrics",
		URL:      srv.srv.URL,
		Interval: 50 * time.Millisecond,
		Field:    "value",
	}})

	sink := make(chan input.EvaluationRecord, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startErr := make(chan error, 1)
	go func() { startErr <- in.Start(ctx, sink) }()

	select {
	case rec := <-sink:
		if rec.InputRef != "metrics" {
			t.Fatalf("InputRef: want metrics, got %q", rec.InputRef)
		}
		if v, ok := rec.Record["value"].(float64); !ok || v != 42 {
			t.Fatalf("Record value: want 42 (float64), got %T %v", rec.Record["value"], rec.Record["value"])
		}
		if rec.Record["source"] != "metrics" {
			t.Errorf("source: want metrics, got %v", rec.Record["source"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("never received a scrape record")
	}

	cancel()
	select {
	case err := <-startErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start: want context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
}

// A target with Interval <= 0 should be defaulted to 30s. We don't want
// to wait 30s in a test — assert by observing that the scrape DOESN'T
// fire in 200ms.
func TestStart_DefaultsZeroIntervalAway(t *testing.T) {
	srv := newScrapeServer(t, http.StatusOK, `{"value": 1}`)
	in := scrape.New([]scrape.Target{{
		Name: "metrics", URL: srv.srv.URL, Interval: 0, Field: "value",
	}})

	sink := make(chan input.EvaluationRecord, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go in.Start(ctx, sink)

	select {
	case <-sink:
		t.Fatal("zero-interval target should default to 30s — should NOT have ticked in 200ms")
	case <-ctx.Done():
		// Good — no scrape emitted within the short window.
	}
}

// Failure modes: bad URL, non-2xx status, malformed JSON, missing field.
// Each branch returns an error from scrape() which is swallowed by the
// runTarget loop, so the public observation is "no record emitted within
// the deadline." We assert that on each variant.
func TestRunTarget_ErrorPathsSwallowedSilently(t *testing.T) {
	cases := []struct {
		name string
		make func(t *testing.T) scrape.Target
	}{
		{
			"bad URL (NewRequest fails)",
			func(*testing.T) scrape.Target {
				return scrape.Target{Name: "x", URL: "http://\x7f", Interval: 30 * time.Millisecond, Field: "value"}
			},
		},
		{
			"non-2xx status",
			func(t *testing.T) scrape.Target {
				s := newScrapeServer(t, http.StatusInternalServerError, `{"value": 1}`)
				return scrape.Target{Name: "x", URL: s.srv.URL, Interval: 30 * time.Millisecond, Field: "value"}
			},
		},
		{
			"malformed JSON",
			func(t *testing.T) scrape.Target {
				s := newScrapeServer(t, http.StatusOK, `{not json}`)
				return scrape.Target{Name: "x", URL: s.srv.URL, Interval: 30 * time.Millisecond, Field: "value"}
			},
		},
		{
			"missing field",
			func(t *testing.T) scrape.Target {
				s := newScrapeServer(t, http.StatusOK, `{"other": 1}`)
				return scrape.Target{Name: "x", URL: s.srv.URL, Interval: 30 * time.Millisecond, Field: "value"}
			},
		},
		{
			"transport error",
			func(*testing.T) scrape.Target {
				// 127.0.0.1:1 — unlikely to be listening; Do() errors.
				return scrape.Target{Name: "x", URL: "http://127.0.0.1:1", Interval: 30 * time.Millisecond, Field: "value"}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := scrape.New([]scrape.Target{tc.make(t)})
			sink := make(chan input.EvaluationRecord, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			go in.Start(ctx, sink)

			select {
			case <-sink:
				t.Fatalf("%s: expected no record on error path", tc.name)
			case <-ctx.Done():
				// expected
			}
		})
	}
}

// Field defaults to "value" when Target.Field is empty.
func TestScrape_FieldDefaultsToValue(t *testing.T) {
	srv := newScrapeServer(t, http.StatusOK, `{"value": 7}`)
	in := scrape.New([]scrape.Target{{
		Name: "metrics", URL: srv.srv.URL, Interval: 30 * time.Millisecond,
		// Field intentionally empty
	}})

	sink := make(chan input.EvaluationRecord, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go in.Start(ctx, sink)

	select {
	case rec := <-sink:
		if rec.Record["value"].(float64) != 7 {
			t.Fatalf("want 7, got %v", rec.Record["value"])
		}
	case <-ctx.Done():
		t.Fatal("never received default-field scrape record")
	}
}
