package retention

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/subscriber"
)

// JSONFileArchiver appends one JSONL line per archived incident to a
// daily-rotated file. The file name is `incidents-YYYY-MM-DD.jsonl`
// under the configured Dir. Survives restarts; new lines are
// O_APPEND'd so multiple processes won't collide on the same line.
type JSONFileArchiver struct {
	// Dir is the directory to write archive files into. Created if
	// it doesn't exist. Required.
	Dir string
	// Now overrides time.Now for tests.
	Now func() time.Time

	mu       sync.Mutex
	openFile *os.File
	openDate string
}

func (a *JSONFileArchiver) Archive(_ context.Context, inc *subscriber.Incident, notifs []*subscriber.Notification) error {
	if a.Dir == "" {
		return errors.New("retention: JSONFileArchiver.Dir required")
	}
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	date := now().UTC().Format("2006-01-02")

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.openDate != date {
		if a.openFile != nil {
			_ = a.openFile.Close()
		}
		if err := os.MkdirAll(a.Dir, 0o755); err != nil {
			return fmt.Errorf("retention: mkdir: %w", err)
		}
		path := filepath.Join(a.Dir, "incidents-"+date+".jsonl")
		// #nosec G302 -- archive files are intentionally world-
		// readable on shared hosts; operators who want stricter
		// perms can set a umask before launching the binary.
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("retention: open archive: %w", err)
		}
		a.openFile = f
		a.openDate = date
	}

	body, err := json.Marshal(archiveEntry{Incident: inc, Notifications: notifs, ArchivedAt: now().UTC()})
	if err != nil {
		return fmt.Errorf("retention: marshal: %w", err)
	}
	body = append(body, '\n')
	if _, err := a.openFile.Write(body); err != nil {
		return fmt.Errorf("retention: write: %w", err)
	}
	return nil
}

// Close flushes the current file. Safe to call from shutdown handlers.
func (a *JSONFileArchiver) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.openFile != nil {
		err := a.openFile.Close()
		a.openFile = nil
		a.openDate = ""
		return err
	}
	return nil
}

// WebhookArchiver POSTs the JSON payload to a configured URL.
// HTTP 2xx counts as success; other statuses (or transport errors)
// return an error so the pruner can log + retry on the next tick.
type WebhookArchiver struct {
	URL        string
	HTTPClient *http.Client
}

func (a *WebhookArchiver) Archive(ctx context.Context, inc *subscriber.Incident, notifs []*subscriber.Notification) error {
	if a.URL == "" {
		return errors.New("retention: WebhookArchiver.URL required")
	}
	client := a.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	body, err := json.Marshal(archiveEntry{Incident: inc, Notifications: notifs, ArchivedAt: time.Now().UTC()})
	if err != nil {
		return fmt.Errorf("retention: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("retention: webhook: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("retention: webhook status %d", resp.StatusCode)
	}
	return nil
}

// archiveEntry is the wire shape for both archive sinks. ArchivedAt
// is a server-clock timestamp; consumers should treat the Incident's
// own timestamps as authoritative for incident timing.
type archiveEntry struct {
	Incident      *subscriber.Incident       `json:"incident"`
	Notifications []*subscriber.Notification `json:"notifications"`
	ArchivedAt    time.Time                  `json:"archived_at"`
}
