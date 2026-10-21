package store

import (
	"context"
	"errors"
	"time"

	"github.com/pklnx/paperless-go/internal/store/db"
)

// Defaults of the Document Archive app for a deadline's raw strings.
const (
	DefaultDeadlineKind   = "other"
	DefaultDeadlineOrigin = "manual"
)

// ErrNoDeadlineDate means a deadline was given without a date.
var ErrNoDeadlineDate = errors.New("store: deadline has no date")

// Deadline is a deadline or expiry date of a document; a document can have
// several. Kind and Origin are the app's raw strings, kept as they are, so
// values of newer app versions survive a round trip. A deadline always
// belongs to its document's owner.
type Deadline struct {
	ID                 string
	OwnerID            int64
	DocumentID         string
	Kind               string // expires, due, noticeEnds, other, or a newer value
	Date               time.Time
	Label              string
	Origin             string // manual, heuristic, ai, or a newer value
	IsDone             bool
	AddedToCalendarAt  *time.Time
	AddedToRemindersAt *time.Time
	CreatedAt          time.Time
	ModifiedAt         time.Time
}

// DeadlineInput holds the fields of a new or changed deadline. All of them
// are replaced on update. Empty Kind and Origin mean the defaults; Date is
// required (ErrNoDeadlineDate).
type DeadlineInput struct {
	Kind               string
	Date               time.Time
	Label              string
	Origin             string
	IsDone             bool
	AddedToCalendarAt  *time.Time
	AddedToRemindersAt *time.Time
}

func deadlineFromDB(r db.Deadline) *Deadline {
	return &Deadline{
		ID: idString(r.ID), OwnerID: r.OwnerID, DocumentID: idString(r.DocumentID), Kind: r.Kind,
		Date: r.Date, Label: r.Label, Origin: r.Origin, IsDone: r.IsDone,
		AddedToCalendarAt: r.AddedToCalendarAt, AddedToRemindersAt: r.AddedToRemindersAt,
		CreatedAt: r.CreatedAt, ModifiedAt: r.ModifiedAt,
	}
}

// AddDeadline adds a deadline to a document in the scope. The deadline
// belongs to the document's owner, also in an All scope. A document outside
// the scope, a missing one and a malformed ID all give ErrOwnerMismatch, as
// any link to a row that is not the owner's does.
func (s *Store) AddDeadline(ctx context.Context, sc Scope, documentID string, in DeadlineInput) (*Deadline, error) {
	if in.Date.IsZero() {
		return nil, ErrNoDeadlineDate
	}
	doc, ok := parseID(documentID)
	if !ok {
		return nil, ErrOwnerMismatch
	}
	r, err := s.q.AddDeadline(ctx, db.AddDeadlineParams{
		DocumentID: doc, Owner: sc.owner(), Kind: orDefault(in.Kind, DefaultDeadlineKind), Date: in.Date,
		Label: in.Label, Origin: orDefault(in.Origin, DefaultDeadlineOrigin), IsDone: in.IsDone,
		AddedToCalendarAt: in.AddedToCalendarAt, AddedToRemindersAt: in.AddedToRemindersAt,
	})
	if err != nil {
		err = mapErr(err)
		if errors.Is(err, ErrNotFound) { // no such document in the scope
			return nil, ErrOwnerMismatch
		}
		return nil, err
	}
	return deadlineFromDB(r), nil
}

// ListDeadlines returns a document's deadlines in the scope, by date. A
// document outside the scope has none.
func (s *Store) ListDeadlines(ctx context.Context, sc Scope, documentID string) ([]*Deadline, error) {
	doc, ok := parseID(documentID)
	if !ok {
		return []*Deadline{}, nil
	}
	rows, err := s.q.ListDeadlines(ctx, db.ListDeadlinesParams{DocumentID: doc, Owner: sc.owner()})
	if err != nil {
		return nil, err
	}
	out := make([]*Deadline, len(rows))
	for i, r := range rows {
		out[i] = deadlineFromDB(r)
	}
	return out, nil
}

// UpdateDeadline replaces a deadline's fields; it stays with its document.
func (s *Store) UpdateDeadline(ctx context.Context, sc Scope, id string, in DeadlineInput) (*Deadline, error) {
	if in.Date.IsZero() {
		return nil, ErrNoDeadlineDate
	}
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	r, err := s.q.UpdateDeadline(ctx, db.UpdateDeadlineParams{
		ID: u, Owner: sc.owner(), Kind: orDefault(in.Kind, DefaultDeadlineKind), Date: in.Date,
		Label: in.Label, Origin: orDefault(in.Origin, DefaultDeadlineOrigin), IsDone: in.IsDone,
		AddedToCalendarAt: in.AddedToCalendarAt, AddedToRemindersAt: in.AddedToRemindersAt,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return deadlineFromDB(r), nil
}

// DeleteDeadline removes a deadline.
func (s *Store) DeleteDeadline(ctx context.Context, sc Scope, id string) error {
	u, ok := parseID(id)
	if !ok {
		return ErrNotFound
	}
	n, err := s.q.DeleteDeadline(ctx, db.DeleteDeadlineParams{ID: u, Owner: sc.owner()})
	return deleted(n, err)
}
