package subscriber

import (
	"strings"
	"testing"
	"time"
)

func TestSubscriptionValidate_OK(t *testing.T) {
	cases := []struct {
		name string
		sub  Subscription
	}{
		{
			"rule_id only",
			Subscription{SubscriberID: "s1", RuleID: "r1"},
		},
		{
			"label selector only",
			Subscription{SubscriberID: "s1", LabelSelector: map[string]string{"team": "ops"}},
		},
		{
			"with dwell and repeat",
			Subscription{
				SubscriberID:    "s1",
				RuleID:          "r1",
				Dwell:           2 * time.Minute,
				RepeatInterval:  5 * time.Minute,
				NotifyOnResolve: true,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.sub.Validate(); err != nil {
				t.Fatalf("Validate: want nil, got %v", err)
			}
		})
	}
}

func TestSubscriptionValidate_Errors(t *testing.T) {
	cases := []struct {
		name    string
		sub     Subscription
		wantSub string
	}{
		{
			"missing subscriber_id",
			Subscription{RuleID: "r1"},
			"subscriber_id required",
		},
		{
			"no match (rule_id or label_selector)",
			Subscription{SubscriberID: "s1"},
			"rule_id or label_selector required",
		},
		{
			"both match types set",
			Subscription{
				SubscriberID:  "s1",
				RuleID:        "r1",
				LabelSelector: map[string]string{"team": "ops"},
			},
			"only one of rule_id or label_selector may be set",
		},
		{
			"negative dwell",
			Subscription{SubscriberID: "s1", RuleID: "r1", Dwell: -time.Second},
			"dwell and repeat_interval must be >= 0",
		},
		{
			"negative repeat",
			Subscription{SubscriberID: "s1", RuleID: "r1", RepeatInterval: -time.Second},
			"dwell and repeat_interval must be >= 0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.sub.Validate()
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q missing %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// NotificationKind constants exist primarily for type safety; this test
// pins their string values so a typo in the enum gets caught.
func TestNotificationKindValues(t *testing.T) {
	cases := map[NotificationKind]string{
		KindFiring:   "firing",
		KindRepeat:   "repeat",
		KindResolved: "resolved",
	}
	for k, want := range cases {
		if string(k) != want {
			t.Errorf("NotificationKind: want %q got %q", want, k)
		}
	}
}
