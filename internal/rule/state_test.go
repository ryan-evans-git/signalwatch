package rule

import (
	"testing"
	"time"
)

// fixedNow is a stable evaluation timestamp used across the state tests.
var fixedNow = time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)

func TestApply_OKStaysOK(t *testing.T) {
	ls := &LiveState{RuleID: "r1", State: StateOK}
	tr := Apply(ls, false, "1 < 10", fixedNow, "ignored")

	if ls.State != StateOK {
		t.Fatalf("state: want OK, got %q", ls.State)
	}
	if !ls.TriggeredAt.IsZero() {
		t.Fatalf("TriggeredAt should remain zero, got %v", ls.TriggeredAt)
	}
	if ls.LastEvalAt != fixedNow {
		t.Fatalf("LastEvalAt: want %v, got %v", fixedNow, ls.LastEvalAt)
	}
	if ls.LastValue != "1 < 10" {
		t.Fatalf("LastValue: want %q, got %q", "1 < 10", ls.LastValue)
	}
	if ls.IncidentID != "" {
		t.Fatalf("IncidentID should remain empty, got %q", ls.IncidentID)
	}
	if tr.Prev != StateOK || tr.Next != StateOK {
		t.Fatalf("transition: want OK->OK, got %q->%q", tr.Prev, tr.Next)
	}
	if tr.IsTransition() {
		t.Fatalf("IsTransition: want false")
	}
	if tr.Opened() || tr.Closed() {
		t.Fatalf("Opened/Closed: want both false")
	}
}

func TestApply_OKToFiringOpensIncident(t *testing.T) {
	ls := &LiveState{RuleID: "r1", State: StateOK}
	tr := Apply(ls, true, "11 > 10", fixedNow, "incident-1")

	if ls.State != StateFiring {
		t.Fatalf("state: want FIRING, got %q", ls.State)
	}
	if ls.TriggeredAt != fixedNow {
		t.Fatalf("TriggeredAt: want %v, got %v", fixedNow, ls.TriggeredAt)
	}
	if ls.IncidentID != "incident-1" {
		t.Fatalf("IncidentID: want incident-1, got %q", ls.IncidentID)
	}
	if tr.Prev != StateOK || tr.Next != StateFiring {
		t.Fatalf("transition: want OK->FIRING, got %q->%q", tr.Prev, tr.Next)
	}
	if tr.TriggeredAt != fixedNow {
		t.Fatalf("Transition.TriggeredAt: want %v, got %v", fixedNow, tr.TriggeredAt)
	}
	if tr.IncidentID != "incident-1" {
		t.Fatalf("Transition.IncidentID: want incident-1, got %q", tr.IncidentID)
	}
	if !tr.IsTransition() || !tr.Opened() || tr.Closed() {
		t.Fatalf("predicates: want IsTransition+Opened, got IsTransition=%v Opened=%v Closed=%v",
			tr.IsTransition(), tr.Opened(), tr.Closed())
	}
}

// Callers may pass "" for newIncidentID when they don't want to mint one
// (e.g. the caller already supplied an id earlier). The state's existing
// IncidentID must not be overwritten with empty.
func TestApply_OKToFiringEmptyIncidentLeavesExisting(t *testing.T) {
	ls := &LiveState{RuleID: "r1", State: StateOK, IncidentID: "carry-over"}
	tr := Apply(ls, true, "v", fixedNow, "")

	if ls.IncidentID != "carry-over" {
		t.Fatalf("IncidentID should be preserved when newIncidentID is empty, got %q", ls.IncidentID)
	}
	if tr.IncidentID != "carry-over" {
		t.Fatalf("Transition.IncidentID: want carry-over, got %q", tr.IncidentID)
	}
}

