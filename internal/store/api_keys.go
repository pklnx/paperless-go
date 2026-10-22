package store

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/paperless-go/internal/store/db"
)

// API keys let scripts use the API, ported from mail-archive. The store
// keeps the SHA-256 of a key and its last characters as a hint; the key
// itself is shown once. Every time comes from the caller (the server
// clock).

// MaxAPIKeys is the number of keys a user can have that are neither
// revoked nor expired.
const MaxAPIKeys = 20

// API key scopes: read for the GET routes, write for the routes that
// change documents.
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
)

// APIKeyScopes are all scopes, in the order they are stored.
var APIKeyScopes = []string{ScopeRead, ScopeWrite}

// apiKeyRetention is how long revoked and expired keys stay listed.
const apiKeyRetention = 30 * 24 * time.Hour

// apiKeyTouchInterval limits how often a key's last use is written.
const apiKeyTouchInterval = time.Minute

// API key errors.
var (
	ErrAPIKeyLimit = errors.New("store: this user has the maximum number of API keys")
	ErrAPIKeyName  = errors.New("store: an API key with this name exists")
	ErrAPIKeyScope = errors.New("store: unknown API key scope")
)

// API key states.
const (
	APIKeyActive  = "active"
	APIKeyExpired = "expired"
	APIKeyRevoked = "revoked"
)

// APIKey is a stored key, without the key.
type APIKey struct {
	ID         int64
	UserID     int64
	UserName   string
	Name       string
	Hint       string
	Scopes     []string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// State tells whether the key works at now (apart from its owner being
// locked).
func (k *APIKey) State(now time.Time) string {
	switch {
	case k.RevokedAt != nil:
		return APIKeyRevoked
	case k.ExpiresAt != nil && !k.ExpiresAt.After(now):
		return APIKeyExpired
	}
	return APIKeyActive
}

// HasScope reports whether the key has scope.
func (k *APIKey) HasScope(scope string) bool { return slices.Contains(k.Scopes, scope) }

// NormalizeScopes removes duplicates and sorts scopes in their stored order.
// It fails with ErrAPIKeyScope for an unknown one or none.
func NormalizeScopes(scopes []string) ([]string, error) {
	for _, s := range scopes {
		if !slices.Contains(APIKeyScopes, s) {
			return nil, ErrAPIKeyScope
		}
	}
	out := make([]string, 0, len(APIKeyScopes))
	for _, s := range APIKeyScopes {
		if slices.Contains(scopes, s) {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, ErrAPIKeyScope
	}
	return out, nil
}

// NewAPIKey is what CreateAPIKey stores.
type NewAPIKey struct {
	Hash      []byte
	Hint      string
	Name      string
	Scopes    []string // normalized by NormalizeScopes
	ExpiresAt *time.Time
}

// CreateAPIKey stores a key for a user, created at now, with the audit
// entry of ctx (whose api_key target gets the new ID). It fails with
// ErrAPIKeyLimit when the user has MaxAPIKeys active keys and with
// ErrAPIKeyName when a key that is not revoked has the name.
func (s *Store) CreateAPIKey(ctx context.Context, userID int64, k NewAPIKey, now time.Time) (*APIKey, error) {
	var out *APIKey
	err := s.inTx(ctx, func(q *db.Queries) error {
		u, err := lockUserRow(ctx, q, userID)
		if err != nil {
			return err
		}
		n, err := q.CountActiveAPIKeys(ctx, db.CountActiveAPIKeysParams{UserID: userID, Now: now})
		if err != nil {
			return err
		}
		if n >= MaxAPIKeys {
			return ErrAPIKeyLimit
		}
		id, err := q.InsertAPIKey(ctx, db.InsertAPIKeyParams{
			UserID: userID, KeyHash: k.Hash, Hint: k.Hint, Name: k.Name, Scopes: k.Scopes, CreatedAt: now, ExpiresAt: k.ExpiresAt,
		})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "api_keys_user_name_idx" {
			return ErrAPIKeyName
		}
		if err != nil {
			return err
		}
		out = &APIKey{
			ID: id, UserID: userID, UserName: u.Name, Name: k.Name, Hint: k.Hint, Scopes: k.Scopes,
			CreatedAt: now, ExpiresAt: k.ExpiresAt,
		}
		fillAuditTarget(ctx, TargetAPIKey, id)
		return nil
	})
	return out, err
}

// APIKeyLogin finds the key with hash that works at now and returns its
// owner's state as a Session, like GetSession, and the key. It fails with
// ErrNotFound when the key does not exist, is revoked or expired, or its
// owner is locked. The lookup goes through the unique index on the hash:
// it compares hashes, never keys.
func (s *Store) APIKeyLogin(ctx context.Context, hash []byte, now time.Time) (*Session, *APIKey, error) {
	r, err := s.q.GetAPIKeyLogin(ctx, db.GetAPIKeyLoginParams{KeyHash: hash, Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	sess := &Session{
		UserID: r.UserID, UserName: r.UserName, IsAdmin: r.IsAdmin,
		MustChangePassword: r.MustChangePassword, TwoFactorEnabled: r.TwoFactorEnabled,
	}
	key := &APIKey{ID: r.ID, UserID: r.UserID, UserName: r.UserName, Name: r.Name, Scopes: r.Scopes, LastUsedAt: r.LastUsedAt}
	return sess, key, nil
}

// RefusedAPIKey describes a stored key that APIKeyLogin refused, for the
// audit entry of the attempt.
type RefusedAPIKey struct {
	ID       int64
	UserID   int64
	UserName string
	Name     string
	Reason   string // revoked, expired or locked
}

// FindRefusedAPIKey looks up a key that APIKeyLogin did not accept at now.
// It fails with ErrNotFound for a key that does not exist.
func (s *Store) FindRefusedAPIKey(ctx context.Context, hash []byte, now time.Time) (*RefusedAPIKey, error) {
	r, err := s.q.GetAPIKeyByHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	out := &RefusedAPIKey{ID: r.ID, UserID: r.UserID, UserName: r.UserName, Name: r.Name, Reason: "locked"}
	switch {
	case r.RevokedAt != nil:
		out.Reason = APIKeyRevoked
	case r.ExpiresAt != nil && !r.ExpiresAt.After(now):
		out.Reason = APIKeyExpired
	}
	return out, nil
}

// TouchAPIKey records that a key was used at now, at most once a minute.
func (s *Store) TouchAPIKey(ctx context.Context, id int64, now time.Time) error {
	return s.q.TouchAPIKey(ctx, db.TouchAPIKeyParams{ID: id, Now: now, TouchCutoff: now.Add(-apiKeyTouchInterval)})
}

// ListAPIKeys returns a user's keys, also revoked and expired ones, newest
// first.
func (s *Store) ListAPIKeys(ctx context.Context, userID int64) ([]APIKey, error) {
	rows, err := s.q.ListUserAPIKeys(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]APIKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, APIKey{
			ID: r.ID, UserID: userID, Name: r.Name, Hint: r.Hint, Scopes: r.Scopes, CreatedAt: r.CreatedAt,
			ExpiresAt: r.ExpiresAt, LastUsedAt: r.LastUsedAt, RevokedAt: r.RevokedAt,
		})
	}
	return out, nil
}

// ListAllAPIKeys returns every user's keys, by user name and newest first.
func (s *Store) ListAllAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.q.ListAllAPIKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]APIKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, APIKey{
			ID: r.ID, UserID: r.UserID, UserName: r.UserName, Name: r.Name, Hint: r.Hint, Scopes: r.Scopes,
			CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, LastUsedAt: r.LastUsedAt, RevokedAt: r.RevokedAt,
		})
	}
	return out, nil
}

