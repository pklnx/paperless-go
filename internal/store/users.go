package store

import (
	"context"
	"time"

	"github.com/pklnx/paperless-go/internal/store/db"
)

// User is an account that owns documents and metadata.
type User struct {
	ID        int64
	Name      string
	IsAdmin   bool
	CreatedAt time.Time
}

// CreateUser adds a user. It is a placeholder until users and login (#3)
// replace it, and only tests call it: keep it out of every CLI and HTTP path
// until then. A taken name gives ErrNameTaken.
func (s *Store) CreateUser(ctx context.Context, name string, isAdmin bool) (*User, error) {
	row, err := s.q.CreateUser(ctx, db.CreateUserParams{Name: name, IsAdmin: isAdmin})
	if err != nil {
		return nil, mapErr(err)
	}
	return &User{ID: row.ID, Name: row.Name, IsAdmin: row.IsAdmin, CreatedAt: row.CreatedAt}, nil
}
