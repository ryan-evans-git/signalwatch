package auth_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/auth"
)

func TestGenerateToken_Format(t *testing.T) {
	raw, hash, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if !strings.HasPrefix(raw, auth.TokenPrefix) {
		t.Errorf("raw missing prefix: %q", raw)
	}
	if !auth.LooksLikeToken(raw) {
		t.Errorf("LooksLikeToken false on freshly generated raw: %q", raw)
	}
	if len(hash) != 64 {
		t.Errorf("hash length: got %d want 64", len(hash))
	}
	if auth.HashToken(raw) != hash {
		t.Errorf("HashToken not deterministic")
	}
}

func TestGenerateToken_Uniqueness(t *testing.T) {
	seen := map[string]bool{}
	for range [50]int{} {
		raw, _, err := auth.GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken: %v", err)
		}
		if seen[raw] {
			t.Fatal("duplicate raw token generated")
		}
		seen[raw] = true
	}
}

func TestHashToken_DiffersByInput(t *testing.T) {
	a := auth.HashToken("sw_aaa")
	b := auth.HashToken("sw_bbb")
	if a == b {
		t.Fatal("different inputs should hash to different values")
	}
}

func TestValidateScope(t *testing.T) {
	for _, s := range auth.ValidScopes {
		if err := auth.ValidateScope(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	if err := auth.ValidateScope("write"); err == nil {
		t.Error("expected error for unknown scope")
	}
}

func TestHasScope(t *testing.T) {
	tok := &auth.Token{Scopes: []auth.Scope{auth.ScopeRead}}
	if !tok.HasScope(auth.ScopeRead) {
		t.Error("ScopeRead should satisfy itself")
	}
	if tok.HasScope(auth.ScopeAdmin) {
		t.Error("ScopeRead should not satisfy ScopeAdmin")
	}
	admin := &auth.Token{Scopes: []auth.Scope{auth.ScopeAdmin}}
	if !admin.HasScope(auth.ScopeRead) {
		t.Error("ScopeAdmin should satisfy ScopeRead")
	}
}

func TestIsExpired(t *testing.T) {
	now := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	t.Run("nil never expires", func(t *testing.T) {
		tok := &auth.Token{}
		if tok.IsExpired(now) {
			t.Fatal("nil ExpiresAt should never be expired")
		}
	})
	t.Run("past", func(t *testing.T) {
		past := now.Add(-time.Hour)
		tok := &auth.Token{ExpiresAt: &past}
		if !tok.IsExpired(now) {
			t.Fatal("past ExpiresAt should be expired")
		}
	})
	t.Run("future", func(t *testing.T) {
		future := now.Add(time.Hour)
		tok := &auth.Token{ExpiresAt: &future}
		if tok.IsExpired(now) {
			t.Fatal("future ExpiresAt should not be expired")
		}
	})
	t.Run("exactly now", func(t *testing.T) {
		eq := now
		tok := &auth.Token{ExpiresAt: &eq}
		if !tok.IsExpired(now) {
			t.Fatal("ExpiresAt == now should be expired")
		}
	})
}

func TestLooksLikeToken(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"sw_" + strings.Repeat("a", 43), true},
		{"sw_" + strings.Repeat("a", 39), false}, // too short
		{"sw_" + strings.Repeat("a", 61), false}, // too long
		{"bearer_xxx", false},
		{"", false},
		{"sw_", false}, // body empty
	}
	for _, c := range cases {
		if got := auth.LooksLikeToken(c.in); got != c.want {
			t.Errorf("LooksLikeToken(%q) = %v want %v", c.in, got, c.want)
		}
	}
}
