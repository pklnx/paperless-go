package web

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pklnx/paperless-go/internal/ingest"
	"github.com/pklnx/paperless-go/internal/search"
	"github.com/pklnx/paperless-go/internal/store"
)

// Uploads and downloads may take longer than the server's read and write
// timeouts (30 seconds to read, 5 minutes to write), which are meant for every
// other request: a 100 MiB file over a slow line needs more. These routes
// extend both of their own deadlines.
const transferTimeout = time.Hour

// fileNameHeader carries the uploaded file's name, percent-encoded (a
// header value cannot hold every character of a name).
const fileNameHeader = "X-File-Name"

// documentJSON is a document as the API lists it. The extracted text is
// only in documentDetailJSON: it can be large.
type documentJSON struct {
	ID      string `json:"id"`
	OwnerID int64  `json:"ownerId"`
	// OwnerName is the owner's user name, only for admins (who may see
	// other users' documents).
	OwnerName           string    `json:"ownerName,omitempty"`
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
	HasArchive     bool   `json:"hasArchive"`
	ArchiveSize    *int64 `json:"archiveSize"`
	ProcessingNote string `json:"processingNote"`
	// Processing is the processing status; it is left out only when it
	// could not be read after an upload.
	Processing *processingJSON `json:"processing,omitempty"`
	// ThumbnailURL is where the thumbnail is, with its version, or null
	// until processing made one.
	ThumbnailURL    *string  `json:"thumbnailUrl"`
	CorrespondentID *string  `json:"correspondentId"`
	DocumentTypeID  *string  `json:"documentTypeId"`
	TagIDs          []string `json:"tagIds"`
}

// processingJSON is a document's processing status.
type processingJSON struct {
	// State is the state of the document's newest job (queued, running,
	// done or failed), or done when it has none: done jobs are deleted
	// after a week.
	State string `json:"state"`
	// Error is the fixed message of the last failed attempt: why it
	// failed, or why a queued job is retried. Never tool output.
	Error       string `json:"error,omitempty"`
	Attempts    int32  `json:"attempts"`
	MaxAttempts int32  `json:"maxAttempts"`
	// RetryAt is when a queued job that failed before runs again.
	RetryAt *time.Time `json:"retryAt,omitempty"`
}

func toProcessingJSON(j *store.Job) *processingJSON {
	if j == nil {
		return &processingJSON{State: store.JobDone}
	}
	p := &processingJSON{State: j.State, Attempts: j.Attempts, MaxAttempts: j.MaxAttempts}
	switch j.State {
	case store.JobFailed:
		p.Error = j.LastError
	case store.JobQueued:
		if j.LastError != "" {
			p.Error = j.LastError
			at := j.RunAfter
			p.RetryAt = &at
		}
	}
	return p
}

// thumbnailVersionLen is how many hex digits of the thumbnail's hash its
// URL carries as the version.
const thumbnailVersionLen = 16

// thumbnailURL is the URL of d's thumbnail with its version (v), or nil.
// A new thumbnail gets a new URL, so the browser may keep each one.
func thumbnailURL(d *store.Document) *string {
	if d.ThumbnailSHA256 == nil || len(*d.ThumbnailSHA256) < thumbnailVersionLen {
		return nil
	}
	u := "/api/documents/" + url.PathEscape(d.ID) + "/thumbnail?v=" + (*d.ThumbnailSHA256)[:thumbnailVersionLen]
	return &u
}

type documentDetailJSON struct {
	documentJSON
	ExtractedText string `json:"extractedText"`
	TextSource    string `json:"textSource"`
}

// toDocumentJSON is d as the API shows it, with the processing status of
// its newest job (nil: none).
func toDocumentJSON(d *store.Document, job *store.Job) documentJSON {
	tags := d.TagIDs
	if tags == nil {
		tags = []string{}
	}
	return documentJSON{
		ID: d.ID, OwnerID: d.OwnerID, Title: d.Title, DocumentDate: d.DocumentDate, CreatedAt: d.CreatedAt, ModifiedAt: d.ModifiedAt,
		Notes: d.Notes, IsFavorite: d.IsFavorite, IsInbox: d.IsInbox, ArchiveSerialNumber: d.ArchiveSerialNumber,
		PageCount: d.PageCount, Source: d.Source, OriginalFileName: d.OriginalFileName, OriginalSize: d.OriginalSize,
		OriginalMIME: d.OriginalMIME, HasArchive: d.ArchiveSHA256 != nil, ArchiveSize: d.ArchiveSize,
		ProcessingNote: d.ProcessingNote, Processing: toProcessingJSON(job), ThumbnailURL: thumbnailURL(d),
		CorrespondentID: d.CorrespondentID, DocumentTypeID: d.DocumentTypeID, TagIDs: tags,
	}
}

