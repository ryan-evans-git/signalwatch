package api_test

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// helper: seed two rules with incidents on each. Returns f for the
// caller to use.
func seedIncidentsTwoRules(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)

	// Two rules.
	for _, id := range []string{"rule-a", "rule-b"} {
		resp := f.do(t, http.MethodPost, "/v1/rules", sampleRulePayload(id))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST rules %s: %d %s", id, resp.StatusCode, resp.Body)
		}
	}
	// Two events for rule-a, one for rule-b. Each event matches the
	// pattern_match condition on `level contains ERROR`.
	for i := 0; i < 2; i++ {
		resp := f.do(t, http.MethodPost, "/v1/events", map[string]any{
			"input_ref": "events",
			"record":    map[string]any{"level": "ERROR", "host": "web-a"},
		})
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("event a-%d: %d", i, resp.StatusCode)
		}
		// Small wait so triggered_at timestamps differ.
		time.Sleep(20 * time.Millisecond)
	}
	// rule-b event — but wait: both rules share input_ref="events" +
	// the same pattern. Each ERROR event triggers both. So
	// rule-b also gets 2 incidents.
	// Adjust: we don't need exactly disjoint counts, just to confirm
	// the filter narrows correctly.

	// Wait briefly for dispatcher to persist incidents.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		resp := f.get(t, "/v1/incidents")
		if resp.StatusCode == http.StatusOK {
			var rows []map[string]any
			_ = json.Unmarshal(resp.Body, &rows)
			if len(rows) >= 1 {
				return f
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("incidents never appeared")
	return nil
}

func TestIncidents_ListFiltersByRuleID(t *testing.T) {
	f := seedIncidentsTwoRules(t)

	// Get all incidents and split by rule_id manually for sanity.
	allResp := f.get(t, "/v1/incidents")
	if allResp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d", allResp.StatusCode)
	}
	var all []map[string]any
	_ = json.Unmarshal(allResp.Body, &all)

	// Filter
	aResp := f.get(t, "/v1/incidents?rule_id=rule-a")
	if aResp.StatusCode != http.StatusOK {
		t.Fatalf("filter: %d %s", aResp.StatusCode, aResp.Body)
	}
	var aOnly []map[string]any
	_ = json.Unmarshal(aResp.Body, &aOnly)

	if len(aOnly) == 0 {
		t.Fatalf("filtered list returned 0 incidents")
	}
	for _, inc := range aOnly {
		if inc["rule_id"] != "rule-a" {
			t.Errorf("rule_id filter leaked: %v", inc)
		}
	}
	// The filter should be a strict subset of "all".
	if len(aOnly) > len(all) {
		t.Errorf("filtered > all: %d > %d", len(aOnly), len(all))
	}
}

// ---- export: format gating ----

func TestExport_RejectsUnknownFormat(t *testing.T) {
	f := seedIncidentsTwoRules(t)
	resp := f.get(t, "/v1/incidents/export?format=xml")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("format=xml: want 400, got %d", resp.StatusCode)
	}
}

func TestExport_RejectsBadSince(t *testing.T) {
	f := seedIncidentsTwoRules(t)
	resp := f.get(t, "/v1/incidents/export?since=tomorrow")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("since=tomorrow: want 400, got %d", resp.StatusCode)
	}
}

func TestExport_RejectsNegativeSinceDuration(t *testing.T) {
	f := seedIncidentsTwoRules(t)
	resp := f.get(t, "/v1/incidents/export?since=-5m")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("since=-5m: want 400, got %d", resp.StatusCode)
	}
}

// ---- export: JSON ----

func TestExport_JSONDefault(t *testing.T) {
	f := seedIncidentsTwoRules(t)
	resp := f.get(t, "/v1/incidents/export")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export: %d", resp.StatusCode)
	}
	var rows []map[string]any
	if err := json.Unmarshal(resp.Body, &rows); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if len(rows) == 0 {
		t.Errorf("expected at least one incident")
	}
}

func TestExport_JSONFormatExplicit(t *testing.T) {
	f := seedIncidentsTwoRules(t)
	resp := f.get(t, "/v1/incidents/export?format=json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("format=json: %d", resp.StatusCode)
	}
}

func TestExport_JSONWithRuleIDFilter(t *testing.T) {
	f := seedIncidentsTwoRules(t)
	resp := f.get(t, "/v1/incidents/export?format=json&rule_id=rule-a")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("filtered export: %d %s", resp.StatusCode, resp.Body)
	}
	var rows []map[string]any
	_ = json.Unmarshal(resp.Body, &rows)
	for _, r := range rows {
		if r["rule_id"] != "rule-a" {
			t.Errorf("filter leak: %v", r)
		}
	}
}

func TestExport_JSONSinceRFC3339(t *testing.T) {
	f := seedIncidentsTwoRules(t)
	// Future timestamp should yield empty rows.
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	resp := f.get(t, "/v1/incidents/export?since="+url.QueryEscape(future))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("since-future: %d", resp.StatusCode)
	}
	body := strings.TrimSpace(string(resp.Body))
	if body != "null" && body != "[]" {
		t.Errorf("future since should yield empty array, got %s", body)
	}
}

func TestExport_JSONSinceDuration(t *testing.T) {
	f := seedIncidentsTwoRules(t)
	// "1h" means everything from the past hour — should include all
	// seeded incidents.
	resp := f.get(t, "/v1/incidents/export?since=1h")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("since-duration: %d", resp.StatusCode)
	}
}

// ---- export: CSV ----

func TestExport_CSV(t *testing.T) {
	f := seedIncidentsTwoRules(t)
	// Need headers, so bypass the fixture helper.
	resp, err := http.Get(f.srv.URL + "/v1/incidents/export?format=csv")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("csv: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/csv") {
		t.Errorf("Content-Type: want text/csv, got %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "incidents.csv") {
		t.Errorf("Content-Disposition should reference incidents.csv: %q", cd)
	}
	rdr := csv.NewReader(resp.Body)
	rows, err := rdr.ReadAll()
	if err != nil {
		t.Fatalf("csv parse: %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("want header + at least one row, got %d", len(rows))
	}
	want := []string{"id", "rule_id", "triggered_at", "resolved_at", "last_value"}
	for i, h := range want {
		if rows[0][i] != h {
			t.Errorf("header col %d: want %s, got %s", i, h, rows[0][i])
		}
	}
}