// APIKeyCounts returns the number of keys active at now per user ID.
func (s *Store) APIKeyCounts(ctx context.Context, now time.Time) (map[int64]int64, error) {
	rows, err := s.q.CountAPIKeysByUser(ctx, now)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int64, len(rows))
	for _, r := range rows {
		out[r.UserID] = r.N
	}
	return out, nil
}

// RevokeAPIKey revokes a key that is not revoked yet, at now, with the
// audit entry of ctx (target: the key; detail owner: its user). With owner
// set, only that user's key; nil is for admins. by is the user who revokes
// it (nil for the CLI). It fails with ErrNotFound.
func (s *Store) RevokeAPIKey(ctx context.Context, id int64, owner, by *int64, now time.Time) (*APIKey, error) {
	var out *APIKey
	err := s.inTx(ctx, func(q *db.Queries) error {
		r, err := q.RevokeAPIKey(ctx, db.RevokeAPIKeyParams{ID: id, Owner: owner, RevokedBy: by, Now: now})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = &APIKey{ID: r.ID, UserID: r.UserID, UserName: r.UserName, Name: r.Name}
		if e := auditFrom(ctx); e != nil {
			e.SetTarget(TargetAPIKey, strconv.FormatInt(r.ID, 10), r.Name)
			e.Detail("owner", r.UserName)
		}
		return nil
	})
	return out, err
}

// RevokeUserAPIKeys revokes all of a user's keys at now, with the audit
// entry of ctx, and returns how many were revoked.
func (s *Store) RevokeUserAPIKeys(ctx context.Context, userID int64, by *int64, now time.Time) (int64, error) {
	var n int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		if _, err := lockUserRow(ctx, q, userID); err != nil {
			return err
		}
		var err error
		n, err = revokeUserAPIKeys(ctx, q, userID, by, now)
		return err
	})
	return n, err
}

func revokeUserAPIKeys(ctx context.Context, q *db.Queries, userID int64, by *int64, now time.Time) (int64, error) {
	n, err := q.RevokeUserAPIKeys(ctx, db.RevokeUserAPIKeysParams{UserID: userID, RevokedBy: by, Now: now})
	if err == nil {
		if e := auditFrom(ctx); e != nil {
			e.Detail("apiKeysRevoked", n)
		}
	}
	return n, err
}

// DeleteOldAPIKeys deletes keys revoked or expired more than 30 days
// before now and returns how many.
func (s *Store) DeleteOldAPIKeys(ctx context.Context, now time.Time) (int64, error) {
	return s.q.DeleteOldAPIKeys(ctx, now.Add(-apiKeyRetention))
}
