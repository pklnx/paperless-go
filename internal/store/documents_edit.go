package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pklnx/paperless-go/internal/search"
	"github.com/pklnx/paperless-go/internal/store/db"
)

// Limits of a document's edited text, in characters.
const (
	MaxTitleLength = 1024
	MaxNotesLength = 100_000
)

// Limits of a bulk change: the IDs of one request, and the documents a
// change by search may cover. A search with more matches is refused as a
// whole (*TooManyError), so a broad query cannot rewrite the archive by
// accident; the UI asks for a narrower one.
const (
	MaxBulkIDs     = 500
	MaxBulkMatches = 5000
)

var (
	// ErrSeveralOwners means a bulk change would set tags, a
	// correspondent or a document type on documents of several owners.
	// Each owner has their own metadata, so no single row fits all.
	ErrSeveralOwners = errors.New("store: the documents belong to several owners")

	// ErrTooMany means a bulk change covers more documents than allowed.
	// The error is a *TooManyError.
	ErrTooMany = errors.New("store: too many documents")
)

// TooManyError is a bulk change over more documents than Limit.
// errors.Is(err, ErrTooMany) holds.
type TooManyError struct {
	Count, Limit int
}

func (e *TooManyError) Error() string {
	return fmt.Sprintf("store: %d documents, at most %d allowed", e.Count, e.Limit)
}

// Is makes errors.Is(err, ErrTooMany) true.
func (e *TooManyError) Is(target error) bool { return target == ErrTooMany }

// ASNTakenError is returned by PatchDocument when another document of the
// owner has the archive serial number. errors.Is(err, ErrASNTaken) holds.
type ASNTakenError struct {
	// DocumentID is the document that has it; empty when it could not be
	// read (it was changed or deleted right after the collision).
	DocumentID string
}

func (e *ASNTakenError) Error() string {
	return "store: archive serial number taken by document " + e.DocumentID
}

// Is makes errors.Is(err, ErrASNTaken) true.
func (e *ASNTakenError) Is(target error) bool { return target == ErrASNTaken }

// DocumentPatch holds the fields of a document to change; nil (or a false
// Set flag) keeps the stored value. With SetASN, SetCorrespondent or
// SetDocumentType a nil value clears the field; with SetTags, TagIDs
// replaces the document's tags.
type DocumentPatch struct {
	Title        *string
	DocumentDate *time.Time
	Notes        *string
	IsFavorite   *bool
	IsInbox      *bool

	SetASN              bool
	ArchiveSerialNumber *int32

	SetCorrespondent bool
	CorrespondentID  *string

	SetDocumentType bool
	DocumentTypeID  *string

	SetTags bool
	TagIDs  []string
}

// Fields names the fields p changes, in the API's spelling, for the audit
// log: names only, never values.
func (p DocumentPatch) Fields() []string {
	var out []string
	for _, f := range []struct {
		set  bool
		name string
	}{
		{p.Title != nil, "title"}, {p.DocumentDate != nil, "documentDate"}, {p.Notes != nil, "notes"},
		{p.IsFavorite != nil, "isFavorite"}, {p.IsInbox != nil, "isInbox"}, {p.SetASN, "archiveSerialNumber"},
		{p.SetCorrespondent, "correspondentId"}, {p.SetDocumentType, "documentTypeId"}, {p.SetTags, "tagIds"},
	} {
		if f.set {
			out = append(out, f.name)
		}
	}
	return out
}

// validTitle reports whether s may be a document's title: storable text
// of at most MaxTitleLength characters without control characters (Cc,
// also NUL, tab and line breaks), as the titles the import makes and the
// names of tags, correspondents and types.
func validTitle(s string) bool {
	return validText(s) && !strings.ContainsFunc(s, unicode.IsControl) && utf8.RuneCountInString(s) <= MaxTitleLength
}

// validNotes reports whether s may be a document's notes: storable text
// of at most MaxNotesLength characters whose only control characters are
// tab, line feed and carriage return (notes have several lines).
func validNotes(s string) bool {
	return validText(s) && utf8.RuneCountInString(s) <= MaxNotesLength && !strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r'
	})
}

func (p DocumentPatch) validate() error {
	if p.Title != nil && !validTitle(*p.Title) {
		return invalid("title")
	}
	if p.Notes != nil && !validNotes(*p.Notes) {
		return invalid("notes")
	}
	if p.SetASN && p.ArchiveSerialNumber != nil && *p.ArchiveSerialNumber <= 0 {
		return invalid("archive_serial_number")
	}
	if len(p.Fields()) == 0 {
		return invalid("fields")
	}
	return nil
}

