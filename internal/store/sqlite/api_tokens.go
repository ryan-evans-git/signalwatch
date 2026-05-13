package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/auth"
	"github.com/ryan-evans-git/signalwatch/internal/store"
)

type apiTokenRepo struct{ db *sql.DB }

// APITokens returns the per-user token repo. Hung off Store so the auth
// middleware can resolve hashes without dragging in every other repo.
func (s *Store) APITokens() store.APITokenRepo { return &apiTokenRepo{db: s.db} }

func (r *apiTokenRepo) Create(ctx context.Context, t *auth.Token) error {
	if t.TokenHash == "" {
		return errors.New("sqlite: api_tokens: TokenHash required")
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	scopesJSON, err := json.Marshal(t.Scopes)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO api_tokens
        (id, name, token_hash, scopes, created_at, expires_at, last_used_at, revoked)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.TokenHash, string(scopesJSON),
		t.CreatedAt.UnixMilli(), nullableMS(t.ExpiresAt), nullableMS(t.LastUsedAt), boolInt(t.Revoked),
	)
	return err
}

func (r *apiTokenRepo) GetByHash(ctx context.Context, h string) (*auth.Token, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, name, token_hash, scopes, created_at, expires_at, last_used_at, revoked
		 FROM api_tokens WHERE token_hash = ?`, h)
	return scanAPIToken(row)
}

func (r *apiTokenRepo) Get(ctx context.Context, id string) (*auth.Token, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, name, token_hash, scopes, created_at, expires_at, last_used_at, revoked
		 FROM api_tokens WHERE id = ?`, id)
	return scanAPIToken(row)
}

func (r *apiTokenRepo) List(ctx context.Context) ([]*auth.Token, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, token_hash, scopes, created_at, expires_at, last_used_at, revoked
		 FROM api_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*auth.Token
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *apiTokenRepo) Revoke(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE api_tokens SET revoked = 1 WHERE id = ?`, id)
	return err
}

func (r *apiTokenRepo) TouchLastUsed(ctx context.Context, id string, ts int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, ts, id)
	return err
}

func (r *apiTokenRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
	return err
}

// nullableMS converts *time.Time to a SQL-friendly value: nil for null,
// int64 milliseconds otherwise.
func nullableMS(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixMilli()
}

func scanAPIToken(row rowScanner) (*auth.Token, error) {
	var (
		id, name, hash, scopes string
		createdAt              int64
		expiresAt, lastUsedAt  sql.NullInt64
		revoked                int
	)
	if err := row.Scan(&id, &name, &hash, &scopes, &createdAt, &expiresAt, &lastUsedAt, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	out := &auth.Token{
		ID:        id,
		Name:      name,
		TokenHash: hash,
		CreatedAt: time.UnixMilli(createdAt),
		Revoked:   revoked != 0,
	}
	if scopes != "" {
		if err := json.Unmarshal([]byte(scopes), &out.Scopes); err != nil {
			return nil, err
		}
	}
	if expiresAt.Valid {
		ts := time.UnixMilli(expiresAt.Int64)
		out.ExpiresAt = &ts
	}
	if lastUsedAt.Valid {
		ts := time.UnixMilli(lastUsedAt.Int64)
		out.LastUsedAt = &ts
	}
	return out, nil
}
