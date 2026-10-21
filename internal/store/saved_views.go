package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/pklnx/paperless-go/internal/store/db"
)

// DefaultSavedViewSymbol is the Document Archive app's symbol for a new
// saved view.
const DefaultSavedViewSymbol = "line.3.horizontal.decrease.circle"

// ErrInvalidFilter means a saved view's filter is not a JSON object.
var ErrInvalidFilter = errors.New("store: saved view filter is not a JSON object")

// SavedView is a named document filter shown in the sidebar, per owner.
// Names need not be unique, as in the app.
type SavedView struct {
	ID        string
	OwnerID   int64
	Name      string
	Symbol    string
	SortIndex int64
	// Filter is the app's filterJSON, a JSON object. Keys the server does
	// not know are kept; PostgreSQL normalizes whitespace and key order.
	Filter     json.RawMessage
	CreatedAt  time.Time
	ModifiedAt time.Time
}

// SavedViewInput holds the fields of a new or changed saved view. All of
// them are replaced on update. An empty Symbol means DefaultSavedViewSymbol,
// an empty Filter means {}.
type SavedViewInput struct {
	Name      string
	Symbol    string
	SortIndex int64
	Filter    json.RawMessage
}

func savedViewFromDB(r db.SavedView) *SavedView {
	return &SavedView{
		ID: idString(r.ID), OwnerID: r.OwnerID, Name: r.Name, Symbol: r.Symbol, SortIndex: r.SortIndex,
		Filter: json.RawMessage(r.Filter), CreatedAt: r.CreatedAt, ModifiedAt: r.ModifiedAt,
	}
}

// filterJSON checks that a filter is a JSON object that PostgreSQL's jsonb
// accepts; empty means {}.
func filterJSON(f json.RawMessage) ([]byte, error) {
	t := bytes.TrimSpace(f)
	if len(t) == 0 {
		return []byte("{}"), nil
	}
	if t[0] != '{' || !json.Valid(t) {
		return nil, ErrInvalidFilter
	}
	// jsonb refuses U+0000 in strings and keys (SQLSTATE 22P05).
	var v any
	if err := json.Unmarshal(t, &v); err != nil || hasNUL(v) {
		return nil, ErrInvalidFilter
	}
	return t, nil
}

// hasNUL reports whether a decoded JSON value holds U+0000 in a string or key.
func hasNUL(v any) bool {
	switch v := v.(type) {
	case string:
		return strings.ContainsRune(v, 0)
	case []any:
		for _, e := range v {
			if hasNUL(e) {
				return true
			}
		}
	case map[string]any:
		for k, e := range v {
			if strings.ContainsRune(k, 0) || hasNUL(e) {
				return true
			}
		}
	}
	return false
}

// CreateSavedView adds a saved view owned by the scope's user. A filter
// that is not a JSON object gives ErrInvalidFilter.
func (s *Store) CreateSavedView(ctx context.Context, sc Scope, in SavedViewInput) (*SavedView, error) {
	owner, err := sc.creator()
	if err != nil {
		return nil, err
	}
	filter, err := filterJSON(in.Filter)
	if err != nil {
		return nil, err
	}
	r, err := s.q.CreateSavedView(ctx, db.CreateSavedViewParams{
		OwnerID: owner, Name: in.Name, Symbol: orDefault(in.Symbol, DefaultSavedViewSymbol),
		SortIndex: in.SortIndex, Filter: filter,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return savedViewFromDB(r), nil
}

// GetSavedView returns one saved view in the scope.
func (s *Store) GetSavedView(ctx context.Context, sc Scope, id string) (*SavedView, error) {
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	r, err := s.q.GetSavedView(ctx, db.GetSavedViewParams{ID: u, Owner: sc.owner()})
	if err != nil {
		return nil, mapErr(err)
	}
	return savedViewFromDB(r), nil
}

// ListSavedViews returns the scope's saved views by owner, then in the
// app's order: sort index, creation time, ID.
func (s *Store) ListSavedViews(ctx context.Context, sc Scope) ([]*SavedView, error) {
	rows, err := s.q.ListSavedViews(ctx, sc.owner())
	if err != nil {
		return nil, err
	}
	out := make([]*SavedView, len(rows))
	for i, r := range rows {
		out[i] = savedViewFromDB(r)
	}
	return out, nil
}

// UpdateSavedView replaces a saved view's fields. A filter that is not a
// JSON object gives ErrInvalidFilter.
func (s *Store) UpdateSavedView(ctx context.Context, sc Scope, id string, in SavedViewInput) (*SavedView, error) {
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	filter, err := filterJSON(in.Filter)
	if err != nil {
		return nil, err
	}
	r, err := s.q.UpdateSavedView(ctx, db.UpdateSavedViewParams{
		ID: u, Owner: sc.owner(), Name: in.Name, Symbol: orDefault(in.Symbol, DefaultSavedViewSymbol),
		SortIndex: in.SortIndex, Filter: filter,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return savedViewFromDB(r), nil
}

// DeleteSavedView removes a saved view.
func (s *Store) DeleteSavedView(ctx context.Context, sc Scope, id string) error {
	u, ok := parseID(id)
	if !ok {
		return ErrNotFound
	}
	n, err := s.q.DeleteSavedView(ctx, db.DeleteSavedViewParams{ID: u, Owner: sc.owner()})
	return deleted(n, err)
}