// PatchDocument changes only the fields set in p, in one transaction: it
// locks the document, updates the given columns (the search index follows
// through its triggers), replaces the tags with SetTags and touches
// modified_at. The columns processing writes (text, archive, thumbnail)
// are never touched, and processing never writes these. The audit entry
// of ctx gets the document as its target and, for an admin's change of
// another user's document, the owner's ID.
//
// Errors: ErrNotFound outside the scope; ErrOwnerMismatch for a link to a
// row that is not the document owner's (also in an admin's scope); an
// *ASNTakenError (ErrASNTaken) for an archive serial number another
// document of the owner has; a *ValidationError (ErrInvalid) for refused
// text or nothing to change. A deadlock with a concurrent change is
// retried (see retryDeadlock), then ErrRetry.
func (s *Store) PatchDocument(ctx context.Context, sc Scope, id string, p DocumentPatch) (*Document, error) {
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	corr, err := parseLink(p.CorrespondentID)
	if err != nil {
		return nil, err
	}
	typ, err := parseLink(p.DocumentTypeID)
	if err != nil {
		return nil, err
	}
	tags, err := parseLinks(p.TagIDs)
	if err != nil {
		return nil, err
	}
	var owner int64
	err = s.retryDeadlock(func() error {
		return s.inTx(ctx, func(q *db.Queries) error {
			var err error
			if owner, err = q.LockDocument(ctx, db.LockDocumentParams{ID: u, Owner: sc.owner()}); err != nil {
				return err
			}
			afterLock(ctx)
			n, err := q.PatchDocument(ctx, db.PatchDocumentParams{
				ID: u, Title: p.Title, DocumentDate: p.DocumentDate, Notes: p.Notes,
				IsFavorite: p.IsFavorite, IsInbox: p.IsInbox,
				SetAsn: p.SetASN, ArchiveSerialNumber: p.ArchiveSerialNumber,
				SetCorrespondent: p.SetCorrespondent, CorrespondentID: corr,
				SetDocumentType: p.SetDocumentType, DocumentTypeID: typ,
			})
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrNotFound
			}
			if p.SetTags {
				if err := q.ClearDocumentTags(ctx, u); err != nil {
					return err
				}
				if len(tags) > 0 {
					if err := q.AddDocumentTags(ctx, db.AddDocumentTagsParams{OwnerID: owner, DocumentID: u, TagIds: tags}); err != nil {
						return err
					}
				}
			}
			if e := auditFrom(ctx); e != nil && e.TargetType == TargetDocument {
				e.TargetID = idString(u)
				if owner != sc.UserID {
					e.Detail("ownerId", owner)
				}
			}
			return nil
		})
	})
	if isUniqueViolation(err, "documents_owner_asn_key") {
		taken := &ASNTakenError{}
		if r, lerr := s.q.DocumentByASN(ctx, db.DocumentByASNParams{OwnerID: owner, ArchiveSerialNumber: p.ArchiveSerialNumber}); lerr == nil {
			taken.DocumentID = idString(r.ID)
		}
		return nil, taken
	}
	if err != nil {
		return nil, mapErr(err)
	}
	return s.GetDocument(ctx, sc, id)
}

// BulkSelection says which documents a bulk change covers: IDs (at most
// MaxBulkIDs after removing repeats, all of them in the scope), or every
// match of Match in the scope (at most MaxBulkMatches), resolved by the
// store with the search's own filters. Exactly one of them is set.
// Match's sort, cursor and limit are ignored.
//
// Owner, with Match, narrows the match to that user's documents (the
// list's owner=me or owner=<user ID>). The scope stays the acting user's,
// so the audit records an admin's change of another user's documents
// with their ownerId either way; outside the scope (a non-admin naming
// another user) nothing matches.
type BulkSelection struct {
	IDs   []string
	Match *search.Query
	Owner *int64
}

// matchScope is the scope a match of sel is resolved and locked in: sc,
// or with Owner only that owner's documents. ok is false when sc may not
// see them.
func (sel BulkSelection) matchScope(sc Scope) (Scope, bool) {
	if sel.Owner == nil {
		return sc, true
	}
	if !sc.All && *sel.Owner != sc.UserID {
		return Scope{}, false
	}
	return Scope{UserID: *sel.Owner}, true
}

// BulkChange holds what a bulk change does to each document: tags to add
// and to remove, and fields to set as in DocumentPatch.
type BulkChange struct {
	AddTags    []string
	RemoveTags []string

	SetCorrespondent bool
	CorrespondentID  *string

	SetDocumentType bool
	DocumentTypeID  *string

	IsInbox    *bool
	IsFavorite *bool
}

