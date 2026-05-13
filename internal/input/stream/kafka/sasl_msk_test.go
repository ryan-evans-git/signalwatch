package kafka

// White-box tests for the MSK SASL mechanism. Lives in-package so the
// tokenProvider type alias and the unexported state machine are
// directly reachable.

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMSKIAMMechanism_Name(t *testing.T) {
	m := newMSKIAMMechanism("us-east-1", nil)
	if got := m.Name(); got != MSKIAMMechanismName {
		t.Fatalf("Name: want %q, got %q", MSKIAMMechanismName, got)
	}
}

func TestMSKIAMMechanism_StartReturnsToken(t *testing.T) {
	called := 0
	provider := tokenProvider(func(_ context.Context, region string) (string, error) {
		called++
		if region != "us-west-2" {
			t.Errorf("region passed to signer: want us-west-2, got %q", region)
		}
		return "signed-token-payload", nil
	})
	m := newMSKIAMMechanism("us-west-2", provider)
	sm, ir, err := m.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if string(ir) != "signed-token-payload" {
		t.Errorf("initial response: want signed-token-payload, got %q", ir)
	}
	if sm == nil {
		t.Fatalf("state machine is nil")
	}
	if called != 1 {
		t.Errorf("provider called %d times, want 1", called)
	}
}

func TestMSKIAMMechanism_StartPropagatesProviderError(t *testing.T) {
	want := errors.New("synthetic IMDS failure")
	m := newMSKIAMMechanism("us-east-1", func(_ context.Context, _ string) (string, error) {
		return "", want
	})
	_, _, err := m.Start(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("want underlying error, got %v", err)
	}
}

func TestMSKIAMMechanism_State_TerminatesImmediately(t *testing.T) {
	// MSK IAM is a single-round-trip exchange. Next must return
	// (done=true, nil, nil) on the first call.
	m := newMSKIAMMechanism("us-east-1", func(_ context.Context, _ string) (string, error) {
		return "tok", nil
	})
	sm, _, err := m.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	done, resp, err := sm.Next(context.Background(), []byte("server-challenge"))
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !done {
		t.Errorf("want done=true on first Next")
	}
	if resp != nil {
		t.Errorf("response: want nil, got %v", resp)
	}
}

func TestNewMSKIAMMechanism_NilProviderFallsBackToDefault(t *testing.T) {
	// When provider is nil, defaultTokenProvider is wired in. We
	// can't easily call the default (it dials AWS), but we can check
	// the field is non-nil so Start won't dereference nil.
	m := newMSKIAMMechanism("us-east-1", nil)
	if m.provider == nil {
		t.Fatalf("provider should fall back to defaultTokenProvider")
	}
}

func TestBuildMSKDialer_WiresMechanismTLSAndTimeout(t *testing.T) {
	provider := tokenProvider(func(_ context.Context, _ string) (string, error) {
		return "t", nil
	})
	d := buildMSKDialer("eu-west-1", provider, 7*time.Second)
	if d.SASLMechanism == nil {
		t.Fatalf("dialer missing SASLMechanism")
	}
	if got := d.SASLMechanism.Name(); got != MSKIAMMechanismName {
		t.Errorf("mech name: %q", got)
	}
	if d.TLS == nil {
		t.Fatalf("dialer should set TLS for MSK")
	}
	if d.TLS.MinVersion != 0x0303 { // tls.VersionTLS12
		t.Errorf("MinVersion: want TLS 1.2 (0x0303), got 0x%04x", d.TLS.MinVersion)
	}
	if d.Timeout != 7*time.Second {
		t.Errorf("Timeout: want %v, got %v", 7*time.Second, d.Timeout)
	}
	if !d.DualStack {
		t.Errorf("DualStack should be true for IPv4+IPv6 broker DNS")
	}
}

func TestBuildMSKDialer_ZeroTimeoutLeavesUnset(t *testing.T) {
	d := buildMSKDialer("us-east-1", func(_ context.Context, _ string) (string, error) { return "t", nil }, 0)
	if d.Timeout != 0 {
		t.Errorf("zero timeout should pass through unchanged, got %v", d.Timeout)
	}
}
