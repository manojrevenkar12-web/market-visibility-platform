// Package store persists policies (PostgreSQL: versioned, append-only, with who activated what) and caches
// visibility masks for request-time checks (Redis).
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lib/pq" // PostgreSQL driver

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/policy"
)

const versionLock = 7_310_001 // advisory lock key for issuing policy versions

// ErrNotFound means no such policy version, or none active yet.
var ErrNotFound = errors.New("not found")

// Policies is the policy store.
type Policies struct{ DB *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS policies (
    version      integer PRIMARY KEY,
    body         jsonb NOT NULL,
    note         text NOT NULL DEFAULT '',
    created_by   text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    activated_by text,
    activated_at timestamptz
);
CREATE INDEX IF NOT EXISTS policies_activated ON policies (activated_at DESC) WHERE activated_at IS NOT NULL;
`

func OpenPolicies(ctx context.Context, dsn string) (*Policies, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, err
	}
	return &Policies{DB: db}, nil
}

// Create validates a policy and stores it as the next version. Versions are never rewritten. Creates take a
// transaction-scoped advisory lock, so concurrent creates queue for the next number instead of colliding.
func (s *Policies) Create(ctx context.Context, p policy.Policy, author string) (policy.Policy, error) {
	if err := p.Validate(); err != nil {
		return policy.Policy{}, err
	}
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		out, err := s.create(ctx, p, author)
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && (pqErr.Code == "40001" || pqErr.Code == "23505") {
			lastErr = err
			continue
		}
		return out, err
	}
	return policy.Policy{}, lastErr
}

func (s *Policies) create(ctx context.Context, p policy.Policy, author string) (policy.Policy, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return policy.Policy{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, versionLock); err != nil {
		return policy.Policy{}, err
	}
	var next int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM policies`).Scan(&next); err != nil {
		return policy.Policy{}, err
	}
	p.Version = next
	body, err := json.Marshal(p)
	if err != nil {
		return policy.Policy{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO policies (version, body, note, created_by) VALUES ($1, $2, $3, $4)`,
		next, body, p.Note, author); err != nil {
		return policy.Policy{}, err
	}
	return p, tx.Commit()
}

// Get loads one version.
func (s *Policies) Get(ctx context.Context, version int) (policy.Policy, error) {
	return s.one(ctx, `SELECT body FROM policies WHERE version = $1`, version)
}

// Active is the most recently activated version.
func (s *Policies) Active(ctx context.Context) (policy.Policy, error) {
	return s.one(ctx, `SELECT body FROM policies WHERE activated_at IS NOT NULL ORDER BY activated_at DESC, version DESC LIMIT 1`)
}

// Activate marks a version active. Activating an older version is a rollback and is recorded the same way.
func (s *Policies) Activate(ctx context.Context, version int, by string, at time.Time) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE policies SET activated_by = $2, activated_at = $3 WHERE version = $1`, version, by, at)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Policies) one(ctx context.Context, q string, args ...any) (policy.Policy, error) {
	var body []byte
	err := s.DB.QueryRowContext(ctx, q, args...).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return policy.Policy{}, ErrNotFound
	}
	if err != nil {
		return policy.Policy{}, err
	}
	var p policy.Policy
	if err := json.Unmarshal(body, &p); err != nil {
		return policy.Policy{}, err
	}
	return p, p.Validate()
}