// Fields names the fields c changes, in the API's spelling.
func (c BulkChange) Fields() []string {
	var out []string
	for _, f := range []struct {
		set  bool
		name string
	}{
		{len(c.AddTags) > 0, "addTags"}, {len(c.RemoveTags) > 0, "removeTags"},
		{c.SetCorrespondent, "correspondent"}, {c.SetDocumentType, "documentType"},
		{c.IsInbox != nil, "inbox"}, {c.IsFavorite != nil, "favorite"},
	} {
		if f.set {
			out = append(out, f.name)
		}
	}
	return out
}

// needsOneOwner reports whether c links documents to an owner's rows.
func (c BulkChange) needsOneOwner() bool {
	return len(c.AddTags) > 0 || len(c.RemoveTags) > 0 ||
		c.SetCorrespondent && c.CorrespondentID != nil || c.SetDocumentType && c.DocumentTypeID != nil
}

// BulkMatch is what a bulk change by search would cover.
type BulkMatch struct {
	// Count is the number of matches, also above MaxBulkMatches.
	Count int
	// Owners is the number of owners among them; 0 when Count is above
	// MaxBulkMatches (not counted).
	Owners int
}

// resolvedMatch holds a search's arguments, resolved once before the
// transaction (the name lookups use the pool).
type resolvedMatch struct {
	args searchArgs
	none bool
}

func (s *Store) resolveMatch(ctx context.Context, sc Scope, q search.Query) (resolvedMatch, error) {
	if err := q.CheckLimits(); err != nil {
		return resolvedMatch{}, err
	}
	m := resolvedMatch{args: searchArgs{Owner: sc.owner()}}
	if q.HasText() {
		tsq, err := s.searchTsquery(ctx, sc, q.Text)
		if err != nil {
			return m, err
		}
		if tsq != "" {
			m.args.Query = tsq
		}
	}
	none, err := s.searchFilters(ctx, sc, q, &m.args)
	m.none = none
	return m, err
}

// match runs the search of m on d: the matches in id order, at most
// MaxBulkMatches+1, and the number of all of them.
func (s *Store) match(ctx context.Context, d db.DBTX, m resolvedMatch) ([]db.MatchDocumentIDsRow, int, error) {
	if m.none {
		return nil, 0, nil
	}
	a := m.args
	rows, err := db.New(s.searchDB(d)).MatchDocumentIDs(ctx, db.MatchDocumentIDsParams{
		Owner: a.Owner, Query: a.Query, TagGroupCount: a.TagGroupCount, TagGroups: a.TagGroups, TagIds: a.TagIds,
		NotTagIds: a.NotTagIds, Correspondents: a.Correspondents, NotCorrespondents: a.NotCorrespondents,
		Types: a.Types, NotTypes: a.NotTypes, Asns: a.Asns, NotAsns: a.NotAsns, DateFrom: a.DateFrom,
		DateTo: a.DateTo, NotDateFrom: a.NotDateFrom, NotDateTo: a.NotDateTo, AddedFrom: a.AddedFrom,
		AddedTo: a.AddedTo, NotAddedFrom: a.NotAddedFrom, NotAddedTo: a.NotAddedTo, Inbox: a.Inbox,
		Favorite: a.Favorite, Lim: MaxBulkMatches + 1,
	})
	if err != nil || len(rows) == 0 {
		return nil, 0, err
	}
	return rows, int(rows[0].Total), nil
}

// CountBulkMatches tells what a bulk change by sel.Match (and sel.Owner)
// in the scope would cover, without changing anything: the dry run the UI
// shows before it asks for confirmation. It uses the same resolution as
// BulkUpdateDocuments.
func (s *Store) CountBulkMatches(ctx context.Context, sc Scope, sel BulkSelection) (BulkMatch, error) {
	if sel.Match == nil || sel.IDs != nil {
		return BulkMatch{}, invalid("selection")
	}
	msc, ok := sel.matchScope(sc)
	if !ok {
		return BulkMatch{}, nil
	}
	m, err := s.resolveMatch(ctx, msc, *sel.Match)
	if err != nil {
		return BulkMatch{}, err
	}
	rows, total, err := s.match(ctx, s.pool, m)
	if err != nil {
		return BulkMatch{}, err
	}
	out := BulkMatch{Count: total}
	if total <= MaxBulkMatches {
		owners := map[int64]bool{}
		for _, r := range rows {
			owners[r.OwnerID] = true
		}
		out.Owners = len(owners)
	}
	return out, nil
}

