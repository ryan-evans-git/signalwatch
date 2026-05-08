// Package scrape periodically GETs JSON endpoints, extracts a numeric value
// at a configured top-level key, and emits records the engine can evaluate.
package scrape

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/rule"
)

// Target describes a single scrape endpoint.
type Target struct {
	Name     string // becomes the input ref
	URL      string
	Interval time.Duration
	Field    string // top-level JSON key whose numeric value becomes record["value"]
}

// Input is a scrape input that runs the configured targets on intervals.
type Input struct {
	targets []Target
	client  *http.Client
}

func New(targets []Target) *Input {
	return &Input{targets: targets, client: &http.Client{Timeout: 10 * time.Second}}
}

func (i *Input) Name() string { return "scrape" }

func (i *Input) Start(ctx context.Context, sink chan<- input.EvaluationRecord) error {
	for _, t := range i.targets {
		if t.Interval <= 0 {
			t.Interval = 30 * time.Second
		}
		go i.runTarget(ctx, t, sink)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (i *Input) runTarget(ctx context.Context, t Target, sink chan<- input.EvaluationRecord) {
	ticker := time.NewTicker(t.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rec, err := i.scrape(ctx, t)
			if err != nil {
				continue
			}
			select {
			case sink <- input.EvaluationRecord{InputRef: t.Name, When: time.Now(), Record: rec}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (i *Input) scrape(ctx context.Context, t Target) (rule.Record, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := i.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("scrape: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	field := t.Field
	if field == "" {
		field = "value"
	}
	v, ok := raw[field]
	if !ok {
		return nil, fmt.Errorf("scrape: field %q missing", field)
	}
	return rule.Record{"value": v, "source": t.Name}, nil
}
