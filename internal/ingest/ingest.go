// Package ingest imports one file as a new document: it detects the type
// from the content, enforces the size and pixel limits, stores the file
// durably in the blob store and only then inserts the document row. The
// upload API, the consume folder and the CLI import all go through it.
package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/pklnx/paperless-go/internal/blobstore"
	"github.com/pklnx/paperless-go/internal/store"
)

// Limits on names derived from the file name.
const (
	maxTitleRunes    = 200
	maxFileNameBytes = 255
)

// Store is the part of *store.Store the importer uses.
type Store interface {
	FindDocumentByOriginal(ctx context.Context, ownerID int64, hash string) (*store.DocumentRef, error)
	CreateDocument(ctx context.Context, sc store.Scope, in store.NewDocument) (*store.Document, error)
}

// Blobs is the part of *blobstore.Store the importer uses. It never
// removes a blob: another owner's document may share it, and unreferenced
// blobs are left for the orphan sweep.
type Blobs interface {
	Put(r io.Reader) (blobstore.Blob, bool, error)
}

// Importer imports files. It is safe for concurrent use.
type Importer struct {
	store   Store
	blobs   Blobs
	maxSize int64
}

// New returns an importer that refuses files larger than maxSize bytes.
func New(st Store, blobs Blobs, maxSize int64) (*Importer, error) {
	if maxSize <= 0 {
		return nil, fmt.Errorf("ingest: size limit must be positive, got %d", maxSize)
	}
	return &Importer{store: st, blobs: blobs, maxSize: maxSize}, nil
}

// MaxSize is the size limit in bytes.
func (im *Importer) MaxSize() int64 { return im.maxSize }

// Request is one file to import.
type Request struct {
	// OwnerID owns the new document and is the only owner the duplicate
	// check looks at (also for admins).
	OwnerID int64
	// FileName is the file's name as given (a path is reduced to its base
	// name). It may be empty.
	FileName string
	// Title, if not empty, replaces the title derived from FileName.
	Title string
	// Body is the file's content. It is read once, up to one byte past the
	// size limit.
	Body io.Reader
}

// Outcome is what happened to a file.
type Outcome int

// Outcomes. A transient failure (database, blob store) is not an outcome
// but the error of Import: the same file may succeed later.
const (
	// Imported: a new document was created.
	Imported Outcome = iota + 1
	// Duplicate: the owner already has a document with this file.
	Duplicate
	// Refused: the file itself is not acceptable; see Result.Reason.
	Refused
)

func (o Outcome) String() string {
	switch o {
	case Imported:
		return "imported"
	case Duplicate:
		return "duplicate"
	case Refused:
		return "refused"
	}
	return fmt.Sprintf("Outcome(%d)", int(o))
}

// Result is the outcome of an import.
type Result struct {
	Outcome Outcome
	// Document is the new document (Imported).
	Document *store.Document
	// Existing is the owner's document that already has the file
	// (Duplicate). Its ID is empty in the rare case that the document was
	// deleted between the collision and the lookup.
	Existing store.DocumentRef
	// Refusal says why the file was refused (Refused).
	Refusal *RefusedError
}

// errTooLarge marks a body that ran past the size limit.
var errTooLarge = errors.New("ingest: size limit exceeded")

