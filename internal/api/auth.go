package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/auth"
	"github.com/ryan-evans-git/signalwatch/internal/store"
)

// MountOption customizes a call to Mount. Used for optional config
// (auth, future: rate limits, CORS) so adding parameters doesn't break
// existing callers.
type MountOption func(*mountSettings)

// mountSettings is the internal accumulator that MountOptions write
// into. Zero values mean "off".
type mountSettings struct {
	// apiToken is the legacy single shared bearer token, sourced from
	// SIGNALWATCH_API_TOKEN. Kept for back-compat with v0.1-0.3 single-
	// tenant deployments. When set, a request bearing this exact token
	// is treated as an admin-scoped caller.
	apiToken string
	// tokens is the per-user token store. When non-nil, the auth
	// middleware additionally looks up sha256(Authorization-bearer)
	// against this repo. nil disables per-user tokens.
	tokens store.APITokenRepo
	// now is injected for tests so expiry checks are deterministic.
	now func() time.Time
	// logger receives token-touch errors; defaults to slog.Default().
	logger *slog.Logger
}

// WithAPIToken enables bearer-token auth on every /v1/* route via a
// single shared secret (the v0.1 model). Pass a fresh value from
// SIGNALWATCH_API_TOKEN. Empty disables this back-compat path.
//
// Per-user tokens (added in v0.4) live in the store and are wired up
// via WithTokenStore; you can use them together with WithAPIToken or on
// their own.
func WithAPIToken(token string) MountOption {
	return func(s *mountSettings) {
		s.apiToken = token
	}
}

// WithTokenStore enables per-user API-token authentication. Requests
// must include `Authorization: Bearer <token>` where sha256(<token>)
// matches a non-revoked, non-expired row in the api_tokens table.
// Together with WithAPIToken, either match satisfies auth.
func WithTokenStore(repo store.APITokenRepo) MountOption {
	return func(s *mountSettings) {
		s.tokens = repo
	}
}

// WithAuthClock injects a clock used for token-expiry checks. Tests
// pass a fixed func for determinism; production callers leave it
// unset.
func WithAuthClock(now func() time.Time) MountOption {
	return func(s *mountSettings) {
		s.now = now
	}
}

// WithAuthLogger sets the slog destination for auth-path warnings
// (failed TouchLastUsed updates, DB lookup errors). Defaults to
// slog.Default().
func WithAuthLogger(l *slog.Logger) MountOption {
	return func(s *mountSettings) {
		s.logger = l
	}
}

// authRequired reports whether ANY auth mechanism is configured. The
// /v1/auth-status endpoint reports this back to the UI so it knows
// whether to show the login gate.
func (s *mountSettings) authRequired() bool {
	return s.apiToken != "" || s.tokens != nil
}

func (s *mountSettings) nowFunc() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *mountSettings) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// ctxKey identifies the *auth.Token attached to a request's context
// after a successful auth check. Shared-token callers get a synthetic
// admin-scoped identity so downstream scope checks have a uniform
// caller representation.
type ctxKey string

const ctxTokenKey ctxKey = "signalwatch.token"

// tokenFromContext returns the resolved caller token, or nil when auth
// was disabled at Mount time.
func tokenFromContext(ctx context.Context) *auth.Token {
	v, _ := ctx.Value(ctxTokenKey).(*auth.Token)
	return v
}

// requireAuth wraps an http.HandlerFunc with bearer-token enforcement.
// When auth is disabled (both legacy and DB store empty) the wrapper
// is a no-op pass-through. Otherwise the request must carry
// `Authorization: Bearer <token>` matching one of:
//
//   - the configured shared token (legacy, treated as admin scope), or
//   - a row in store.APITokenRepo whose sha256(secret) matches and
//     which is neither revoked nor expired.
//
// Comparison uses crypto/subtle for the shared-token path; the DB path
// looks up by hash (so the secret is never logged or held in memory
// longer than the hash call).
func requireAuth(s *mountSettings, h http.HandlerFunc) http.HandlerFunc {
	if !s.authRequired() {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		provided, err := bearerToken(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}

		// Legacy shared-token path.
		if s.apiToken != "" {
			expected := []byte(s.apiToken)
			if len(provided) == len(expected) &&
				subtle.ConstantTimeCompare(provided, expected) == 1 {
				ctx := context.WithValue(r.Context(), ctxTokenKey, sharedTokenIdentity())
				h(w, r.WithContext(ctx))
				return
			}
		}

		// Per-user token path.
		if s.tokens != nil {
			hash := auth.HashToken(string(provided))
			tok, lookupErr := s.tokens.GetByHash(r.Context(), hash)
			if lookupErr != nil {
				s.log().Warn("api.auth.token_lookup_error", "err", lookupErr)
				writeError(w, http.StatusInternalServerError, errors.New("auth lookup failed"))
				return
			}
			if tok != nil && !tok.Revoked && !tok.IsExpired(s.nowFunc()) {
				// Best-effort last-used update — failure must not
				// fail the request because the caller is otherwise
				// valid. The touch goroutine intentionally uses a
				// fresh context.Background() rather than the request
				// context: the request context is cancelled as soon
				// as the response is flushed, which would cause the
				// touch UPDATE to race-cancel. We bound the work with
				// an explicit 2s timeout so a slow DB still can't
				// leak goroutines.
				// #nosec G118 -- intentional detached context; see
				// comment above.
				go func(id string, ts int64) {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					if err := s.tokens.TouchLastUsed(ctx, id, ts); err != nil {
						s.log().Warn("api.auth.touch_last_used_failed", "err", err, "token_id", id)
					}
				}(tok.ID, s.nowFunc().UnixMilli())

				ctx := context.WithValue(r.Context(), ctxTokenKey, tok)
				h(w, r.WithContext(ctx))
				return
			}
		}

		writeError(w, http.StatusUnauthorized, errors.New("invalid token"))
	}
}

// requireScope wraps a handler with a scope check. It MUST be composed
// inside requireAuth — when no token is in the request context, the
// check is skipped (auth was disabled at the Mount layer, so all
// callers are effectively admin).
func requireScope(scope auth.Scope, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := tokenFromContext(r.Context())
		if tok == nil {
			// Auth disabled at Mount time — allow.
			h(w, r)
			return
		}
		if !tok.HasScope(scope) {
			writeError(w, http.StatusForbidden, errors.New("insufficient scope"))
			return
		}
		h(w, r)
	}
}

// sharedTokenInstance is the synthetic admin-scoped identity
// representing a v0.1-style shared-token caller. Reusing a single
// immutable instance avoids per-request allocation.
var sharedTokenInstance = &auth.Token{
	ID:     "shared",
	Name:   "shared-token (legacy)",
	Scopes: []auth.Scope{auth.ScopeAdmin},
}

func sharedTokenIdentity() *auth.Token { return sharedTokenInstance }

// bearerToken extracts the token portion of a "Bearer ..." Authorization
// header. Returns an error when the header is missing or malformed.
func bearerToken(r *http.Request) ([]byte, error) {
	raw := r.Header.Get("Authorization")
	if raw == "" {
		return nil, errors.New("missing Authorization header")
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(raw, prefix) {
		return nil, errors.New("authorization scheme must be Bearer")
	}
	tok := strings.TrimSpace(raw[len(prefix):])
	if tok == "" {
		return nil, errors.New("empty bearer token")
	}
	return []byte(tok), nil
}
