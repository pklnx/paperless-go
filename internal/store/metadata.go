package store

import (
	"context"
	"time"

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
// An empty MatchMode means DefaultMatchMode.
type CorrespondentInput struct {
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

// CreateCorrespondent adds a correspondent owned by the scope's user.
func (s *Store) CreateCorrespondent(ctx context.Context, sc Scope, in CorrespondentInput) (*Correspondent, error) {
	owner, err := sc.creator()
	if err != nil {
		return nil, err
	}
	r, err := s.q.CreateCorrespondent(ctx, db.CreateCorrespondentParams{
		OwnerID: owner, Name: in.Name, MatchText: in.MatchText, MatchMode: orDefault(in.MatchMode, DefaultMatchMode),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return correspondentFromDB(r), nil
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

// UpdateCorrespondent replaces a correspondent's fields.
func (s *Store) UpdateCorrespondent(ctx context.Context, sc Scope, id string, in CorrespondentInput) (*Correspondent, error) {
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	r, err := s.q.UpdateCorrespondent(ctx, db.UpdateCorrespondentParams{
		ID: u, Owner: sc.owner(), Name: in.Name, MatchText: in.MatchText,
		MatchMode: orDefault(in.MatchMode, DefaultMatchMode),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return correspondentFromDB(r), nil
}

// DeleteCorrespondent removes a correspondent; its documents keep existing
// without one.
func (s *Store) DeleteCorrespondent(ctx context.Context, sc Scope, id string) error {
	u, ok := parseID(id)
	if !ok {
		return ErrNotFound
	}
	n, err := s.q.DeleteCorrespondent(ctx, db.DeleteCorrespondentParams{ID: u, Owner: sc.owner()})
	return deleted(n, err)
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
// Empty Symbol and MatchMode mean the defaults.
type DocumentTypeInput struct {
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

// CreateDocumentType adds a document type owned by the scope's user.
func (s *Store) CreateDocumentType(ctx context.Context, sc Scope, in DocumentTypeInput) (*DocumentType, error) {
	owner, err := sc.creator()
	if err != nil {
		return nil, err
	}
	r, err := s.q.CreateDocumentType(ctx, db.CreateDocumentTypeParams{
		OwnerID: owner, Name: in.Name, Symbol: orDefault(in.Symbol, DefaultDocumentTypeSymbol),
		MatchText: in.MatchText, MatchMode: orDefault(in.MatchMode, DefaultMatchMode),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return documentTypeFromDB(r), nil
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

// UpdateDocumentType replaces a document type's fields.
func (s *Store) UpdateDocumentType(ctx context.Context, sc Scope, id string, in DocumentTypeInput) (*DocumentType, error) {
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	r, err := s.q.UpdateDocumentType(ctx, db.UpdateDocumentTypeParams{
		ID: u, Owner: sc.owner(), Name: in.Name, Symbol: orDefault(in.Symbol, DefaultDocumentTypeSymbol),
		MatchText: in.MatchText, MatchMode: orDefault(in.MatchMode, DefaultMatchMode),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return documentTypeFromDB(r), nil
}

// DeleteDocumentType removes a document type; its documents keep existing
// without one.
func (s *Store) DeleteDocumentType(ctx context.Context, sc Scope, id string) error {
	u, ok := parseID(id)
	if !ok {
		return ErrNotFound
	}
	n, err := s.q.DeleteDocumentType(ctx, db.DeleteDocumentTypeParams{ID: u, Owner: sc.owner()})
	return deleted(n, err)
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
type TagInput struct {
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

// CreateTag adds a tag owned by the scope's user. A parent of another owner
// gives ErrOwnerMismatch.
func (s *Store) CreateTag(ctx context.Context, sc Scope, in TagInput) (*Tag, error) {
	owner, err := sc.creator()
	if err != nil {
		return nil, err
	}
	parent, err := parseLink(in.ParentID)
	if err != nil {
		return nil, err
	}
	r, err := s.q.CreateTag(ctx, db.CreateTagParams{
		OwnerID: owner, Name: in.Name, ColorHex: orDefault(in.ColorHex, DefaultTagColor),
		Symbol: orDefault(in.Symbol, DefaultTagSymbol), ParentID: parent,
		MatchText: in.MatchText, MatchMode: orDefault(in.MatchMode, DefaultMatchMode),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return tagFromDB(r), nil
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
// owner gives ErrOwnerMismatch.
func (s *Store) UpdateTag(ctx context.Context, sc Scope, id string, in TagInput) (*Tag, error) {
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	parent, err := parseLink(in.ParentID)
	if err != nil {
		return nil, err
	}
	r, err := s.q.UpdateTag(ctx, db.UpdateTagParams{
		ID: u, Owner: sc.owner(), Name: in.Name, ColorHex: orDefault(in.ColorHex, DefaultTagColor),
		Symbol: orDefault(in.Symbol, DefaultTagSymbol), ParentID: parent,
		MatchText: in.MatchText, MatchMode: orDefault(in.MatchMode, DefaultMatchMode),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return tagFromDB(r), nil
}

// DeleteTag removes a tag. Its children become root tags and its documents
// lose the tag. When a child would then collide with a root tag of the same
// name, nothing changes and the error is a *TagNameConflictError
// (ErrTagNameConflict).
func (s *Store) DeleteTag(ctx context.Context, sc Scope, id string) error {
	u, ok := parseID(id)
	if !ok {
		return ErrNotFound
	}
	n, err := s.q.DeleteTag(ctx, db.DeleteTagParams{ID: u, Owner: sc.owner()})
	if isUniqueViolation(err, "tags_owner_parent_name_key") {
		// The delete rolled back, so the colliding child is still a child.
		conflict := &TagNameConflictError{}
		if c, lerr := s.q.TagRootNameConflict(ctx, u); lerr == nil {
			conflict.ChildID, conflict.ChildName = idString(c.ID), c.Name
		}
		return conflict
	}
	return deleted(n, err)
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
