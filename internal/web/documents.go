package web

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/pklnx/paperless-go/internal/ingest"
	"github.com/pklnx/paperless-go/internal/store"
)

// Uploads and downloads may take longer than the server's read and write
// timeouts (5 minutes), which are meant for every other request: a
// 100 MiB file over a slow line needs more. These routes extend their own
// deadlines.
const transferTimeout = time.Hour

// fileNameHeader carries the uploaded file's name, percent-encoded (a
// header value cannot hold every character of a name).
const fileNameHeader = "X-File-Name"

// documentJSON is a document as the API lists it. The extracted text is
// only in documentDetailJSON: it can be large.
type documentJSON struct {
	ID                  string    `json:"id"`
	Title               string    `json:"title"`
	DocumentDate        time.Time `json:"documentDate"`
	CreatedAt           time.Time `json:"createdAt"`
	ModifiedAt          time.Time `json:"modifiedAt"`
	Notes               string    `json:"notes"`
	IsFavorite          bool      `json:"isFavorite"`
	IsInbox             bool      `json:"isInbox"`
	ArchiveSerialNumber *int32    `json:"archiveSerialNumber"`
	PageCount           int32     `json:"pageCount"`
	Source              string    `json:"source"`
	OriginalFileName    string    `json:"originalFileName"`
	OriginalSize        int64     `json:"originalSize"`
	OriginalMIME        string    `json:"originalMime"`
	// HasArchive: the archive PDF exists (processing has run).
	HasArchive      bool     `json:"hasArchive"`
	ArchiveSize     *int64   `json:"archiveSize"`
	ProcessingNote  string   `json:"processingNote"`
	CorrespondentID *string  `json:"correspondentId"`
	DocumentTypeID  *string  `json:"documentTypeId"`
	TagIDs          []string `json:"tagIds"`
}

type documentDetailJSON struct {
	documentJSON
	ExtractedText string `json:"extractedText"`
	TextSource    string `json:"textSource"`
}

func toDocumentJSON(d *store.Document) documentJSON {
	tags := d.TagIDs
	if tags == nil {
		tags = []string{}
	}
	return documentJSON{
		ID: d.ID, Title: d.Title, DocumentDate: d.DocumentDate, CreatedAt: d.CreatedAt, ModifiedAt: d.ModifiedAt,
		Notes: d.Notes, IsFavorite: d.IsFavorite, IsInbox: d.IsInbox, ArchiveSerialNumber: d.ArchiveSerialNumber,
		PageCount: d.PageCount, Source: d.Source, OriginalFileName: d.OriginalFileName, OriginalSize: d.OriginalSize,
		OriginalMIME: d.OriginalMIME, HasArchive: d.ArchiveSHA256 != nil, ArchiveSize: d.ArchiveSize,
		ProcessingNote: d.ProcessingNote, CorrespondentID: d.CorrespondentID, DocumentTypeID: d.DocumentTypeID, TagIDs: tags,
	}
}

// documentsAvailable answers 503 when the server has no blob store (only
// in tests that do not need documents).
func (s *Server) documentsAvailable(w http.ResponseWriter, r *http.Request) bool {
	if s.importer == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "documents are not available", errors.New("web: no blob store configured"))
		return false
	}
	return true
}

type documentListJSON struct {
	Documents []documentJSON `json:"documents"`
	// MaxUploadSize is the upload limit in bytes, so the UI can refuse a
	// larger file before sending it.
	MaxUploadSize int64 `json:"maxUploadSize"`
}

// handleListDocuments lists the scope's documents, newest first. Paging
// and filters come with the document list (#7).
func (s *Server) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	if !s.documentsAvailable(w, r) {
		return
	}
	docs, err := s.store.ListDocuments(r.Context(), requestScope(r))
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := documentListJSON{Documents: make([]documentJSON, len(docs)), MaxUploadSize: s.importer.MaxSize()}
	for i, d := range docs {
		out.Documents[i] = toDocumentJSON(d)
	}
	s.writeJSON(w, http.StatusOK, out)
}