func TestApply_FiringStaysFiringPreservesIncident(t *testing.T) {
	openedAt := fixedNow.Add(-3 * time.Minute)
	ls := &LiveState{
		RuleID:      "r1",
		State:       StateFiring,
		TriggeredAt: openedAt,
		IncidentID:  "incident-1",
	}
	tr := Apply(ls, true, "v2", fixedNow, "should-be-ignored")

	if ls.State != StateFiring {
		t.Fatalf("state: want FIRING, got %q", ls.State)
	}
	if ls.TriggeredAt != openedAt {
		t.Fatalf("TriggeredAt should not move during continuous firing, got %v want %v", ls.TriggeredAt, openedAt)
	}
	if ls.IncidentID != "incident-1" {
		t.Fatalf("IncidentID: want incident-1, got %q", ls.IncidentID)
	}
	if tr.Prev != StateFiring || tr.Next != StateFiring {
		t.Fatalf("transition: want FIRING->FIRING, got %q->%q", tr.Prev, tr.Next)
	}
	if tr.IsTransition() || tr.Opened() || tr.Closed() {
		t.Fatalf("predicates: all should be false")
	}
	if tr.IncidentID != "incident-1" {
		t.Fatalf("Transition.IncidentID: want incident-1, got %q", tr.IncidentID)
	}
}

func TestApply_FiringToOKClosesIncident(t *testing.T) {
	openedAt := fixedNow.Add(-5 * time.Minute)
	ls := &LiveState{
		RuleID:      "r1",
		State:       StateFiring,
		TriggeredAt: openedAt,
		IncidentID:  "incident-1",
	}
	tr := Apply(ls, false, "9 < 10", fixedNow, "ignored")

	if ls.State != StateOK {
		t.Fatalf("state: want OK, got %q", ls.State)
	}
	if !ls.TriggeredAt.IsZero() {
		t.Fatalf("TriggeredAt should clear on resolve, got %v", ls.TriggeredAt)
	}
	if ls.IncidentID != "" {
		t.Fatalf("IncidentID should clear on resolve, got %q", ls.IncidentID)
	}
	if tr.Prev != StateFiring || tr.Next != StateOK {
		t.Fatalf("transition: want FIRING->OK, got %q->%q", tr.Prev, tr.Next)
	}
	// The resolution belongs to the same incident, so the transition must
	// still carry the closing incident id even though state has cleared it.
	if tr.IncidentID != "incident-1" {
		t.Fatalf("Transition.IncidentID: want incident-1, got %q", tr.IncidentID)
	}
	if !tr.IsTransition() || !tr.Closed() || tr.Opened() {
		t.Fatalf("predicates: want IsTransition+Closed only")
	}
}

func TestApply_AlwaysUpdatesLastEvalAndValue(t *testing.T) {
	cases := []struct {
		name      string
		state     State
		triggered bool
	}{
		{"OK -> OK", StateOK, false},
		{"OK -> FIRING", StateOK, true},
		{"FIRING -> FIRING", StateFiring, true},
		{"FIRING -> OK", StateFiring, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ls := &LiveState{
				RuleID: "r1",
				State:  tc.state,
			}
			if tc.state == StateFiring {
				ls.TriggeredAt = fixedNow.Add(-time.Minute)
				ls.IncidentID = "i1"
			}
			Apply(ls, tc.triggered, "value-X", fixedNow, "new-i")
			if ls.LastEvalAt != fixedNow {
				t.Fatalf("LastEvalAt not updated: want %v got %v", fixedNow, ls.LastEvalAt)
			}
			if ls.LastValue != "value-X" {
				t.Fatalf("LastValue not updated: want %q got %q", "value-X", ls.LastValue)
			}
		})
	}
}

func TestTransitionPredicates(t *testing.T) {
	cases := []struct {
		name             string
		prev, next       State
		wantIsTransition bool
		wantOpened       bool
		wantClosed       bool
	}{
		{"OK->OK", StateOK, StateOK, false, false, false},
		{"OK->FIRING", StateOK, StateFiring, true, true, false},
		{"FIRING->FIRING", StateFiring, StateFiring, false, false, false},
		{"FIRING->OK", StateFiring, StateOK, true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := Transition{Prev: tc.prev, Next: tc.next}
			if got := tr.IsTransition(); got != tc.wantIsTransition {
				t.Errorf("IsTransition: want %v got %v", tc.wantIsTransition, got)
			}
			if got := tr.Opened(); got != tc.wantOpened {
				t.Errorf("Opened: want %v got %v", tc.wantOpened, got)
			}
			if got := tr.Closed(); got != tc.wantClosed {
				t.Errorf("Closed: want %v got %v", tc.wantClosed, got)
			}
		})
	}
}
