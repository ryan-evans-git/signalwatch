package rule

import (
	"strings"
	"testing"
	"time"
)

// goodRule returns a Rule that passes Validate, so each test can mutate one
// field at a time to exercise a single validation branch.
func goodRule() *Rule {
	return &Rule{
		Name:      "cpu high",
		InputRef:  "events",
		Severity:  SeverityWarning,
		Condition: Threshold{Field: "cpu", Op: OpGT, Value: 90},
	}
}

func TestRuleValidate_OK(t *testing.T) {
	if err := goodRule().Validate(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestRuleValidate_MissingFields(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Rule)
		wantSub string
	}{
		{"empty name", func(r *Rule) { r.Name = "" }, "name is required"},
		{"empty input_ref", func(r *Rule) { r.InputRef = "" }, "input_ref is required"},
		{"nil condition", func(r *Rule) { r.Condition = nil }, "condition is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := goodRule()
			tc.mutate(r)
			err := r.Validate()
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q missing substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestRuleValidate_ScheduledRequiresPositiveSchedule(t *testing.T) {
	// WindowAggregate is a Mode==Scheduled condition.
	scheduled := WindowAggregate{Field: "v", Agg: AggAvg, Window: time.Minute, Op: OpLT, Value: 5}

	t.Run("zero schedule is rejected", func(t *testing.T) {
		r := goodRule()
		r.Condition = scheduled
		r.Schedule = 0
		if err := r.Validate(); err == nil ||
			!strings.Contains(err.Error(), "scheduled rules require a positive schedule duration") {
			t.Fatalf("want positive-schedule error, got %v", err)
		}
	})

	t.Run("negative schedule is rejected", func(t *testing.T) {
		r := goodRule()
		r.Condition = scheduled
		r.Schedule = -time.Second
		if err := r.Validate(); err == nil {
			t.Fatalf("want error for negative schedule, got nil")
		}
	})

	t.Run("positive schedule is accepted", func(t *testing.T) {
		r := goodRule()
		r.Condition = scheduled
		r.Schedule = 30 * time.Second
		if err := r.Validate(); err != nil {
			t.Fatalf("want nil error, got %v", err)
		}
	})

	t.Run("push rules ignore schedule", func(t *testing.T) {
		// A push rule should validate even with zero Schedule, because
		// Schedule is only checked for scheduled rules.
		r := goodRule() // Threshold == push
		r.Schedule = 0
		if err := r.Validate(); err != nil {
			t.Fatalf("push rule should not require Schedule, got %v", err)
		}
	})
}

func TestRuleValidate_Severity(t *testing.T) {
	cases := []struct {
		name    string
		sev     Severity
		wantErr bool
	}{
		{"empty is accepted", "", false},
		{"info", SeverityInfo, false},
		{"warning", SeverityWarning, false},
		{"critical", SeverityCritical, false},
		{"unknown rejected", Severity("emergency"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := goodRule()
			r.Severity = tc.sev
			err := r.Validate()
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("want error, got nil")
			case !tc.wantErr && err != nil:
				t.Fatalf("want nil, got %v", err)
			case tc.wantErr && err != nil &&
				!strings.Contains(err.Error(), "invalid severity"):
				t.Fatalf("error %q missing 'invalid severity'", err.Error())
			}
		})
	}
}