// fillOwnerNames sets the owner names of docs for an admin; for anybody
// else it does nothing.
func (s *Server) fillOwnerNames(r *http.Request, docs ...*documentJSON) error {
	if !requestScope(r).All || len(docs) == 0 {
		return nil
	}
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		return err
	}
	names := make(map[int64]string, len(users))
	for _, u := range users {
		names[u.ID] = u.Name
	}
	for _, d := range docs {
		d.OwnerName = names[d.OwnerID]
	}
	return nil
}

// latestJobs reads the processing status of docs.
func (s *Server) latestJobs(r *http.Request, docs ...*store.Document) (map[string]*store.Job, error) {
	ids := make([]string, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
	}
	return s.store.LatestJobs(r.Context(), ids)
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
	Documents []documentListItemJSON `json:"documents"`
	// NextCursor continues the list (the cursor parameter); it is left
	// out on the last page.
	NextCursor string `json:"nextCursor,omitempty"`
	// MaxUploadSize is the upload limit in bytes, so the UI can refuse a
	// larger file before sending it.
	MaxUploadSize int64 `json:"maxUploadSize"`
}

// documentListItemJSON is a listed document. A search with free text adds
// the snippet: up to two fragments of the text, each match between
// store.SnippetStart and store.SnippetStop (U+E000, U+E001). It is plain
// text, never HTML.
type documentListItemJSON struct {
	documentJSON
	Snippet *string `json:"snippet,omitempty"`
}

// Limits of GET /api/documents: the page size (default and maximum 100),
// the IDs per filter parameter, a time zone name and a cursor.
const (
	documentListDefault = search.DefaultLimit
	documentListMax     = search.MaxLimit
	maxFilterIDs        = 100
	maxTimeZoneLen      = 64
	maxCursorLen        = 512
)

// listError is a refused parameter of GET /api/documents: always 400, with
// fixed wording.
type listError string

func (e listError) Error() string { return string(e) }

// listQuery reads the parameters of GET /api/documents into a query. It
// reports whether the request is a search: q with free text or search
// syntax, on the first page. Only these are audited; the filter
// parameters (tag, correspondent, type, inbox, favorite) are browsing,
// like paging through the plain list.
func listQuery(v url.Values) (q search.Query, isSearch bool, err error) {
	q.Limit = documentListDefault
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return q, false, listError("limit must be a positive number")
		}
		q.Limit = min(n, documentListMax)
	}
	var loc *time.Location
	if tz := v.Get("tz"); tz != "" {
		// "Local" would be the server's zone, not one the browser named.
		if len(tz) > maxTimeZoneLen || tz == "Local" {
			return q, false, listError("invalid time zone")
		}
		if loc, err = time.LoadLocation(tz); err != nil {
			return q, false, listError("invalid time zone")
		}
	}
	if text := v.Get("q"); text != "" {
		parsed, err := search.Parse(text, loc)
		switch {
		case errors.Is(err, search.ErrTooLong):
			return q, false, listError("query too long")
		case err != nil:
			return q, false, listError("invalid query")
		}
		parsed.Limit = q.Limit
		q = parsed
	}
	q.Location = loc
	sort, ok := search.ParseSort(v.Get("sort"))
	if !ok {
		return q, false, listError("invalid sort")
	}
	q.Sort = sort
	switch v.Get("order") {
	case "", "desc":
	case "asc":
		q.Ascending = true
	default:
		return q, false, listError("invalid order")
	}
	for _, p := range []struct {
		name string
		dst  *[]string
	}{{"tag", &q.TagIDs}, {"correspondent", &q.CorrespondentIDs}, {"type", &q.TypeIDs}} {
		ids := v[p.name]
		if len(ids) > maxFilterIDs {
			return q, false, listError("too many " + p.name + " ids")
		}
		*p.dst = ids
	}
	typed := len(q.Text) > 0 || len(q.Filters) > 0
	for _, p := range []struct{ name, value string }{{"inbox", search.IsInbox}, {"favorite", search.IsFavorite}} {
		switch v.Get(p.name) {
		case "":
		case "true", "false":
			q.Filters = append(q.Filters, search.Filter{Field: search.FieldIs, Negated: v.Get(p.name) == "false", Values: []string{p.value}})
		default:
			return q, false, listError(p.name + " must be true or false")
		}
	}
	if err := q.CheckLimits(); err != nil {
		return q, false, listError("query too long")
	}
	q.Cursor = v.Get("cursor")
	if len(q.Cursor) > maxCursorLen {
		return q, false, listError("invalid cursor")
	}
	return q, q.Cursor == "" && typed, nil
}

