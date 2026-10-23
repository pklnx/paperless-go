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
	// SortLast, on create, puts the view after the owner's others instead
	// of at SortIndex.
	SortLast bool
	Filter   json.RawMessage
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

// CreateSavedView adds a saved view owned by the scope's user, with the
// audit entry of ctx (target saved_view, with the view's name). A filter
// that is not a JSON object gives ErrInvalidFilter. With SortLast the view
// goes after the owner's others (the highest sort index + 1) and SortIndex
// is ignored.
func (s *Store) CreateSavedView(ctx context.Context, sc Scope, in SavedViewInput) (*SavedView, error) {
	owner, err := sc.creator()
	if err != nil {
		return nil, err
	}
	filter, err := filterJSON(in.Filter)
	if err != nil {
		return nil, err
	}
	var out *SavedView
	err = s.inTx(ctx, func(q *db.Queries) error {
		sortIndex := in.SortIndex
		if in.SortLast {
			if sortIndex, err = q.NextSavedViewSortIndex(ctx, owner); err != nil {
				return err
			}
		}
		r, err := q.CreateSavedView(ctx, db.CreateSavedViewParams{
			OwnerID: owner, Name: in.Name, Symbol: orDefault(in.Symbol, DefaultSavedViewSymbol),
			SortIndex: sortIndex, Filter: filter,
		})
		if err != nil {
			return err
		}
		out = savedViewFromDB(r)
		auditRow(ctx, sc, TargetSavedView, out.ID, out.Name, out.OwnerID)
		return nil
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
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

// UpdateSavedView replaces a saved view's fields, with the audit entry of
// ctx (as PatchSavedView). A filter that is not a JSON object gives
// ErrInvalidFilter.
func (s *Store) UpdateSavedView(ctx context.Context, sc Scope, id string, in SavedViewInput) (*SavedView, error) {
	sortIndex := in.SortIndex
	return s.PatchSavedView(ctx, sc, id, SavedViewPatch{
		Name: &in.Name, Symbol: &in.Symbol, SortIndex: &sortIndex, Filter: orEmptyFilter(in.Filter),
	})
}

// orEmptyFilter is f, or {} for an empty filter, so a replace always sets one.
func orEmptyFilter(f json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(f)) == 0 {
		return json.RawMessage("{}")
	}
	return f
}

// SavedViewPatch holds the fields of a saved view to change; nil keeps the
// stored value. An empty Symbol means DefaultSavedViewSymbol.
type SavedViewPatch struct {
	Name      *string
	Symbol    *string
	SortIndex *int64
	// Filter replaces the whole filter object; nil keeps it.
	Filter json.RawMessage
}

// PatchSavedView changes the given fields of a saved view, with the audit
// entry of ctx: target saved_view with the view's name, and oldName on a
// rename. It locks the row, merges and writes in one transaction, so
// concurrent patches of one view do not undo each other. A filter that is
// not a JSON object gives ErrInvalidFilter.
func (s *Store) PatchSavedView(ctx context.Context, sc Scope, id string, p SavedViewPatch) (*SavedView, error) {
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var filter []byte
	if p.Filter != nil {
		var err error
		if filter, err = filterJSON(p.Filter); err != nil {
			return nil, err
		}
	}
	var out *SavedView
	err := s.inTx(ctx, func(q *db.Queries) error {
		old, err := q.LockSavedView(ctx, db.LockSavedViewParams{ID: u, Owner: sc.owner()})
		if err != nil {
			return err
		}
		afterLock(ctx)
		in := db.UpdateSavedViewParams{
			ID: u, Owner: sc.owner(), Name: old.Name, Symbol: old.Symbol, SortIndex: old.SortIndex, Filter: old.Filter,
		}
		if p.Name != nil {
			in.Name = *p.Name
		}
		if p.Symbol != nil {
			in.Symbol = orDefault(*p.Symbol, DefaultSavedViewSymbol)
		}
		if p.SortIndex != nil {
			in.SortIndex = *p.SortIndex
		}
		if filter != nil {
			in.Filter = filter
		}
		r, err := q.UpdateSavedView(ctx, in)
		if err != nil {
			return err
		}
		out = savedViewFromDB(r)
		auditPatch(ctx, sc, TargetSavedView, out.ID, old.Name, out.Name, out.OwnerID)
		return nil
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// DeleteSavedView removes a saved view, with the audit entry of ctx
// (target saved_view, with the view's name).
func (s *Store) DeleteSavedView(ctx context.Context, sc Scope, id string) error {
	u, ok := parseID(id)
	if !ok {
		return ErrNotFound
	}
	err := s.inTx(ctx, func(q *db.Queries) error {
		r, err := q.DeleteSavedView(ctx, db.DeleteSavedViewParams{ID: u, Owner: sc.owner()})
		if err != nil {
			return err
		}
		auditRow(ctx, sc, TargetSavedView, idString(r.ID), r.Name, r.OwnerID)
		return nil
	})
	return mapErr(err)
}
