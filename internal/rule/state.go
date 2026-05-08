package rule

import "time"

// Transition describes the result of applying an evaluation outcome to the
// current LiveState. The dispatcher consumes Transitions to decide whether
// to notify subscribers.
type Transition struct {
	RuleID      string
	Prev        State
	Next        State
	Now         time.Time
	TriggeredAt time.Time // start of the current firing cycle (zero if !FIRING)
	IncidentID  string    // assigned by the caller when Prev==OK and Next==FIRING
	LastValue   string
}

// Apply mutates state based on the latest evaluation outcome.
//
// triggered is the boolean result of the rule's compiled condition; value is
// a human-readable summary of the comparison (e.g. "5 < 10"); now is the
// evaluation timestamp; newIncidentID is the id to attach if this evaluation
// opens a new incident — pass "" to leave the IncidentID untouched, or a new
// id from the caller (typically a UUID).
//
// Returns a Transition with Prev/Next set if the rule changed state, or
// Prev==Next if it stayed in the same state.
func Apply(state *LiveState, triggered bool, value string, now time.Time, newIncidentID string) Transition {
	prev := state.State
	state.LastEvalAt = now
	state.LastValue = value

	t := Transition{
		RuleID:    state.RuleID,
		Prev:      prev,
		Now:       now,
		LastValue: value,
	}

	switch {
	case triggered && prev != StateFiring:
		state.State = StateFiring
		state.TriggeredAt = now
		if newIncidentID != "" {
			state.IncidentID = newIncidentID
		}
		t.Next = StateFiring
		t.TriggeredAt = state.TriggeredAt
		t.IncidentID = state.IncidentID
	case triggered && prev == StateFiring:
		t.Next = StateFiring
		t.TriggeredAt = state.TriggeredAt
		t.IncidentID = state.IncidentID
	case !triggered && prev == StateFiring:
		state.State = StateOK
		t.Next = StateOK
		t.IncidentID = state.IncidentID // resolution belongs to the same incident
		state.IncidentID = ""
		state.TriggeredAt = time.Time{}
	default:
		t.Next = StateOK
	}
	return t
}

// IsTransition reports whether t represents a state change (OK->FIRING or
// FIRING->OK), as opposed to staying in the same state.
func (t Transition) IsTransition() bool {
	return t.Prev != t.Next
}

// Opened reports whether t opens a new incident.
func (t Transition) Opened() bool {
	return t.Prev == StateOK && t.Next == StateFiring
}

// Closed reports whether t closes an existing incident.
func (t Transition) Closed() bool {
	return t.Prev == StateFiring && t.Next == StateOK
}
