package store

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pklnx/paperless-go/internal/store/db"
)

// Document sources, as in the app.
const (
	SourceFile  = "file"
	SourceScan  = "scan"
	SourceImage = "image"
)

// Document is one stored document. Its files live in the blob store, named
// by their SHA-256: the original upload and, once OCR has run, the archive
// PDF with a text layer.
type Document struct {
	ID                  string
	OwnerID             int64
	Title               string
	DocumentDate        time.Time // issue date, for example the invoice date
	CreatedAt           time.Time
	ModifiedAt          time.Time
	Notes               string
	IsFavorite          bool
	IsInbox             bool // false unless set; uploads set it (the column defaults to true)
	ArchiveSerialNumber *int32
	PageCount           int32
	ExtractedText       string
	TextSource          string
	Source              string // SourceFile, SourceScan or SourceImage
	OriginalFileName    string
	OriginalSHA256      string
	OriginalSize        int64
	OriginalMIME        string
	ArchiveSHA256       *string // nil until OCR has run
	ArchiveSize         *int64
	ThumbnailSHA256     *string // nil until processing has made one (#5, part 4)
	ProcessingNote      string  // how processing went, for example why OCR was skipped
	CorrespondentID     *string
	DocumentTypeID      *string
	TagIDs              []string
}

// NewDocument holds the fields of a new document. A zero DocumentDate means
// now. The original file must already be stored durably.
type NewDocument struct {
	Title               string
	DocumentDate        time.Time
	Notes               string
	IsFavorite          bool
	IsInbox             bool
	ArchiveSerialNumber *int32
	PageCount           int32
	ExtractedText       string
	TextSource          string
	Source              string
	OriginalFileName    string
	OriginalSHA256      string
	OriginalSize        int64
	OriginalMIME        string
	ArchiveSHA256       *string
	ArchiveSize         *int64
	CorrespondentID     *string
	DocumentTypeID      *string
	TagIDs              []string
}

func documentFromDB(r db.GetDocumentRow) *Document {
	tags := make([]string, len(r.TagIds))
	for i, t := range r.TagIds {
		tags[i] = idString(t)
	}
	return &Document{
		ID: idString(r.ID), OwnerID: r.OwnerID, Title: r.Title, DocumentDate: r.DocumentDate,
		CreatedAt: r.CreatedAt, ModifiedAt: r.ModifiedAt, Notes: r.Notes, IsFavorite: r.IsFavorite,
		IsInbox: r.IsInbox, ArchiveSerialNumber: r.ArchiveSerialNumber, PageCount: r.PageCount,
		ExtractedText: r.ExtractedText, TextSource: r.TextSource, Source: r.Source,
		OriginalFileName: r.OriginalFileName, OriginalSHA256: r.OriginalSha256,
		OriginalSize: r.OriginalSize, OriginalMIME: r.OriginalMime, ArchiveSHA256: r.ArchiveSha256,
		ArchiveSize: r.ArchiveSize, ThumbnailSHA256: r.ThumbnailSha256, ProcessingNote: r.ProcessingNote,
		CorrespondentID: idPtr(r.CorrespondentID), DocumentTypeID: idPtr(r.DocumentTypeID), TagIDs: tags,
	}
}

// DocumentRef names a document.
type DocumentRef struct {
	ID    string
	Title string
}

// isSHA256 reports whether s is a lowercase hex SHA-256, as the blob store
// names its files.
func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// validText reports whether s can be stored in a TEXT column: valid UTF-8
// without NUL.
func validText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }

// validate checks a new document before it reaches the database. A refused
// field gives a *ValidationError (ErrInvalid) naming it.
func (in *NewDocument) validate() error {
	for _, f := range []struct{ name, v string }{
		{"title", in.Title}, {"notes", in.Notes}, {"extracted_text", in.ExtractedText},
		{"text_source", in.TextSource}, {"original_file_name", in.OriginalFileName},
		{"original_mime", in.OriginalMIME},
	} {
		if !validText(f.v) {
			return invalid(f.name)
		}
	}
	switch {
	case in.Source != SourceFile && in.Source != SourceScan && in.Source != SourceImage:
		return invalid("source")
	case !isSHA256(in.OriginalSHA256):
		return invalid("original_sha256")
	case in.OriginalSize < 0:
		return invalid("original_size")
	case strings.TrimSpace(in.OriginalMIME) == "":
		return invalid("original_mime")
	case in.PageCount < 0:
		return invalid("page_count")
	case in.ArchiveSerialNumber != nil && *in.ArchiveSerialNumber <= 0:
		return invalid("archive_serial_number")
	case (in.ArchiveSHA256 == nil) != (in.ArchiveSize == nil):
		return invalid("archive_size")
	case in.ArchiveSHA256 != nil && !isSHA256(*in.ArchiveSHA256):
		return invalid("archive_sha256")
	case in.ArchiveSize != nil && *in.ArchiveSize < 0:
		return invalid("archive_size")
	}
	return nil
}