// searchAuditParams are the parameters a document.search entry records:
// the query as typed and the filters, not the paging and sort options.
var searchAuditParams = []string{"q", "tag", "correspondent", "type", "inbox", "favorite", "owner"}

// searchEntry is the audit entry of the first page of a search.
func (s *Server) searchEntry(r *http.Request) *store.AuditEntry {
	e := s.auditEntry(r, "document.search")
	v := r.URL.Query()
	for _, k := range searchAuditParams {
		switch vals := v[k]; {
		case len(vals) == 0:
		case k == "tag" || k == "correspondent" || k == "type":
			e.Detail(k, vals)
		default:
			e.Detail(k, vals[0])
		}
	}
	return e
}

// handleListDocuments lists and searches the scope's documents, without
// their text: by default the newest added first, with free text the best
// match first, each with a snippet. Parameters: q (the search syntax),
// tag, correspondent and type (UUIDs, repeatable, any of them), inbox and
// favorite (true, false), owner (me, or for an admin a user ID; see
// listScope), sort (relevance, added, date), order (desc, asc), tz (an
// IANA zone for the days of date filters, default UTC), limit (1 to 100,
// default 100) and cursor (nextCursor of the previous page). Invalid
// input answers 400 with fixed wording. The first page of a typed search
// (q with free text or syntax) writes document.search; without the audit
// log it answers 503.
func (s *Server) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	if !s.documentsAvailable(w, r) {
		return
	}
	q, isSearch, err := listQuery(r.URL.Query())
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	sc, ok := s.listScope(w, r)
	if !ok {
		return
	}
	page, err := s.store.SearchDocuments(r.Context(), sc, q)
	if err != nil {
		s.failSearch(w, r, err)
		return
	}
	if isSearch {
		if err := s.store.Audit(r.Context(), *s.searchEntry(r)); err != nil {
			s.failAudit(w, r, err)
			return
		}
	}
	docs := make([]*store.Document, len(page.Hits))
	for i, h := range page.Hits {
		docs[i] = h.Document
	}
	jobs, err := s.latestJobs(r, docs...)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := documentListJSON{Documents: make([]documentListItemJSON, len(page.Hits)), NextCursor: page.NextCursor, MaxUploadSize: s.importer.MaxSize()}
	for i, h := range page.Hits {
		out.Documents[i] = documentListItemJSON{documentJSON: toDocumentJSON(h.Document, jobs[h.ID])}
		if q.HasText() {
			snippet := h.Snippet
			out.Documents[i].Snippet = &snippet
		}
	}
	refs := make([]*documentJSON, len(out.Documents))
	for i := range out.Documents {
		refs[i] = &out.Documents[i].documentJSON
	}
	if err := s.fillOwnerNames(r, refs...); err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

// failSearch answers an error of SearchDocuments: refused input with fixed
// wording, everything else as failStore does.
func (s *Server) failSearch(w http.ResponseWriter, r *http.Request, err error) {
	var ve *store.ValidationError
	switch {
	case errors.As(err, &ve) && (ve.Field == "tag" || ve.Field == "correspondent" || ve.Field == "type"):
		s.fail(w, r, http.StatusBadRequest, "invalid "+ve.Field+" id", nil)
	default:
		s.failStore(w, r, err)
	}
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
	jobs, err := s.latestJobs(r, d)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := documentDetailJSON{documentJSON: toDocumentJSON(d, jobs[d.ID]), ExtractedText: d.ExtractedText, TextSource: d.TextSource}
	if err := s.fillOwnerNames(r, &out.documentJSON); err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

// handleReprocess queues one document of the scope for processing again
// (the Retry button), as `reprocess ID` does: its text, page count,
// archive PDF and thumbnail are made again, while text that did not come
// from processing stays. It writes document.reprocess in the queueing
// transaction. Answers: 202 with the document when it was queued, 200 when
// it was queued or running already, 404 for a document outside the scope,
// 503 without the audit log.
func (s *Server) handleReprocess(w http.ResponseWriter, r *http.Request) {
	if !s.documentsAvailable(w, r) {
		return
	}
	d, err := s.store.GetDocument(r.Context(), requestScope(r), r.PathValue("id"))
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	e := s.auditEntry(r, "document.reprocess")
	e.SetTarget(store.TargetDocument, d.ID, d.Title)
	e.Detail("selection", "ids")
	n, err := s.store.Reprocess(withAudit(r, e), store.ReprocessSelection{IDs: []string{d.ID}})
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	jobs, err := s.latestJobs(r, d)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	status := http.StatusOK
	if n > 0 {
		status = http.StatusAccepted
	}
	out := toDocumentJSON(d, jobs[d.ID])
	if err := s.fillOwnerNames(r, &out); err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, status, out)
}

