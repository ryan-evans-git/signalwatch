package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ryan-evans-git/signalwatch/engine"
	"github.com/ryan-evans-git/signalwatch/internal/api"
	"github.com/ryan-evans-git/signalwatch/internal/auth"
	"github.com/ryan-evans-git/signalwatch/internal/channel"
	"github.com/ryan-evans-git/signalwatch/internal/input"
	"github.com/ryan-evans-git/signalwatch/internal/input/event"
	"github.com/ryan-evans-git/signalwatch/internal/store"
	"github.com/ryan-evans-git/signalwatch/internal/store/sqlite"
)

// tokensFixture spins up a server with the new per-user token store
// enabled, optionally combined with a legacy shared token.
type tokensFixture struct {
	srv     *httptest.Server
	st      *sqlite.Store
	cleanup func()
	now     func() time.Time
}

func newTokensFixture(t *testing.T, sharedToken string, opts ...api.MountOption) *tokensFixture {
	t.Helper()
	// Use the test name as the SQLite URI database name (not just a
	// query param) so each test gets a fresh in-memory DB. The
	// :memory: sentinel is shared across every connection that
	// references it; a unique name with `mode=memory&cache=shared`
	// gives per-test isolation while still allowing the in-process
	// connection pool to share state within one test.
	st, err := sqlite.Open("file:" + t.Name() + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ev := event.New("events")
	eng, err := engine.New(engine.Options{
		Store:      st,
		Channels:   map[string]channel.Channel{},
		Inputs:     []input.Input{ev},
		EventInput: ev,
		Logger:     slog.Default(),
	})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}

	mux := http.NewServeMux()
	mountOpts := []api.MountOption{
		api.WithAPIToken(sharedToken),
		api.WithTokenStore(st.APITokens()),
	}
	mountOpts = append(mountOpts, opts...)
	api.Mount(mux, eng, nil, mountOpts...)
	srv := httptest.NewServer(mux)

	return &tokensFixture{
		srv: srv, st: st, now: time.Now,
		cleanup: func() {
			srv.Close()
			cancel()
			_ = eng.Close()
			_ = st.Close()
		},
	}
}

// seedToken inserts a token directly via the repo (bypassing the API).
// Returns the raw secret so the caller can use it in headers. The
// returned token ID is namespaced by the test name to avoid cross-test
// PK collisions under sqlite's shared in-memory store (when `cache=shared`
// the same `:memory:` DB is reused across tests that omit a unique
// filename, so PKs must be globally unique).
func seedToken(t *testing.T, repo store.APITokenRepo, name string, scopes []auth.Scope, expiresAt *time.Time) string {
	t.Helper()
	raw, hash, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	tok := &auth.Token{
		ID: tokenIDFor(t, name), Name: name, TokenHash: hash,
		Scopes: scopes, ExpiresAt: expiresAt,
	}
	if err := repo.Create(context.Background(), tok); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return raw
}

// tokenIDFor returns a token PK scoped to the calling test. Use this
// (not raw "t-"+name) anywhere a test expects to find / delete / revoke
// a previously-seeded token by ID.
func tokenIDFor(t *testing.T, name string) string {
	t.Helper()
	return "t-" + t.Name() + "-" + name
}

// ---------- auth-status reflects DB tokens ----------