// CreateDocument adds a document owned by the scope's user, with its tags.
// The input is validated first: a refused field gives a *ValidationError
// (ErrInvalid). A document without an archive PDF is queued for processing
// in the same transaction, so no committed document is ever left
// unprocessed. When the owner already has a document with the same
// original file, the error is a *DuplicateError (ErrDuplicate); other
// owners' documents do not count. A linked tag, correspondent or document
// type that is not the owner's gives ErrOwnerMismatch; a taken ASN gives
// ErrASNTaken; an owner that does not exist gives ErrUnknownUser. An entry
// from WithAudit is written in the same transaction; a document target
// without an ID gets the new document's. The transaction takes the
// owner's gate and write lock first (ownerTx).
func (s *Store) CreateDocument(ctx context.Context, sc Scope, in NewDocument) (*Document, error) {
	owner, err := sc.creator()
	if err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	corr, err := parseLink(in.CorrespondentID)
	if err != nil {
		return nil, err
	}
	typ, err := parseLink(in.DocumentTypeID)
	if err != nil {
		return nil, err
	}
	tags, err := parseLinks(in.TagIDs)
	if err != nil {
		return nil, err
	}
	date := in.DocumentDate
	if date.IsZero() {
		date = time.Now()
	}

	var id pgtype.UUID
	err = s.ownerTx(ctx, []int64{owner}, func(ctx context.Context, t *writeTx) error {
		q := t.q
		var err error
		id, err = q.CreateDocument(ctx, db.CreateDocumentParams{
			OwnerID: owner, Title: in.Title, DocumentDate: date, Notes: in.Notes,
			IsFavorite: in.IsFavorite, IsInbox: in.IsInbox, ArchiveSerialNumber: in.ArchiveSerialNumber,
			PageCount: in.PageCount, ExtractedText: in.ExtractedText, TextSource: in.TextSource,
			Source: in.Source, OriginalFileName: in.OriginalFileName, OriginalSha256: in.OriginalSHA256,
			OriginalSize: in.OriginalSize, OriginalMime: in.OriginalMIME, ArchiveSha256: in.ArchiveSHA256,
			ArchiveSize: in.ArchiveSize, CorrespondentID: corr, DocumentTypeID: typ,
		})
		if err != nil {
			return err
		}
		fillAuditTargetID(ctx, TargetDocument, idString(id))
		if len(tags) > 0 {
			if err := q.AddDocumentTags(ctx, db.AddDocumentTagsParams{OwnerID: owner, DocumentID: id, TagIds: tags}); err != nil {
				return err
			}
		}
		if in.ArchiveSHA256 == nil {
			_, err := enqueue(ctx, q, []pgtype.UUID{id})
			return err
		}
		return nil
	})
	if isUniqueViolation(err, "documents_owner_original_sha256_key") {
		dup := &DuplicateError{}
		if existing, lerr := s.FindDocumentByOriginal(ctx, owner, in.OriginalSHA256); lerr == nil {
			dup.ExistingID = existing.ID
		}
		return nil, dup
	}
	if err != nil {
		return nil, mapErr(err)
	}
	return s.GetDocument(ctx, Scope{UserID: owner}, idString(id))
}

// FindDocumentByOriginal returns the document of ownerID whose original file
// has the SHA-256 hash, or ErrNotFound. It looks only at that owner's
// documents, never through a scope: an admin's duplicate check must not
// find another user's document. A malformed hash gives a *ValidationError
// (ErrInvalid).
func (s *Store) FindDocumentByOriginal(ctx context.Context, ownerID int64, hash string) (*DocumentRef, error) {
	if !isSHA256(hash) {
		return nil, invalid("original_sha256")
	}
	r, err := s.q.DocumentByOriginal(ctx, db.DocumentByOriginalParams{OwnerID: ownerID, OriginalSha256: hash})
	if err != nil {
		return nil, mapErr(err)
	}
	return &DocumentRef{ID: idString(r.ID), Title: r.Title}, nil
}

// GetDocument returns one document in the scope, with its tags.
func (s *Store) GetDocument(ctx context.Context, sc Scope, id string) (*Document, error) {
	u, ok := parseID(id)
	if !ok {
		return nil, ErrNotFound
	}
	r, err := s.q.GetDocument(ctx, db.GetDocumentParams{ID: u, Owner: sc.owner()})
	if err != nil {
		return nil, mapErr(err)
	}
	return documentFromDB(r), nil
}

// ListDocuments returns the scope's documents, newest first.
func (s *Store) ListDocuments(ctx context.Context, sc Scope) ([]*Document, error) {
	rows, err := s.q.ListDocuments(ctx, sc.owner())
	if err != nil {
		return nil, err
	}
	out := make([]*Document, len(rows))
	for i, r := range rows {
		out[i] = documentFromDB(db.GetDocumentRow(r))
	}
	return out, nil
}

// DeleteDocument removes a document row with its tag links. Its files stay
// in the blob store; the orphan sweep comes with the trash (#12). It reads
// the owner, takes the owner's gate and write lock (ownerTx), then
// deletes.
func (s *Store) DeleteDocument(ctx context.Context, sc Scope, id string) error {
	u, ok := parseID(id)
	if !ok {
		return ErrNotFound
	}
	owner, err := s.q.DocumentOwner(ctx, db.DocumentOwnerParams{ID: u, Owner: sc.owner()})
	if err != nil {
		return mapErr(err)
	}
	return mapErr(s.ownerTx(ctx, []int64{owner}, func(ctx context.Context, t *writeTx) error {
		got, err := t.q.DeleteDocument(ctx, db.DeleteDocumentParams{ID: u, Owner: sc.owner()})
		if err != nil {
			return err
		}
		return t.holds(got)
	}))
}
