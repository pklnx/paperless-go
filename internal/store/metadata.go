package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pklnx/paperless-go/internal/store/db"
)

// Defaults of the Document Archive app for fields left empty.
const (
	DefaultMatchMode          = "any"
	DefaultTagColor           = "#3B82F6"
	DefaultTagSymbol          = "tag.fill"
	DefaultDocumentTypeSymbol = "doc.text"
)

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// Correspondent is a sender or issuer of documents, per owner. Names are
// unique per owner, ignoring case.
type Correspondent struct {
	ID         string
	OwnerID    int64
	Name       string
	MatchText  string
	MatchMode  string // none, any, all, exact, regex
	CreatedAt  time.Time
	ModifiedAt time.Time
}

// CorrespondentInput holds the fields of a new or changed correspondent.
// An empty MatchMode means DefaultMatchMode. OwnerID is for a create only:
// see creatorFor.
type CorrespondentInput struct {
	OwnerID   int64
	Name      string
	MatchText string
	MatchMode string
}

func correspondentFromDB(r db.Correspondent) *Correspondent {
	return &Correspondent{
		ID: idString(r.ID), OwnerID: r.OwnerID, Name: r.Name, MatchText: r.MatchText,
		MatchMode: r.MatchMode, CreatedAt: r.CreatedAt, ModifiedAt: r.ModifiedAt,
	}
}