func TestAuthStatus_ReportsTrueWhenTokenStoreMounted(t *testing.T) {
	f := newTokensFixture(t, "")
	defer f.cleanup()
	status, body := doRaw(t, http.MethodGet, f.srv.URL+"/v1/auth-status", "", nil)
	if status != http.StatusOK {
		t.Fatalf("status: %d (%s)", status, body)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["auth_required"] != true {
		t.Fatalf("auth_required: want true (token store mounted), got %v", got["auth_required"])
	}
}

// ---------- shared-token continues to work alongside token store ----------

func TestAuth_SharedTokenStillWorksWhenStorePresent(t *testing.T) {
	f := newTokensFixture(t, "legacy-s3cr3t")
	defer f.cleanup()
	status, body := doRaw(t, http.MethodGet, f.srv.URL+"/v1/rules", "legacy-s3cr3t", nil)
	if status != http.StatusOK {
		t.Fatalf("legacy shared token: want 200, got %d (%s)", status, body)
	}
}

// ---------- per-user token: happy path ----------

func TestAuth_DBToken_AcceptedAndTouches(t *testing.T) {
	f := newTokensFixture(t, "")
	defer f.cleanup()
	raw := seedToken(t, f.st.APITokens(), "alice", []auth.Scope{auth.ScopeAdmin}, nil)

	status, body := doRaw(t, http.MethodGet, f.srv.URL+"/v1/rules", raw, nil)
	if status != http.StatusOK {
		t.Fatalf("DB token request: status=%d body=%s", status, body)
	}

	// Wait briefly for the async last-used update.
	deadline := time.Now().Add(time.Second)
	var got *auth.Token
	for time.Now().Before(deadline) {
		got, _ = f.st.APITokens().Get(context.Background(), tokenIDFor(t, "alice"))
		if got != nil && got.LastUsedAt != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got == nil || got.LastUsedAt == nil {
		t.Fatalf("LastUsedAt not updated: %+v", got)
	}
}

// ---------- per-user token: revocation ----------

func TestAuth_DBToken_RevokedRejected(t *testing.T) {
	f := newTokensFixture(t, "")
	defer f.cleanup()
	raw := seedToken(t, f.st.APITokens(), "bob", []auth.Scope{auth.ScopeAdmin}, nil)
	if err := f.st.APITokens().Revoke(context.Background(), tokenIDFor(t, "bob")); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	status, _ := doRaw(t, http.MethodGet, f.srv.URL+"/v1/rules", raw, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("revoked token: want 401, got %d", status)
	}
}

// ---------- per-user token: expiry ----------

func TestAuth_DBToken_ExpiredRejected(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	f := newTokensFixture(t, "")
	defer f.cleanup()
	raw := seedToken(t, f.st.APITokens(), "exp", []auth.Scope{auth.ScopeAdmin}, &past)
	status, _ := doRaw(t, http.MethodGet, f.srv.URL+"/v1/rules", raw, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("expired token: want 401, got %d", status)
	}
}

// ---------- scope: read scope can GET, cannot POST ----------

func TestAuth_DBToken_ReadScopeCannotMutate(t *testing.T) {
	f := newTokensFixture(t, "")
	defer f.cleanup()
	raw := seedToken(t, f.st.APITokens(), "readonly", []auth.Scope{auth.ScopeRead}, nil)

	// GET works.
	status, _ := doRaw(t, http.MethodGet, f.srv.URL+"/v1/rules", raw, nil)
	if status != http.StatusOK {
		t.Fatalf("read GET: want 200, got %d", status)
	}
	// POST returns 403 because the token only has read scope.
	body, _ := json.Marshal(map[string]any{
		"input_ref": "events", "record": map[string]any{"x": 1},
	})
	status, _ = doRaw(t, http.MethodPost, f.srv.URL+"/v1/events", raw, body)
	if status != http.StatusForbidden {
		t.Fatalf("read POST: want 403, got %d", status)
	}
}

func TestAuth_DBToken_AdminCanMutate(t *testing.T) {
	f := newTokensFixture(t, "")
	defer f.cleanup()
	raw := seedToken(t, f.st.APITokens(), "admin", []auth.Scope{auth.ScopeAdmin}, nil)

	body, _ := json.Marshal(map[string]any{
		"input_ref": "events", "record": map[string]any{"x": 1},
	})
	status, _ := doRaw(t, http.MethodPost, f.srv.URL+"/v1/events", raw, body)
	if status != http.StatusAccepted {
		t.Fatalf("admin POST: want 202, got %d", status)
	}
}

// ---------- token issuance / list / revoke endpoints ----------

func TestTokens_Issue_ReturnsRawSecretOnce(t *testing.T) {
	f := newTokensFixture(t, "")
	defer f.cleanup()
	// To call /v1/auth/tokens we need an admin-scoped caller. Seed one.
	raw := seedToken(t, f.st.APITokens(), "bootstrap", []auth.Scope{auth.ScopeAdmin}, nil)

	body, _ := json.Marshal(map[string]any{
		"name":   "ci-deploybot",
		"scopes": []string{"admin"},
	})
	status, respBody := doRaw(t, http.MethodPost, f.srv.URL+"/v1/auth/tokens", raw, body)
	if status != http.StatusCreated {
		t.Fatalf("issue: want 201, got %d (%s)", status, respBody)
	}
	var got map[string]any
	if err := json.Unmarshal(respBody, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	secret, _ := got["secret"].(string)
	if !auth.LooksLikeToken(secret) {
		t.Fatalf("returned secret doesn't look like a token: %q", secret)
	}
	// The secret should authenticate on a subsequent request.
	status2, _ := doRaw(t, http.MethodGet, f.srv.URL+"/v1/rules", secret, nil)
	if status2 != http.StatusOK {
		t.Fatalf("newly issued token failed to authenticate: %d", status2)
	}
}

func TestTokens_Issue_WithExpiresIn(t *testing.T) {
	f := newTokensFixture(t, "")
	defer f.cleanup()
	raw := seedToken(t, f.st.APITokens(), "boot", []auth.Scope{auth.ScopeAdmin}, nil)

	body, _ := json.Marshal(map[string]any{
		"name": "ephemeral", "scopes": []string{"read"}, "expires_in": "1h",
	})
	status, respBody := doRaw(t, http.MethodPost, f.srv.URL+"/v1/auth/tokens", raw, body)
	if status != http.StatusCreated {
		t.Fatalf("issue: want 201, got %d (%s)", status, respBody)
	}
	var got struct {
		Token struct {
			ExpiresAt *time.Time `json:"expires_at"`
		} `json:"token"`
	}
	_ = json.Unmarshal(respBody, &got)
	if got.Token.ExpiresAt == nil {
		t.Fatal("expires_at not set on response")
	}
}

func TestTokens_Issue_RejectsBadInput(t *testing.T) {
	f := newTokensFixture(t, "")
	defer f.cleanup()
	raw := seedToken(t, f.st.APITokens(), "boot", []auth.Scope{auth.ScopeAdmin}, nil)

	cases := []struct {
		name string
		body any
	}{
		{"missing name", map[string]any{"scopes": []string{"read"}}},
		{"missing scopes", map[string]any{"name": "x"}},
		{"unknown scope", map[string]any{"name": "x", "scopes": []string{"superuser"}}},
		{"bad expires_in", map[string]any{"name": "x", "scopes": []string{"read"}, "expires_in": "tomorrow"}},
		{"negative expires_in", map[string]any{"name": "x", "scopes": []string{"read"}, "expires_in": "-1h"}},
		{"malformed body", "not-json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b []byte
			switch v := tc.body.(type) {
			case string:
				b = []byte(v)
			default:
				b, _ = json.Marshal(v)
			}
			status, _ := doRaw(t, http.MethodPost, f.srv.URL+"/v1/auth/tokens", raw, b)
			if status != http.StatusBadRequest {
				t.Fatalf("want 400, got %d", status)
			}
		})
	}
}

func TestTokens_List_OmitsSecret(t *testing.T) {
	f := newTokensFixture(t, "")
	defer f.cleanup()
	raw := seedToken(t, f.st.APITokens(), "admin", []auth.Scope{auth.ScopeAdmin}, nil)
	_ = seedToken(t, f.st.APITokens(), "reader", []auth.Scope{auth.ScopeRead}, nil)

	status, body := doRaw(t, http.MethodGet, f.srv.URL+"/v1/auth/tokens", raw, nil)
	if status != http.StatusOK {
		t.Fatalf("list: status=%d body=%s", status, body)
	}
	if bytes.Contains(body, []byte("\"secret\"")) {
		t.Fatalf("List leaked secret in response: %s", body)
	}
	if bytes.Contains(body, []byte("\"token_hash\"")) {
		t.Fatalf("List leaked token_hash in response: %s", body)
	}
	var got []map[string]any
	_ = json.Unmarshal(body, &got)
	if len(got) != 2 {
		t.Fatalf("want 2 tokens in list, got %d", len(got))
	}
}

func TestTokens_Revoke_HappyAnd404(t *testing.T) {
	f := newTokensFixture(t, "")
	defer f.cleanup()
	raw := seedToken(t, f.st.APITokens(), "admin", []auth.Scope{auth.ScopeAdmin}, nil)
	_ = seedToken(t, f.st.APITokens(), "target", []auth.Scope{auth.ScopeRead}, nil)

	// Revoke target.
	targetID := tokenIDFor(t, "target")
	status, body := doRaw(t, http.MethodDelete, f.srv.URL+"/v1/auth/tokens/"+targetID, raw, nil)
	if status != http.StatusNoContent {
		t.Fatalf("revoke: want 204, got %d (%s)", status, body)
	}
	got, _ := f.st.APITokens().Get(context.Background(), targetID)
	if !got.Revoked {
		t.Fatal("revoked flag not set")
	}
	// 404 on missing id.
	status, _ = doRaw(t, http.MethodDelete, f.srv.URL+"/v1/auth/tokens/nope", raw, nil)
	if status != http.StatusNotFound {
		t.Fatalf("revoke missing: want 404, got %d", status)
	}
}

// ---------- token-store lookup failure surfaces as 500 ----------

type failingTokenRepo struct{ store.APITokenRepo }

func (failingTokenRepo) GetByHash(_ context.Context, _ string) (*auth.Token, error) {
	return nil, io.ErrUnexpectedEOF
}

func TestAuth_DBLookup_500OnError(t *testing.T) {
	// Disable shared-token path so the DB path is the only option, then
	// supply a failing repo.
	mux := http.NewServeMux()
	api.Mount(mux, nil, nil,
		api.WithTokenStore(failingTokenRepo{}),
	)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Send a syntactically valid bearer that will trigger a DB lookup.
	status, _ := doRaw(t, http.MethodGet, srv.URL+"/v1/auth-status", "", nil)
	if status != http.StatusOK {
		t.Fatalf("status route should stay open: %d", status)
	}
	// Hit a gated route. With nil engine the route would panic if it
	// were called, so we expect the auth middleware to intercept first.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rules", nil)
	req.Header.Set("Authorization", "Bearer sw_anything")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("DB lookup error: want 500, got %d", resp.StatusCode)
	}
}

// ---------- scope check: no token in context (auth disabled) ----------

func TestRequireScope_AuthDisabled_AllowsThrough(t *testing.T) {
	// No api token, no token store → auth disabled → scope checks
	// shouldn't apply.
	f := newAuthFixture(t, "")
	defer f.cleanup()
	status, _ := doRaw(t, http.MethodGet, f.srv.URL+"/v1/rules", "", nil)
	if status != http.StatusOK {
		t.Fatalf("auth-disabled GET: want 200, got %d", status)
	}
}

// ---------- clock injection (deterministic expiry) ----------

func TestAuth_DBToken_FixedClockExpiryBoundary(t *testing.T) {
	fixedNow := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	expires := fixedNow.Add(time.Minute)
	f := newTokensFixture(t, "",
		api.WithAuthClock(func() time.Time { return fixedNow.Add(2 * time.Minute) }),
	)
	defer f.cleanup()
	raw := seedToken(t, f.st.APITokens(), "boundary", []auth.Scope{auth.ScopeAdmin}, &expires)
	status, _ := doRaw(t, http.MethodGet, f.srv.URL+"/v1/rules", raw, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("token past expiry with injected clock: want 401, got %d", status)
	}
}

// ---------- WithAuthLogger smoke ----------

func TestAuth_WithAuthLogger_AcceptsCustomLogger(t *testing.T) {
	// Logger isn't directly assertable from outside, but exercising
	// the option ensures it's wired up and the auth path can call
	// .log() without panicking.
	f := newTokensFixture(t, "",
		api.WithAuthLogger(slog.Default()),
	)
	defer f.cleanup()
	raw := seedToken(t, f.st.APITokens(), "logged", []auth.Scope{auth.ScopeAdmin}, nil)
	status, _ := doRaw(t, http.MethodGet, f.srv.URL+"/v1/rules", raw, nil)
	if status != http.StatusOK {
		t.Fatalf("with custom logger: status=%d", status)
	}
}

// ---------- list / revoke 500 paths ----------

// failingListRepo wraps a working repo and forces List to fail. Used
// to exercise the 500 branch on /v1/auth/tokens.
type failingListRepo struct{ inner store.APITokenRepo }

func (f failingListRepo) Create(ctx context.Context, t *auth.Token) error {
	return f.inner.Create(ctx, t)
}
func (failingListRepo) GetByHash(_ context.Context, _ string) (*auth.Token, error) {
	return &auth.Token{ID: "shadow", Scopes: []auth.Scope{auth.ScopeAdmin}}, nil
}
func (f failingListRepo) Get(ctx context.Context, id string) (*auth.Token, error) {
	return f.inner.Get(ctx, id)
}
func (failingListRepo) List(_ context.Context) ([]*auth.Token, error) {
	return nil, io.ErrUnexpectedEOF
}
func (f failingListRepo) Revoke(ctx context.Context, id string) error {
	return f.inner.Revoke(ctx, id)
}
func (f failingListRepo) TouchLastUsed(ctx context.Context, id string, ts int64) error {
	return f.inner.TouchLastUsed(ctx, id, ts)
}
func (f failingListRepo) Delete(ctx context.Context, id string) error {
	return f.inner.Delete(ctx, id)
}

func TestTokens_List_500OnRepoError(t *testing.T) {
	st, err := sqlite.Open("file:" + t.Name() + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ev := event.New("events")
	eng, err := engine.New(engine.Options{
		Store: st, Inputs: []input.Input{ev}, EventInput: ev,
		Channels: map[string]channel.Channel{}, Logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	defer eng.Close()

	mux := http.NewServeMux()
	api.Mount(mux, eng, nil, api.WithTokenStore(failingListRepo{inner: st.APITokens()}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	status, _ := doRaw(t, http.MethodGet, srv.URL+"/v1/auth/tokens", "sw_anything", nil)
	if status != http.StatusInternalServerError {
		t.Fatalf("List with failing repo: want 500, got %d", status)
	}
}

// failingRevokeRepo lets Get succeed (so we get past the 404 branch)
// but Revoke fails. Exercises the 500 path inside revokeToken.
type failingRevokeRepo struct{ inner store.APITokenRepo }

func (f failingRevokeRepo) Create(ctx context.Context, t *auth.Token) error {
	return f.inner.Create(ctx, t)
}
func (failingRevokeRepo) GetByHash(_ context.Context, _ string) (*auth.Token, error) {
	return &auth.Token{ID: "shadow", Scopes: []auth.Scope{auth.ScopeAdmin}}, nil
}
func (f failingRevokeRepo) Get(_ context.Context, id string) (*auth.Token, error) {
	if id == "target" {
		return &auth.Token{ID: id, Name: "target", Scopes: []auth.Scope{auth.ScopeRead}}, nil
	}
	return nil, nil
}
func (f failingRevokeRepo) List(ctx context.Context) ([]*auth.Token, error) {
	return f.inner.List(ctx)
}
func (failingRevokeRepo) Revoke(_ context.Context, _ string) error {
	return io.ErrUnexpectedEOF
}
func (f failingRevokeRepo) TouchLastUsed(ctx context.Context, id string, ts int64) error {
	return f.inner.TouchLastUsed(ctx, id, ts)
}
func (f failingRevokeRepo) Delete(ctx context.Context, id string) error {
	return f.inner.Delete(ctx, id)
}

func TestTokens_Revoke_500OnRepoError(t *testing.T) {
	st, err := sqlite.Open("file:" + t.Name() + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ev := event.New("events")
	eng, err := engine.New(engine.Options{
		Store: st, Inputs: []input.Input{ev}, EventInput: ev,
		Channels: map[string]channel.Channel{}, Logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	defer eng.Close()

	mux := http.NewServeMux()
	api.Mount(mux, eng, nil, api.WithTokenStore(failingRevokeRepo{inner: st.APITokens()}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	status, _ := doRaw(t, http.MethodDelete, srv.URL+"/v1/auth/tokens/target", "sw_anything", nil)
	if status != http.StatusInternalServerError {
		t.Fatalf("Revoke with failing repo: want 500, got %d", status)
	}
}

func TestTokens_Revoke_MissingID(t *testing.T) {
	f := newTokensFixture(t, "")
	defer f.cleanup()
	raw := seedToken(t, f.st.APITokens(), "a", []auth.Scope{auth.ScopeAdmin}, nil)
	// PathValue extraction means a trailing-slash route still gets through;
	// the explicit empty-id branch is exercised via path "/" suffix.
	req, _ := http.NewRequest(http.MethodDelete, f.srv.URL+"/v1/auth/tokens/", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	// Net/http's path router matches "/v1/auth/tokens/{id}" → 404
	// when id is empty before reaching the handler. Either 400 or 404
	// is fine; the important point is the handler doesn't crash.
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing id: want 400/404, got %d", resp.StatusCode)
	}
}
