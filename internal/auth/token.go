// Package auth defines the per-user API-token primitives shared across the
// store, api, and cmd packages. Tokens are persisted by SHA-256 hash so a
// stolen database row can't be replayed; the raw secret is returned to the
// caller exactly once at issuance.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// Scope is a coarse permission band that gates API routes. We intentionally
// ship only two scopes for v0.4 — full RBAC (per-resource permissions, role
// hierarchies) is a v0.5+ feature. Operators who want finer control today
// can layer a reverse proxy.
type Scope string

const (
	// ScopeAdmin grants every API route (create / update / delete +
	// read).
	ScopeAdmin Scope = "admin"
	// ScopeRead grants GET-only routes; mutating verbs return 403.
	ScopeRead Scope = "read"
)

// ValidScopes is the canonical set of scopes; anything outside this set is
// rejected at issuance time.
var ValidScopes = []Scope{ScopeAdmin, ScopeRead}

// TokenPrefix marks raw tokens so they're recognizable in logs / git
// commits / leaks. The same prefix is required on inbound Authorization
// headers — defense-in-depth against accidentally accepting some other
// system's bearer token.
const TokenPrefix = "sw_"

// rawTokenBytes is the entropy size of the random portion of a token (in
// bytes). 32 bytes ≈ 256 bits, comfortably above brute-force.
const rawTokenBytes = 32

// Token is the persisted shape of an API token. The raw secret is NEVER
// in this struct after issuance; only TokenHash is.
type Token struct {
	ID         string
	Name       string  // human label, e.g. "ci-deploybot"
	TokenHash  string  // sha256 hex of the raw secret
	Scopes     []Scope // at least one
	CreatedAt  time.Time
	ExpiresAt  *time.Time // nil = never expires
	LastUsedAt *time.Time // nil = never observed
	Revoked    bool
}

// GenerateToken returns a fresh raw token and its sha256 hash. The raw
// token is the value to return to the user; the hash is what gets stored
// in api_tokens.token_hash.
func GenerateToken() (raw, hash string, err error) {
	b := make([]byte, rawTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	// base64.RawURLEncoding is URL-safe and unpadded, so the token can
	// flow through query strings or Authorization headers without
	// surprises.
	raw = TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	hash = HashToken(raw)
	return raw, hash, nil
}

// HashToken returns the canonical storage hash for raw. Always uses
// sha256; never call this in a hot loop where a slow hash would matter
// (API auth happens at most O(requests-per-second), so sha256 is fine).
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ValidateScope returns an error if s isn't one of ValidScopes.
func ValidateScope(s Scope) error {
	for _, v := range ValidScopes {
		if v == s {
			return nil
		}
	}
	return errors.New("auth: unknown scope " + string(s))
}

// HasScope reports whether the token grants s. ScopeAdmin satisfies every
// scope check.
func (t *Token) HasScope(s Scope) bool {
	for _, have := range t.Scopes {
		if have == ScopeAdmin || have == s {
			return true
		}
	}
	return false
}

// IsExpired reports whether the token's ExpiresAt is non-nil and in the
// past relative to now. now is injected so tests don't need a fake clock
// at the package level.
func (t *Token) IsExpired(now time.Time) bool {
	return t.ExpiresAt != nil && !now.Before(*t.ExpiresAt)
}

// LooksLikeToken returns true if s plausibly is a signalwatch token
// (right prefix, right length range). Used as an early sniff before
// hitting the database so obviously-bogus headers don't trigger a DB
// lookup.
func LooksLikeToken(s string) bool {
	if !strings.HasPrefix(s, TokenPrefix) {
		return false
	}
	body := s[len(TokenPrefix):]
	// base64-url(32 bytes) → 43 chars; allow a small slack window for
	// future prefix changes.
	return len(body) >= 40 && len(body) <= 60
}