// handleThumbnail sends a document's thumbnail, a JPEG of its first page
// (404 until processing made one), to show inline. Its ETag is the blob's
// hash, so a revalidation answers 304. The URL of the document JSON
// carries the version (v, the start of the hash): with the current one the
// browser may keep it for a day without asking, as a new thumbnail gets a
// new URL; without it, or with an old one, it must revalidate. Either way
// only the browser's own cache may keep it.
func (s *Server) handleThumbnail(w http.ResponseWriter, r *http.Request) {
	if !s.documentsAvailable(w, r) {
		return
	}
	d, err := s.store.GetDocument(r.Context(), requestScope(r), r.PathValue("id"))
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	if d.ThumbnailSHA256 == nil {
		s.fail(w, r, http.StatusNotFound, "this document has no thumbnail yet", nil)
		return
	}
	hash := *d.ThumbnailSHA256
	f, err := s.blobs.OpenHash(hash)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", fmt.Errorf("open the thumbnail of document %s: %w", d.ID, err))
		return
	}
	defer func() { _ = f.Close() }()
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("ETag", `"`+hash+`"`)
	if v := r.URL.Query().Get("v"); len(hash) >= thumbnailVersionLen && v == hash[:thumbnailVersionLen] {
		h.Set("Cache-Control", "private, max-age=86400, immutable")
	} else {
		h.Set("Cache-Control", "private, no-cache")
	}
	http.ServeContent(w, r, "", time.Time{}, f)
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
	hash, mimeType, name := d.OriginalSHA256, servedType(d.OriginalMIME), downloadName(d, false)
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
	// The read deadline too: in HTTP/1, the server's background read of
	// a request without a body ends at the read deadline and cancels the
	// request's context, which a long download would otherwise outlive.
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(transferTimeout)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)
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

// servedTypes are the types the importer stores, with the extension a
// download gets. Only these are sent as their own Content-Type; anything
// else (a stored type from elsewhere, such as an archive import) goes out
// as application/octet-stream without an extension.
var servedTypes = map[string]string{
	ingest.MIMEPDF: ".pdf", ingest.MIMEJPEG: ".jpg", ingest.MIMEPNG: ".png", ingest.MIMEHEIC: ".heic", ingest.MIMEHEIF: ".heif",
}

// servedType is the Content-Type a stored file is sent with.
func servedType(mime string) string {
	if _, ok := servedTypes[mime]; ok {
		return mime
	}
	return "application/octet-stream"
}

// downloadName is the file name a download is offered under: the stem of
// the original file name (or the title) with the extension of the stored
// type, never the client's own extension, so "x.html" holding a PDF
// downloads as "x.pdf". The archive PDF gets .pdf.
func downloadName(d *store.Document, archive bool) string {
	stem := ingest.FileName(d.OriginalFileName)
	stem = strings.TrimSuffix(stem, filepath.Ext(stem))
	if strings.TrimSpace(stem) == "" {
		// A title may contain anything, also slashes.
		stem = ingest.FileName(strings.NewReplacer("/", "_", `\`, "_").Replace(d.Title))
	}
	if strings.TrimSpace(stem) == "" {
		stem = "document"
	}
	if archive {
		return stem + ".pdf"
	}
	return stem + servedTypes[d.OriginalMIME]
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
		out := toDocumentJSON(res.Document, nil)
		if requestScope(r).All {
			out.OwnerName = currentSession(r).UserName
		}
		// The document is stored; a status that cannot be read is left
		// out rather than failing the upload.
		if jobs, err := s.latestJobs(r, res.Document); err != nil {
			s.log.Warn("read the processing status of a new document", "document", res.Document.ID, "err", err)
			out.Processing = nil
		} else {
			out.Processing = toProcessingJSON(jobs[res.Document.ID])
		}
		s.writeJSON(w, http.StatusCreated, out)
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