// limitReader reads at most n bytes and fails with errTooLarge as soon as
// the source has more.
type limitReader struct {
	r io.Reader
	n int64 // bytes still allowed
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.n < 0 {
		return 0, errTooLarge
	}
	// Read one byte past the limit, to tell "exactly at the limit" from
	// "more". Written without l.n+1, which overflows for a limit of
	// math.MaxInt64.
	if int64(len(p))-1 > l.n {
		p = p[:l.n+1]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	if l.n < 0 {
		return n + int(l.n), errTooLarge
	}
	return n, err
}

// Import imports one file for req.OwnerID, in this order, each step
// finished durably before the next:
//
//  1. Read a prefix of up to PrefixSize bytes and detect the type and the
//     pixel limit from it; a refused file stores nothing.
//  2. Stream the whole file through the size limit into the blob store.
//  3. If the owner already has a document with that file, stop with
//     Duplicate.
//  4. Create the document (in the inbox). A concurrent import of the same
//     file that wins the insert also gives Duplicate.
//
// A refused file gives a Result with Outcome Refused and no error. A
// non-nil error is transient (database, blob store, reading the body): no
// document was created, and a retry may succeed.
//
// An audit entry in ctx (store.WithAudit) is written in the transaction
// that creates the document; a duplicate or refused file writes none.
func (im *Importer) Import(ctx context.Context, req Request) (Result, error) {
	body := &limitReader{r: req.Body, n: im.maxSize}
	prefix := make([]byte, PrefixSize)
	n, err := io.ReadFull(body, prefix)
	prefix = prefix[:n]
	switch {
	case errors.Is(err, errTooLarge):
		return im.tooLarge(), nil
	case err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF):
		return Result{}, fmt.Errorf("ingest: read: %w", err)
	}

	typ, err := Detect(prefix)
	if err != nil {
		var r *RefusedError
		if errors.As(err, &r) {
			return Result{Outcome: Refused, Refusal: r}, nil
		}
		return Result{}, err
	}

	blob, _, err := im.blobs.Put(io.MultiReader(bytes.NewReader(prefix), body))
	if errors.Is(err, errTooLarge) {
		return im.tooLarge(), nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("ingest: store file: %w", err)
	}

	existing, err := im.store.FindDocumentByOriginal(ctx, req.OwnerID, blob.SHA256)
	switch {
	case err == nil:
		return Result{Outcome: Duplicate, Existing: *existing}, nil
	case !errors.Is(err, store.ErrNotFound):
		return Result{}, fmt.Errorf("ingest: duplicate check: %w", err)
	}

	fileName := FileName(req.FileName)
	title := Title(req.Title)
	if title == "" {
		title = TitleFromFileName(fileName)
	}
	doc, err := im.store.CreateDocument(ctx, store.Scope{UserID: req.OwnerID}, store.NewDocument{
		Title:            title,
		IsInbox:          true,
		Source:           typ.Source,
		OriginalFileName: fileName,
		OriginalSHA256:   blob.SHA256,
		OriginalSize:     blob.Size,
		OriginalMIME:     typ.MIME,
	})
	var dup *store.DuplicateError
	if errors.As(err, &dup) {
		res := Result{Outcome: Duplicate, Existing: store.DocumentRef{ID: dup.ExistingID}}
		if ref, lerr := im.store.FindDocumentByOriginal(ctx, req.OwnerID, blob.SHA256); lerr == nil {
			res.Existing = *ref
		}
		return res, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("ingest: create document: %w", err)
	}
	return Result{Outcome: Imported, Document: doc}, nil
}

func (im *Importer) tooLarge() Result {
	return Result{Outcome: Refused, Refusal: &RefusedError{
		Reason: ReasonTooLarge,
		Detail: fmt.Sprintf("larger than %d bytes", im.maxSize),
	}}
}

// clean makes s storable and displayable: valid UTF-8 (invalid bytes
// become U+FFFD), NFC, without control characters.
func clean(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return norm.NFC.String(s)
}

// FileName reduces name to the base name of a path (split on / and \),
// cleaned (valid UTF-8, NFC, no control characters) and cut to at most
// 255 bytes on a character boundary. "." and ".." become empty.
func FileName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(clean(name))
	if name == "." || name == ".." {
		return ""
	}
	for len(name) > maxFileNameBytes {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}

// TitleFromFileName returns the title for a file name: the base name
// without its extension (the whole name if nothing else is left), cleaned
// and cut to at most 200 characters.
func TitleFromFileName(name string) string {
	name = FileName(name)
	if stem := strings.TrimSuffix(name, filepath.Ext(name)); strings.TrimSpace(stem) != "" {
		name = stem
	}
	return Title(name)
}

// Title cleans a title: valid UTF-8, NFC, no control characters, trimmed,
// at most 200 characters.
func Title(s string) string {
	s = strings.TrimSpace(clean(s))
	if utf8.RuneCountInString(s) > maxTitleRunes {
		s = strings.TrimSpace(string([]rune(s)[:maxTitleRunes]))
	}
	return s
}