// handleGetDocument returns one document of the scope; another user's
// answers 404 like a missing one.
func (s *Server) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	if !s.documentsAvailable(w, r) {
		return
	}
	d, err := s.store.GetDocument(r.Context(), requestScope(r), r.PathValue("id"))
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, documentDetailJSON{documentJSON: toDocumentJSON(d), ExtractedText: d.ExtractedText, TextSource: d.TextSource})
}

// handleDocumentFile sends a document's original file (kind=original, the
// default) or its archive PDF (kind=archive; 404 until processing made
// one) as a download. The file comes from the blob store by its hash, is
// never shown inline and runs in a sandbox if a browser renders it
// anyway.
func (s *Server) handleDocumentFile(w http.ResponseWriter, r *http.Request) {
	if !s.documentsAvailable(w, r) {
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "" && kind != "original" && kind != "archive" {
		s.fail(w, r, http.StatusBadRequest, "kind must be original or archive", nil)
		return
	}
	d, err := s.store.GetDocument(r.Context(), requestScope(r), r.PathValue("id"))
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	hash, mimeType, name := d.OriginalSHA256, d.OriginalMIME, downloadName(d, false)
	if kind == "archive" {
		if d.ArchiveSHA256 == nil {
			s.fail(w, r, http.StatusNotFound, "this document has no archive PDF yet", nil)
			return
		}
		hash, mimeType, name = *d.ArchiveSHA256, ingest.MIMEPDF, downloadName(d, true)
	}
	f, err := s.blobs.OpenHash(hash)
	if err != nil {
		// A row whose file is missing: the data directory is damaged.
		s.fail(w, r, http.StatusInternalServerError, "internal error", fmt.Errorf("open the file of document %s: %w", d.ID, err))
		return
	}
	defer func() { _ = f.Close() }()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(transferTimeout))
	h := w.Header()
	h.Set("Content-Type", mimeType)
	h.Set("Content-Disposition", contentDisposition(name))
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("Cache-Control", "private, no-store")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	// No modification time: the content behind a hash never changes, and
	// conditional requests are not worth the edge cases.
	http.ServeContent(w, r, "", time.Time{}, f)
}

// extensions gives a download without a stored file name an extension.
var extensions = map[string]string{
	ingest.MIMEPDF: ".pdf", ingest.MIMEJPEG: ".jpg", ingest.MIMEPNG: ".png", ingest.MIMEHEIC: ".heic", ingest.MIMEHEIF: ".heif",
}

