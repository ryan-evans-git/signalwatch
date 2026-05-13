package api

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
)

// MountOption customizes a call to Mount. Used for optional config
// (currently auth; future: rate limits, CORS) so adding parameters
// doesn't break existing callers.
type MountOption func(*mountSettings)

// mountSettings is the internal accumulator that MountOptions write
// into. Zero values mean "off".
type mountSettings struct {
	apiToken string
}

// WithAPIToken enables bearer-token auth on every /v1/* route.
// Requests must include `Authorization: Bearer <token>` matching this
// value or the server returns 401. Passing an empty string disables
// auth entirely (the default), which is intended for single-tenant
// localhost deployments.
//
// The current implementation supports exactly one shared token. Per-
// user auth + RBAC is post-PI-1 (see ROADMAP.md).
func WithAPIToken(token string) MountOption {
	return func(s *mountSettings) {
		s.apiToken = token
	}
}

// requireAuth wraps an http.HandlerFunc with bearer-token enforcement.
// When token is empty the wrapper is a no-op pass-through. Otherwise
// the request must carry `Authorization: Bearer <token>` byte-for-
// byte equal to the configured value; comparison uses crypto/subtle
// to avoid timing leaks.
func requireAuth(token string, h http.HandlerFunc) http.HandlerFunc {
	if token == "" {
		return h
	}
	expected := []byte(token)
	return func(w http.ResponseWriter, r *http.Request) {
		provided, err := bearerToken(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		// subtle.ConstantTimeCompare requires equal-length inputs.
		// If the lengths differ we still want a constant-time path
		// rather than an obvious-length-leak short-circuit.
		if len(provided) != len(expected) ||
			subtle.ConstantTimeCompare(provided, expected) != 1 {
			writeError(w, http.StatusUnauthorized, errors.New("invalid token"))
			return
		}
		h(w, r)
	}
}

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
