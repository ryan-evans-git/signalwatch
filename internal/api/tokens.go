package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/ryan-evans-git/signalwatch/internal/auth"
)

// tokenView is the public JSON shape for an existing token. The raw
// secret is never returned here — only at issuance.
type tokenView struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Scopes     []auth.Scope `json:"scopes"`
	CreatedAt  time.Time    `json:"created_at"`
	ExpiresAt  *time.Time   `json:"expires_at,omitempty"`
	LastUsedAt *time.Time   `json:"last_used_at,omitempty"`
	Revoked    bool         `json:"revoked"`
}

func toTokenView(t *auth.Token) tokenView {
	return tokenView{
		ID:         t.ID,
		Name:       t.Name,
		Scopes:     t.Scopes,
		CreatedAt:  t.CreatedAt,
		ExpiresAt:  t.ExpiresAt,
		LastUsedAt: t.LastUsedAt,
		Revoked:    t.Revoked,
	}
}

// listTokens returns every token in the store. Secrets are not
// included. Always admin-scoped (mounted under writeGate).
func (h *handlers) listTokens(w http.ResponseWriter, r *http.Request) {
	rows, err := h.settings.tokens.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]tokenView, 0, len(rows))
	for _, t := range rows {
		out = append(out, toTokenView(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// issueTokenRequest is the POST /v1/auth/tokens body.
type issueTokenRequest struct {
	Name      string       `json:"name"`
	Scopes    []auth.Scope `json:"scopes"`
	ExpiresIn string       `json:"expires_in,omitempty"` // Go duration; "" = never
}

// issueTokenResponse echoes the new token view plus the raw secret —
// the ONLY time we return that secret. Clients must capture it on
// success; we can't recover it later (only the hash is persisted).
type issueTokenResponse struct {
	Token tokenView `json:"token"`
	Raw   string    `json:"secret"`
}

// issueToken mints a fresh token and stores it. The plaintext is
// returned exactly once.
func (h *handlers) issueToken(w http.ResponseWriter, r *http.Request) {
	var body issueTokenRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, errors.New("name required"))
		return
	}
	if len(body.Scopes) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("at least one scope required"))
		return
	}
	for _, s := range body.Scopes {
		if err := auth.ValidateScope(s); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}

	var expires *time.Time
	if body.ExpiresIn != "" {
		d, err := time.ParseDuration(body.ExpiresIn)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("expires_in: "+err.Error()))
			return
		}
		if d <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("expires_in must be positive"))
			return
		}
		t := h.settings.nowFunc().Add(d)
		expires = &t
	}

	raw, hash, err := auth.GenerateToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	tok := &auth.Token{
		ID:        uuid.NewString(),
		Name:      body.Name,
		TokenHash: hash,
		Scopes:    body.Scopes,
		CreatedAt: h.settings.nowFunc(),
		ExpiresAt: expires,
	}
	if err := h.settings.tokens.Create(r.Context(), tok); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, issueTokenResponse{
		Token: toTokenView(tok),
		Raw:   raw,
	})
}

// revokeToken marks the token revoked. Idempotent; revoking a non-
// existent ID is treated as a 404 so clients can distinguish "already
// gone" from "never existed".
func (h *handlers) revokeToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, errors.New("id required"))
		return
	}
	existing, err := h.settings.tokens.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, errors.New("token not found"))
		return
	}
	if err := h.settings.tokens.Revoke(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
