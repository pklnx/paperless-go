package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgtype"
)

// Scope says whose rows a store call may see and change. A row outside the
// scope behaves as if it did not exist (ErrNotFound). The web layer decides
// who gets All.
type Scope struct {
	// UserID is the acting user. New rows belong to this user.
	UserID int64
	// All widens reads, updates and deletes to every owner's rows (admins).
	// Links stay within the row's own owner either way.
	All bool
}

// owner is the owner filter for queries: nil means every owner.
func (s Scope) owner() *int64 {
	if s.All {
		return nil
	}
	id := s.UserID
	return &id
}

var errNoUser = errors.New("store: scope has no user")

// creator returns the owner of rows created in this scope.
func (s Scope) creator() (int64, error) {
	if s.UserID <= 0 {
		return 0, errNoUser
	}
	return s.UserID, nil
}

// parseID parses a UUID. A malformed ID names no row.
func parseID(id string) (pgtype.UUID, bool) {
	var u pgtype.UUID
	if err := u.Scan(id); err != nil || !u.Valid {
		return pgtype.UUID{}, false
	}
	return u, true
}

// parseLink parses an optional link to another row; nil means no link. A
// malformed ID gives ErrOwnerMismatch, as an ID of no row of the owner does.
func parseLink(id *string) (pgtype.UUID, error) {
	if id == nil {
		return pgtype.UUID{}, nil
	}
	u, ok := parseID(*id)
	if !ok {
		return pgtype.UUID{}, ErrOwnerMismatch
	}
	return u, nil
}

// parseLinks parses a list of links and drops repeated IDs.
func parseLinks(ids []string) ([]pgtype.UUID, error) {
	out := make([]pgtype.UUID, 0, len(ids))
	seen := make(map[[16]byte]bool, len(ids))
	for _, id := range ids {
		u, ok := parseID(id)
		if !ok {
			return nil, ErrOwnerMismatch
		}
		if !seen[u.Bytes] {
			seen[u.Bytes] = true
			out = append(out, u)
		}
	}
	return out, nil
}

// idString formats a UUID; NULL gives "".
func idString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return u.String()
}

// idPtr formats an optional UUID; NULL gives nil.
func idPtr(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	s := u.String()
	return &s
}