// CreateCorrespondent adds a correspondent owned by the scope's user, or
// by in.OwnerID (see creatorFor), with the audit entry of ctx.
func (s *Store) CreateCorrespondent(ctx context.Context, sc Scope, in CorrespondentInput) (*Correspondent, error) {
	owner, err := sc.creatorFor(in.OwnerID)
	if err != nil {
		return nil, err
	}
	var out *Correspondent
	err = s.ownerTx(ctx, []int64{owner}, func(ctx context.Context, t *writeTx) error {
		r, err := t.q.CreateCorrespondent(ctx, db.CreateCorrespondentParams{
			OwnerID: owner, Name: in.Name, MatchText: in.MatchText, MatchMode: orDefault(in.MatchMode, DefaultMatchMode),
		})
		if err != nil {
			return err
		}
		out = correspondentFromDB(r)
		auditRow(ctx, sc, TargetCorrespondent, out.ID, out.Name, out.OwnerID)
		return nil
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// GetCorrespondent returns one correspondent in the scope.
func (s *Store) GetCorrespondent(ctx context.Context, sc Scope, id string) (*Correspondent, error) {
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	r, err := s.q.GetCorrespondent(ctx, db.GetCorrespondentParams{ID: u, Owner: sc.owner()})
	if err != nil {
		return nil, mapErr(err)
	}
	return correspondentFromDB(r), nil
}

// ListCorrespondents returns the scope's correspondents by owner and name.
func (s *Store) ListCorrespondents(ctx context.Context, sc Scope) ([]*Correspondent, error) {
	rows, err := s.q.ListCorrespondents(ctx, sc.owner())
	if err != nil {
		return nil, err
	}
	out := make([]*Correspondent, len(rows))
	for i, r := range rows {
		out[i] = correspondentFromDB(r)
	}
	return out, nil
}

// UpdateCorrespondent replaces a correspondent's fields, under the owner's
// write lock.
func (s *Store) UpdateCorrespondent(ctx context.Context, sc Scope, id string, in CorrespondentInput) (*Correspondent, error) {
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var r db.Correspondent
	err := s.inOwnerTx(ctx, sc, u, kindCorrespondent, func(ctx context.Context, t *writeTx) error {
		var err error
		r, err = t.q.UpdateCorrespondent(ctx, db.UpdateCorrespondentParams{
			ID: u, Owner: sc.owner(), Name: in.Name, MatchText: in.MatchText,
			MatchMode: orDefault(in.MatchMode, DefaultMatchMode),
		})
		if err != nil {
			return err
		}
		return t.holds(r.OwnerID)
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return correspondentFromDB(r), nil
}

// DeleteCorrespondent removes a correspondent, with the audit entry of
// ctx; its documents keep existing without one. It reads the owner, takes
// the owner's write lock (locking.go), then deletes; the SET NULL on the
// documents and the search triggers run under that lock.
func (s *Store) DeleteCorrespondent(ctx context.Context, sc Scope, id string) error {
	u, ok := parseID(id)
	if !ok {
		return ErrNotFound
	}
	return mapErr(s.inOwnerTx(ctx, sc, u, kindCorrespondent, func(ctx context.Context, t *writeTx) error {
		afterLock(ctx)
		r, err := t.q.DeleteCorrespondent(ctx, db.DeleteCorrespondentParams{ID: u, Owner: sc.owner()})
		if err != nil {
			return err
		}
		if err := t.holds(r.OwnerID); err != nil {
			return err
		}
		auditRow(ctx, sc, TargetCorrespondent, idString(r.ID), r.Name, r.OwnerID)
		return nil
	}))
}

// DocumentType is a kind of document (invoice, contract, ...), per owner.
// Names are unique per owner, ignoring case.
type DocumentType struct {
	ID         string
	OwnerID    int64
	Name       string
	Symbol     string
	MatchText  string
	MatchMode  string
	CreatedAt  time.Time
	ModifiedAt time.Time
}

// DocumentTypeInput holds the fields of a new or changed document type.
// Empty Symbol and MatchMode mean the defaults. OwnerID is for a create
// only: see creatorFor.
type DocumentTypeInput struct {
	OwnerID   int64
	Name      string
	Symbol    string
	MatchText string
	MatchMode string
}

func documentTypeFromDB(r db.DocumentType) *DocumentType {
	return &DocumentType{
		ID: idString(r.ID), OwnerID: r.OwnerID, Name: r.Name, Symbol: r.Symbol, MatchText: r.MatchText,
		MatchMode: r.MatchMode, CreatedAt: r.CreatedAt, ModifiedAt: r.ModifiedAt,
	}
}

// CreateDocumentType adds a document type owned by the scope's user, or
// by in.OwnerID (see creatorFor), with the audit entry of ctx.
func (s *Store) CreateDocumentType(ctx context.Context, sc Scope, in DocumentTypeInput) (*DocumentType, error) {
	owner, err := sc.creatorFor(in.OwnerID)
	if err != nil {
		return nil, err
	}
	var out *DocumentType
	err = s.ownerTx(ctx, []int64{owner}, func(ctx context.Context, t *writeTx) error {
		r, err := t.q.CreateDocumentType(ctx, db.CreateDocumentTypeParams{
			OwnerID: owner, Name: in.Name, Symbol: orDefault(in.Symbol, DefaultDocumentTypeSymbol),
			MatchText: in.MatchText, MatchMode: orDefault(in.MatchMode, DefaultMatchMode),
		})
		if err != nil {
			return err
		}
		out = documentTypeFromDB(r)
		auditRow(ctx, sc, TargetDocumentType, out.ID, out.Name, out.OwnerID)
		return nil
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// GetDocumentType returns one document type in the scope.
func (s *Store) GetDocumentType(ctx context.Context, sc Scope, id string) (*DocumentType, error) {
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	r, err := s.q.GetDocumentType(ctx, db.GetDocumentTypeParams{ID: u, Owner: sc.owner()})
	if err != nil {
		return nil, mapErr(err)
	}
	return documentTypeFromDB(r), nil
}

// ListDocumentTypes returns the scope's document types by owner and name.
func (s *Store) ListDocumentTypes(ctx context.Context, sc Scope) ([]*DocumentType, error) {
	rows, err := s.q.ListDocumentTypes(ctx, sc.owner())
	if err != nil {
		return nil, err
	}
	out := make([]*DocumentType, len(rows))
	for i, r := range rows {
		out[i] = documentTypeFromDB(r)
	}
	return out, nil
}

// UpdateDocumentType replaces a document type's fields, under the owner's
// write lock.
func (s *Store) UpdateDocumentType(ctx context.Context, sc Scope, id string, in DocumentTypeInput) (*DocumentType, error) {
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var r db.DocumentType
	err := s.inOwnerTx(ctx, sc, u, kindDocumentType, func(ctx context.Context, t *writeTx) error {
		var err error
		r, err = t.q.UpdateDocumentType(ctx, db.UpdateDocumentTypeParams{
			ID: u, Owner: sc.owner(), Name: in.Name, Symbol: orDefault(in.Symbol, DefaultDocumentTypeSymbol),
			MatchText: in.MatchText, MatchMode: orDefault(in.MatchMode, DefaultMatchMode),
		})
		if err != nil {
			return err
		}
		return t.holds(r.OwnerID)
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return documentTypeFromDB(r), nil
}

// DeleteDocumentType removes a document type, with the audit entry of
// ctx; its documents keep existing without one. It locks as
// DeleteCorrespondent.
func (s *Store) DeleteDocumentType(ctx context.Context, sc Scope, id string) error {
	u, ok := parseID(id)
	if !ok {
		return ErrNotFound
	}
	return mapErr(s.inOwnerTx(ctx, sc, u, kindDocumentType, func(ctx context.Context, t *writeTx) error {
		afterLock(ctx)
		r, err := t.q.DeleteDocumentType(ctx, db.DeleteDocumentTypeParams{ID: u, Owner: sc.owner()})
		if err != nil {
			return err
		}
		if err := t.holds(r.OwnerID); err != nil {
			return err
		}
		auditRow(ctx, sc, TargetDocumentType, idString(r.ID), r.Name, r.OwnerID)
		return nil
	}))
}

// Tag is a label on documents, per owner. Tags nest through ParentID;
// sibling names are unique, ignoring case.
type Tag struct {
	ID         string
	OwnerID    int64
	Name       string
	ColorHex   string
	Symbol     string
	ParentID   *string
	MatchText  string
	MatchMode  string
	CreatedAt  time.Time
	ModifiedAt time.Time
}

// TagInput holds the fields of a new or changed tag. Empty ColorHex, Symbol
// and MatchMode mean the defaults; a nil ParentID makes a root tag.
// OwnerID is for a create only: see creatorFor.
type TagInput struct {
	OwnerID   int64
	Name      string
	ColorHex  string
	Symbol    string
	ParentID  *string
	MatchText string
	MatchMode string
}

func tagFromDB(r db.Tag) *Tag {
	return &Tag{
		ID: idString(r.ID), OwnerID: r.OwnerID, Name: r.Name, ColorHex: r.ColorHex, Symbol: r.Symbol,
		ParentID: idPtr(r.ParentID), MatchText: r.MatchText, MatchMode: r.MatchMode,
		CreatedAt: r.CreatedAt, ModifiedAt: r.ModifiedAt,
	}
}

// CreateTag adds a tag owned by the scope's user, or by in.OwnerID (see
// creatorFor), with the audit entry of ctx. A parent of another owner gives
// ErrOwnerMismatch.
func (s *Store) CreateTag(ctx context.Context, sc Scope, in TagInput) (*Tag, error) {
	owner, err := sc.creatorFor(in.OwnerID)
	if err != nil {
		return nil, err
	}
	parent, err := parseLink(in.ParentID)
	if err != nil {
		return nil, err
	}
	var out *Tag
	err = s.ownerTx(ctx, []int64{owner}, func(ctx context.Context, t *writeTx) error {
		r, err := t.q.CreateTag(ctx, db.CreateTagParams{
			OwnerID: owner, Name: in.Name, ColorHex: orDefault(in.ColorHex, DefaultTagColor),
			Symbol: orDefault(in.Symbol, DefaultTagSymbol), ParentID: parent,
			MatchText: in.MatchText, MatchMode: orDefault(in.MatchMode, DefaultMatchMode),
		})
		if err != nil {
			return err
		}
		out = tagFromDB(r)
		auditRow(ctx, sc, TargetTag, out.ID, out.Name, out.OwnerID)
		return nil
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// GetTag returns one tag in the scope.
func (s *Store) GetTag(ctx context.Context, sc Scope, id string) (*Tag, error) {
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	r, err := s.q.GetTag(ctx, db.GetTagParams{ID: u, Owner: sc.owner()})
	if err != nil {
		return nil, mapErr(err)
	}
	return tagFromDB(r), nil
}

// ListTags returns the scope's tags by owner and name.
func (s *Store) ListTags(ctx context.Context, sc Scope) ([]*Tag, error) {
	rows, err := s.q.ListTags(ctx, sc.owner())
	if err != nil {
		return nil, err
	}
	out := make([]*Tag, len(rows))
	for i, r := range rows {
		out[i] = tagFromDB(r)
	}
	return out, nil
}

// UpdateTag replaces a tag's fields, including its parent. A parent that
// would make the tag its own ancestor gives ErrTagCycle; a parent of another
// owner gives ErrOwnerMismatch. It runs under the owner's write lock.
func (s *Store) UpdateTag(ctx context.Context, sc Scope, id string, in TagInput) (*Tag, error) {
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	parent, err := parseLink(in.ParentID)
	if err != nil {
		return nil, err
	}
	var r db.Tag
	err = s.inOwnerTx(ctx, sc, u, kindTag, func(ctx context.Context, t *writeTx) error {
		var err error
		r, err = t.q.UpdateTag(ctx, db.UpdateTagParams{
			ID: u, Owner: sc.owner(), Name: in.Name, ColorHex: orDefault(in.ColorHex, DefaultTagColor),
			Symbol: orDefault(in.Symbol, DefaultTagSymbol), ParentID: parent,
			MatchText: in.MatchText, MatchMode: orDefault(in.MatchMode, DefaultMatchMode),
		})
		if err != nil {
			return err
		}
		return t.holds(r.OwnerID)
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return tagFromDB(r), nil
}

// DeleteTag removes a tag, with the audit entry of ctx. Its children
// become root tags and its documents lose the tag. When a child would then
// collide with a root tag of the same name, nothing changes and the error
// is a *TagNameConflictError (ErrTagNameConflict). It locks as
// DeleteCorrespondent; the SET NULL on the children, the cascade to
// document_tags and its search trigger run under the owner's lock.
func (s *Store) DeleteTag(ctx context.Context, sc Scope, id string) error {
	u, ok := parseID(id)
	if !ok {
		return ErrNotFound
	}
	err := s.inOwnerTx(ctx, sc, u, kindTag, func(ctx context.Context, t *writeTx) error {
		afterLock(ctx)
		r, err := t.q.DeleteTag(ctx, db.DeleteTagParams{ID: u, Owner: sc.owner()})
		if err != nil {
			return err
		}
		if err := t.holds(r.OwnerID); err != nil {
			return err
		}
		auditRow(ctx, sc, TargetTag, idString(r.ID), r.Name, r.OwnerID)
		return nil
	})
	if isUniqueViolation(err, "tags_owner_parent_name_key") {
		// The delete rolled back, so the colliding child is still a child.
		conflict := &TagNameConflictError{}
		if c, lerr := s.q.TagRootNameConflict(ctx, u); lerr == nil {
			conflict.ChildID, conflict.ChildName = idString(c.ID), c.Name
		}
		return conflict
	}
	return mapErr(err)
}

// Kinds of metadata rows, for inOwnerTx.
const (
	kindCorrespondent = iota
	kindDocumentType
	kindTag
)

// inOwnerTx reads the owner of the metadata row id of kind in the scope
// (ErrNotFound outside it), then runs fn in an owner transaction of that
// owner (ownerTx: its gate, then its write lock). fn checks the owner of
// the row it locks or writes with t.holds.
func (s *Store) inOwnerTx(ctx context.Context, sc Scope, id pgtype.UUID, kind int, fn func(context.Context, *writeTx) error) error {
	var owner int64
	var err error
	switch kind {
	case kindCorrespondent:
		owner, err = s.q.CorrespondentOwner(ctx, db.CorrespondentOwnerParams{ID: id, Owner: sc.owner()})
	case kindDocumentType:
		owner, err = s.q.DocumentTypeOwner(ctx, db.DocumentTypeOwnerParams{ID: id, Owner: sc.owner()})
	default:
		owner, err = s.q.TagOwner(ctx, db.TagOwnerParams{ID: id, Owner: sc.owner()})
	}
	if err != nil {
		return err
	}
	return s.ownerTx(ctx, []int64{owner}, fn)
}

// creatorFor returns the owner of a new row: the scope's user for 0 or
// that user's own ID, another user only in an admin's scope (All). In any
// other scope another owner names no row the scope may create
// (ErrNotFound). An owner that does not exist gives ErrUnknownUser from
// the insert.
func (sc Scope) creatorFor(owner int64) (int64, error) {
	if owner == 0 || owner == sc.UserID {
		return sc.creator()
	}
	if !sc.All || sc.UserID <= 0 || owner < 0 {
		return 0, ErrNotFound
	}
	return owner, nil
}

// auditRow completes the pending audit entry of ctx when it is about typ:
// the row as its target with its name, and the owner's ID when that is not
// the scope's user (an admin's change of another user's row).
func auditRow(ctx context.Context, sc Scope, typ, id, name string, owner int64) {
	e := auditFrom(ctx)
	if e == nil || e.TargetType != typ {
		return
	}
	e.TargetID, e.TargetName = id, name
	e.Detail("name", name)
	if owner != sc.UserID {
		e.Detail("ownerId", owner)
	}
}

// deleted turns the result of a delete into ErrNotFound when no row matched.
func deleted(n int64, err error) error {
	if err != nil {
		return mapErr(err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// afterLockKey carries a test seam: a function a patch, a bulk change or
// a metadata delete calls with the owner lock and its row locks held,
// before it writes (see export_test.go).
type afterLockKey struct{}

func afterLock(ctx context.Context) {
	if f, ok := ctx.Value(afterLockKey{}).(func()); ok {
		f()
	}
}

// CorrespondentPatch holds the fields of a correspondent to change; nil
// keeps the stored value.
type CorrespondentPatch struct {
	Name *string
}

// PatchCorrespondent changes only the fields set in p, with the audit entry
// of ctx (its target and, on a rename, oldName). The row is read and
// written in one transaction under the owner's write lock and a row lock,
// so a concurrent change of another field is never lost.
func (s *Store) PatchCorrespondent(ctx context.Context, sc Scope, id string, p CorrespondentPatch) (*Correspondent, error) {
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var out *Correspondent
	err := s.inOwnerTx(ctx, sc, u, kindCorrespondent, func(ctx context.Context, t *writeTx) error {
		q := t.q
		old, err := q.LockCorrespondent(ctx, db.LockCorrespondentParams{ID: u, Owner: sc.owner()})
		if err != nil {
			return err
		}
		if err := t.holds(old.OwnerID); err != nil {
			return err
		}
		afterLock(ctx)
		in := db.UpdateCorrespondentParams{
			ID: u, Owner: sc.owner(), Name: old.Name, MatchText: old.MatchText, MatchMode: old.MatchMode,
		}
		if p.Name != nil {
			in.Name = *p.Name
		}
		r, err := q.UpdateCorrespondent(ctx, in)
		if err != nil {
			return err
		}
		out = correspondentFromDB(r)
		auditPatch(ctx, sc, TargetCorrespondent, out.ID, old.Name, out.Name, out.OwnerID)
		return nil
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// DocumentTypePatch holds the fields of a document type to change; nil
// keeps the stored value.
type DocumentTypePatch struct {
	Name *string
}

// PatchDocumentType is PatchCorrespondent for a document type.
func (s *Store) PatchDocumentType(ctx context.Context, sc Scope, id string, p DocumentTypePatch) (*DocumentType, error) {
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var out *DocumentType
	err := s.inOwnerTx(ctx, sc, u, kindDocumentType, func(ctx context.Context, t *writeTx) error {
		q := t.q
		old, err := q.LockDocumentType(ctx, db.LockDocumentTypeParams{ID: u, Owner: sc.owner()})
		if err != nil {
			return err
		}
		if err := t.holds(old.OwnerID); err != nil {
			return err
		}
		afterLock(ctx)
		in := db.UpdateDocumentTypeParams{
			ID: u, Owner: sc.owner(), Name: old.Name, Symbol: old.Symbol, MatchText: old.MatchText, MatchMode: old.MatchMode,
		}
		if p.Name != nil {
			in.Name = *p.Name
		}
		r, err := q.UpdateDocumentType(ctx, in)
		if err != nil {
			return err
		}
		out = documentTypeFromDB(r)
		auditPatch(ctx, sc, TargetDocumentType, out.ID, old.Name, out.Name, out.OwnerID)
		return nil
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// TagPatch holds the fields of a tag to change; nil keeps the stored value.
// With SetParent, ParentID replaces the parent (nil makes a root tag).
type TagPatch struct {
	Name      *string
	ColorHex  *string
	SetParent bool
	ParentID  *string
}

// PatchTag is PatchCorrespondent for a tag. A parent that would make the
// tag its own ancestor gives ErrTagCycle; a parent of another owner, or one
// that does not exist, ErrOwnerMismatch.
func (s *Store) PatchTag(ctx context.Context, sc Scope, id string, p TagPatch) (*Tag, error) {
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var parent pgtype.UUID
	if p.SetParent {
		var err error
		if parent, err = parseLink(p.ParentID); err != nil {
			return nil, err
		}
	}
	var out *Tag
	err := s.inOwnerTx(ctx, sc, u, kindTag, func(ctx context.Context, t *writeTx) error {
		q := t.q
		old, err := q.LockTag(ctx, db.LockTagParams{ID: u, Owner: sc.owner()})
		if err != nil {
			return err
		}
		if err := t.holds(old.OwnerID); err != nil {
			return err
		}
		afterLock(ctx)
		in := db.UpdateTagParams{
			ID: u, Owner: sc.owner(), Name: old.Name, ColorHex: old.ColorHex, Symbol: old.Symbol,
			ParentID: old.ParentID, MatchText: old.MatchText, MatchMode: old.MatchMode,
		}
		if p.Name != nil {
			in.Name = *p.Name
		}
		if p.ColorHex != nil {
			in.ColorHex = *p.ColorHex
		}
		if p.SetParent {
			in.ParentID = parent
		}
		r, err := q.UpdateTag(ctx, in)
		if err != nil {
			return err
		}
		out = tagFromDB(r)
		auditPatch(ctx, sc, TargetTag, out.ID, old.Name, out.Name, out.OwnerID)
		return nil
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// auditPatch is auditRow for a patch, with oldName when the name changed.
func auditPatch(ctx context.Context, sc Scope, typ, id, oldName, name string, owner int64) {
	auditRow(ctx, sc, typ, id, name, owner)
	if e := auditFrom(ctx); e != nil && e.TargetType == typ && oldName != name {
		e.Detail("oldName", oldName)
	}
}

// CorrespondentCount is a correspondent with the number of its documents.
type CorrespondentCount struct {
	*Correspondent
	DocumentCount int64
}

// ListCorrespondentsWithCounts is ListCorrespondents with each one's
// number of documents.
func (s *Store) ListCorrespondentsWithCounts(ctx context.Context, sc Scope) ([]CorrespondentCount, error) {
	rows, err := s.q.ListCorrespondentsWithCounts(ctx, sc.owner())
	if err != nil {
		return nil, err
	}
	out := make([]CorrespondentCount, len(rows))
	for i, r := range rows {
		out[i] = CorrespondentCount{
			Correspondent: correspondentFromDB(db.Correspondent{
				ID: r.ID, OwnerID: r.OwnerID, Name: r.Name, MatchText: r.MatchText, MatchMode: r.MatchMode,
				CreatedAt: r.CreatedAt, ModifiedAt: r.ModifiedAt,
			}),
			DocumentCount: r.DocumentCount,
		}
	}
	return out, nil
}

// DocumentTypeCount is a document type with the number of its documents.
type DocumentTypeCount struct {
	*DocumentType
	DocumentCount int64
}

// ListDocumentTypesWithCounts is ListDocumentTypes with each one's number
// of documents.
func (s *Store) ListDocumentTypesWithCounts(ctx context.Context, sc Scope) ([]DocumentTypeCount, error) {
	rows, err := s.q.ListDocumentTypesWithCounts(ctx, sc.owner())
	if err != nil {
		return nil, err
	}
	out := make([]DocumentTypeCount, len(rows))
	for i, r := range rows {
		out[i] = DocumentTypeCount{
			DocumentType: documentTypeFromDB(db.DocumentType{
				ID: r.ID, OwnerID: r.OwnerID, Name: r.Name, Symbol: r.Symbol, MatchText: r.MatchText,
				MatchMode: r.MatchMode, CreatedAt: r.CreatedAt, ModifiedAt: r.ModifiedAt,
			}),
			DocumentCount: r.DocumentCount,
		}
	}
	return out, nil
}

// TagCount is a tag with the number of its own documents and the number of
// distinct documents of the tag and its descendants, which a tag filter
// lists.
type TagCount struct {
	*Tag
	DocumentCount int64
	SubtreeCount  int64
}

// ListTagsWithCounts is ListTags with each tag's counts.
func (s *Store) ListTagsWithCounts(ctx context.Context, sc Scope) ([]TagCount, error) {
	rows, err := s.q.ListTagsWithCounts(ctx, sc.owner())
	if err != nil {
		return nil, err
	}
	out := make([]TagCount, len(rows))
	for i, r := range rows {
		out[i] = TagCount{
			Tag: tagFromDB(db.Tag{
				ID: r.ID, OwnerID: r.OwnerID, Name: r.Name, ColorHex: r.ColorHex, Symbol: r.Symbol,
				ParentID: r.ParentID, MatchText: r.MatchText, MatchMode: r.MatchMode,
				CreatedAt: r.CreatedAt, ModifiedAt: r.ModifiedAt,
			}),
			DocumentCount: r.DocumentCount, SubtreeCount: r.SubtreeCount,
		}
	}
	return out, nil
}

// DocumentCounts are the numbers of the scope's documents for the sidebar.
type DocumentCounts struct {
	Inbox, Favorites, Total int64
}

// CountDocuments counts the scope's documents: in the inbox, favourites
// and all.
func (s *Store) CountDocuments(ctx context.Context, sc Scope) (DocumentCounts, error) {
	r, err := s.q.DocumentCounts(ctx, sc.owner())
	if err != nil {
		return DocumentCounts{}, err
	}
	return DocumentCounts{Inbox: r.Inbox, Favorites: r.Favorites, Total: r.Total}, nil
}