// downloadName is the file name a download is offered under: the original
// file name, or the title with the type's extension; the archive PDF gets
// the same stem with .pdf.
func downloadName(d *store.Document, archive bool) string {
	name := d.OriginalFileName
	if name == "" {
		// A title may contain anything, also slashes.
		name = ingest.FileName(strings.NewReplacer("/", "_", `\`, "_").Replace(d.Title))
		if name == "" {
			name = "document"
		}
		name += extensions[d.OriginalMIME]
	}
	if archive {
		if stem := strings.TrimSuffix(name, filepath.Ext(name)); stem != "" {
			name = stem
		}
		name += ".pdf"
	}
	return name
}

// contentDisposition is an attachment header for name after RFC 6266: a
// plain ASCII filename (quotes and backslashes escaped, other characters
// replaced) for old clients, and the exact name as filename* in UTF-8.
func contentDisposition(name string) string {
	var ascii, ext strings.Builder
	for _, r := range name {
		switch {
		case r == '"' || r == '\\':
			ascii.WriteByte('\\')
			ascii.WriteRune(r)
		case r < 0x20 || r == 0x7f:
		case r > 0x7e:
			ascii.WriteByte('_')
		default:
			ascii.WriteRune(r)
		}
	}
	for _, b := range []byte(name) {
		if isAttrChar(b) {
			ext.WriteByte(b)
		} else {
			fmt.Fprintf(&ext, "%%%02X", b)
		}
	}
	return `attachment; filename="` + ascii.String() + `"; filename*=UTF-8''` + ext.String()
}

// isAttrChar reports whether b may appear unencoded in an RFC 8187 value.
func isAttrChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", b) >= 0
}

// uploadFileName decodes the X-File-Name header: percent-decoded, then
// reduced to a clean base name (ingest.FileName). An empty result means no
// name. A malformed encoding gives an error.
func uploadFileName(header string) (string, error) {
	name, err := url.PathUnescape(header)
	if err != nil {
		return "", err
	}
	return ingest.FileName(name), nil
}

// bodyReader remembers the first error reading the request body gave, to
// tell a broken upload (the client's side) from a failing server.
type bodyReader struct {
	r   io.Reader
	err error
}

func (b *bodyReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && b.err == nil {
		b.err = err
	}
	return n, err
}

type duplicateJSON struct {
	Duplicate bool `json:"duplicate"`
	// Existing is omitted when the existing document could not be named.
	Existing *documentRefJSON `json:"existing,omitempty"`
}

type documentRefJSON struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type refusedJSON struct {
	Error  string        `json:"error"`
	Reason ingest.Reason `json:"reason"`
}

// Answers to a refused upload, by reason.
var refusals = map[ingest.Reason]struct {
	status int
	msg    string
}{
	ingest.ReasonTooLarge:      {http.StatusRequestEntityTooLarge, "the file is larger than the upload limit"},
	ingest.ReasonUnsupported:   {http.StatusUnsupportedMediaType, "the file is not a PDF, JPEG, PNG or HEIC image"},
	ingest.ReasonUnreadable:    {http.StatusUnprocessableEntity, "the file cannot be read"},
	ingest.ReasonTooManyPixels: {http.StatusUnprocessableEntity, "the image has more than 50 megapixels"},
}

func (s *Server) refuseUpload(w http.ResponseWriter, reason ingest.Reason) {
	a, ok := refusals[reason]
	if !ok {
		a.status, a.msg = http.StatusUnprocessableEntity, "the file cannot be imported"
	}
	s.writeJSON(w, a.status, refusedJSON{Error: a.msg, Reason: reason})
}

// handleUpload imports the request body as a new document of the caller,
// also for an admin. The body is the raw file (see uploadContentTypes),
// the name is in X-File-Name. Answers: 201 with the document, 200 with
// duplicate (and the existing document, if it can be named), 413, 415 or
// 422 with the reason of a refusal.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !s.documentsAvailable(w, r) {
		return
	}
	maxSize := s.importer.MaxSize()
	if r.ContentLength > maxSize {
		s.refuseUpload(w, ingest.ReasonTooLarge)
		return
	}
	name, err := uploadFileName(r.Header.Get(fileNameHeader))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "the file name in "+fileNameHeader+" must be percent-encoded", nil)
		return
	}
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(transferTimeout)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)

	// The importer reads at most one byte past the limit; the cap is a
	// second line, and closes the connection of a client that sends more.
	limit := maxSize
	if limit < math.MaxInt64 {
		limit++
	}
	body := &bodyReader{r: http.MaxBytesReader(w, r.Body, limit)}
	title := ingest.TitleFromFileName(name)
	if title == "" {
		title = "upload-" + s.now().UTC().Format("20060102-150405")
	}
	sc := requestScope(r)
	e := s.auditEntry(r, "document.create")
	e.SetTarget(store.TargetDocument, "", title)
	e.Detail("via", "upload")
	res, err := s.importer.Import(withAudit(r, e), ingest.Request{OwnerID: sc.UserID, FileName: name, Title: title, Body: body})
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		s.refuseUpload(w, ingest.ReasonTooLarge)
		return
	case err != nil && body.err != nil:
		s.log.Info("upload interrupted", "err", body.err)
		s.fail(w, r, http.StatusBadRequest, "the upload was interrupted", nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	switch res.Outcome {
	case ingest.Imported:
		w.Header().Set("Location", "/api/documents/"+res.Document.ID)
		s.writeJSON(w, http.StatusCreated, toDocumentJSON(res.Document))
	case ingest.Duplicate:
		out := duplicateJSON{Duplicate: true}
		if res.Existing.ID != "" {
			out.Existing = &documentRefJSON{ID: res.Existing.ID, Title: res.Existing.Title}
		}
		s.writeJSON(w, http.StatusOK, out)
	default:
		reason := ingest.ReasonUnreadable
		if res.Refusal != nil {
			reason = res.Refusal.Reason
		}
		s.refuseUpload(w, reason)
	}
}