// BulkUpdateDocuments applies c to the documents of sel, all or nothing,
// in one transaction, and returns the IDs of the changed documents in id
// order. It locks the documents in id order (so two bulk changes over
// overlapping sets wait for each other); by search it then runs the
// search again and keeps only the locked documents that still match, so a
// document changed between the match and the lock so that it no longer
// matches is left alone. It checks them, removes and adds the
// tags (adding a tag a document has, or removing one it lacks, is no
// error), sets the fields and touches modified_at of every one.
//
// The audit entry of ctx is the summary: the store adds count and ids; the caller
// sets the rest. With it, each document gets an entry of its own
// (perDocument's action, target the document, the summary's details
// fields and bulkRun, and ownerId when the owner is not the acting user,
// sc.UserID, also with sel.Owner).
//
// Errors: a *ValidationError (ErrInvalid) for an empty selection, both or
// neither of IDs and Match, nothing to change, or a tag both added and
// removed; a *TooManyError (ErrTooMany) above MaxBulkIDs or
// MaxBulkMatches; ErrNotFound when an ID is not in the scope;
// ErrSeveralOwners when tags, a correspondent or a type are set on
// documents of several owners; ErrOwnerMismatch for a tag to add, a
// correspondent or a type that is not the owner's (a tag to remove that
// the documents do not have is no error). A deadlock, for example
// with a concurrent delete of one of the tags, is retried (see
// retryDeadlock), then ErrRetry. A search that matches nothing changes
// nothing and writes no audit entry.
func (s *Store) BulkUpdateDocuments(ctx context.Context, sc Scope, sel BulkSelection, c BulkChange, perDocument string) ([]string, error) {
	if (sel.IDs == nil) == (sel.Match == nil) {
		return nil, invalid("selection")
	}
	if len(c.Fields()) == 0 {
		return nil, invalid("fields")
	}
	add, err := parseLinks(c.AddTags)
	if err != nil {
		return nil, err
	}
	remove, err := parseLinks(c.RemoveTags)
	if err != nil {
		return nil, err
	}
	for _, a := range add {
		if slices.Contains(remove, a) {
			return nil, invalid("tags")
		}
	}
	corr, err := parseLink(c.CorrespondentID)
	if err != nil {
		return nil, err
	}
	typ, err := parseLink(c.DocumentTypeID)
	if err != nil {
		return nil, err
	}

	var ids []pgtype.UUID
	var m resolvedMatch
	lsc := sc // the scope the documents are locked in
	if sel.Match != nil {
		var ok bool
		if lsc, ok = sel.matchScope(sc); !ok {
			return []string{}, nil
		}
		if m, err = s.resolveMatch(ctx, lsc, *sel.Match); err != nil {
			return nil, err
		}
	} else {
		if sel.Owner != nil {
			return nil, invalid("selection")
		}
		if len(sel.IDs) == 0 {
			return nil, invalid("ids")
		}
		seen := map[[16]byte]bool{}
		for _, id := range sel.IDs {
			u, ok := parseID(id)
			if !ok {
				return nil, ErrNotFound
			}
			if !seen[u.Bytes] {
				seen[u.Bytes] = true
				ids = append(ids, u)
			}
		}
		if len(ids) > MaxBulkIDs {
			return nil, &TooManyError{Count: len(ids), Limit: MaxBulkIDs}
		}
	}

	var changed []string
	err = s.retryDeadlock(func() error {
		changed = nil
		return s.inTxWith(ctx, func(tx pgx.Tx, q *db.Queries) error {
			want := ids
			if sel.Match != nil {
				rows, total, err := s.match(ctx, tx, m)
				if err != nil {
					return err
				}
				if total > MaxBulkMatches {
					return &TooManyError{Count: total, Limit: MaxBulkMatches}
				}
				if total == 0 {
					return errNothingMatched
				}
				want = make([]pgtype.UUID, len(rows))
				for i, r := range rows {
					want[i] = r.ID
				}
				afterMatch(ctx)
			}
			locked, err := q.LockDocuments(ctx, db.LockDocumentsParams{Ids: want, Owner: lsc.owner()})
			if err != nil {
				return err
			}
			afterLock(ctx)
			if sel.Match != nil {
				// The match ran before the lock under READ COMMITTED: a
				// concurrent change committed in between may have taken a
				// document out of it. Run it again now that the documents
				// are locked (the statement sees every change committed
				// before the lock, and the lock keeps them as they are)
				// and keep the locked ones that still match; a match
				// deleted before the lock is left out too. Documents that
				// match only now were not part of the selection.
				locked, err = s.stillMatching(ctx, tx, m, locked)
				if err != nil {
					return err
				}
			} else if len(locked) != len(want) {
				// By IDs, every one must exist.
				return ErrNotFound
			}
			if len(locked) == 0 {
				return errNothingMatched
			}
			got := make([]pgtype.UUID, len(locked))
			owners := map[int64]bool{}
			for i, l := range locked {
				got[i] = l.ID
				owners[l.OwnerID] = true
			}
			if c.needsOneOwner() && len(owners) > 1 {
				return ErrSeveralOwners
			}
			// Tags to add must be the owner's (the foreign key would also
			// refuse them). Tags to remove need no check: a document
			// links only its owner's tags, so another owner's tag, or one
			// deleted meanwhile (a retry after a deadlock with its
			// delete), is simply not there.
			if len(add) > 0 {
				n, err := q.CountOwnedTags(ctx, db.CountOwnedTagsParams{OwnerID: locked[0].OwnerID, Ids: add})
				if err != nil {
					return err
				}
				if n != int64(len(add)) {
					return ErrOwnerMismatch
				}
			}
			if len(remove) > 0 {
				if err := q.BulkRemoveDocumentTags(ctx, db.BulkRemoveDocumentTagsParams{Ids: got, TagIds: remove}); err != nil {
					return err
				}
			}
			if len(add) > 0 {
				if err := q.BulkAddDocumentTags(ctx, db.BulkAddDocumentTagsParams{Ids: got, TagIds: add}); err != nil {
					return err
				}
			}
			if err := q.BulkUpdateDocuments(ctx, db.BulkUpdateDocumentsParams{
				Ids: got, IsFavorite: c.IsFavorite, IsInbox: c.IsInbox,
				SetCorrespondent: c.SetCorrespondent, CorrespondentID: corr,
				SetDocumentType: c.SetDocumentType, DocumentTypeID: typ,
			}); err != nil {
				return err
			}
			for _, l := range locked {
				changed = append(changed, idString(l.ID))
			}
			auditBulk(ctx, sc.UserID, locked, perDocument)
			return nil
		})
	})
	if errors.Is(err, errNothingMatched) {
		return []string{}, nil
	}
	if err != nil {
		return nil, mapErr(err)
	}
	return changed, nil
}

// afterMatchKey carries a test seam: a function a bulk change by search
// calls after the match, before it locks the documents (see
// export_test.go).
type afterMatchKey struct{}

func afterMatch(ctx context.Context) {
	if f, ok := ctx.Value(afterMatchKey{}).(func()); ok {
		f()
	}
}

// errNothingMatched rolls back a bulk change by search without matches,
// so no audit entry is written.
var errNothingMatched = errors.New("store: nothing matched")

// stillMatching returns the rows of locked that the search of m matches
// now, in their order.
func (s *Store) stillMatching(ctx context.Context, tx pgx.Tx, m resolvedMatch, locked []db.LockDocumentsRow) ([]db.LockDocumentsRow, error) {
	rows, _, err := s.match(ctx, tx, m)
	if err != nil {
		return nil, err
	}
	now := make(map[[16]byte]bool, len(rows))
	for _, r := range rows {
		now[r.ID.Bytes] = true
	}
	return slices.DeleteFunc(locked, func(l db.LockDocumentsRow) bool { return !now[l.ID.Bytes] }), nil
}

// auditBulk completes the summary entry of ctx with the count and adds
// one entry per document, with ownerId for a document whose owner is not
// actor (the acting user).
func auditBulk(ctx context.Context, actor int64, locked []db.LockDocumentsRow, action string) {
	e := auditFrom(ctx)
	if e == nil {
		return
	}
	e.Detail("count", len(locked))
	ids := make([]string, len(locked))
	for i, l := range locked {
		ids[i] = idString(l.ID)
	}
	e.Detail("ids", ids)
	entries := make([]*AuditEntry, len(locked))
	for i, l := range locked {
		d := &AuditEntry{Action: action}
		d.SetTarget(TargetDocument, idString(l.ID), "")
		for _, k := range []string{"fields", "bulkRun"} {
			if v, ok := e.Details[k]; ok {
				d.Detail(k, v)
			}
		}
		if l.OwnerID != actor {
			d.Detail("ownerId", l.OwnerID)
		}
		entries[i] = d
	}
	auditExtra(ctx, entries...)
}
