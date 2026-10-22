package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/pklnx/paperless-go/internal/store"
)

// Editing documents: PATCH /api/documents/{id} for one, POST
// /api/documents/bulk for several. Both are accessWrite. The scope is
// requestScope's, so another user's document answers 404 and an admin may
// change everyone's. Audit entries hold the names of the changed fields,
// never their values (maintainer decision on #7).

// maxPatchBody is the largest body of a document's PATCH: notes of
// store.MaxNotesLength characters, each escaped in JSON.
const maxPatchBody = 1 << 20

// Codes of refused document changes, for the UI.
const (
	codeASNTaken        = "asn_taken"
	codeInvalidTitle    = "invalid_title"
	codeInvalidNotes    = "invalid_notes"
	codeInvalidASN      = "invalid_asn"
	codeInvalidField    = "invalid_field"
	codeNothingToChange = "nothing_to_change"
	codeInvalidSelect   = "invalid_selection"
	codeEmptySelection  = "empty_selection"
	codeTooMany         = "too_many_documents"
	codeSeveralOwners   = "several_owners"
	codeTagAddRemove    = "tag_add_and_remove"
	codeMatchChanged    = "match_changed"
	codeDocChanged      = "document_changed"
)

// documentError is a refused document change: a fixed message and a code,
// with the document holding a taken ASN, the count and limit of a
// selection that is too large, or the current modifiedAt of a document
// changed meanwhile.
type documentError struct {
	Error      string     `json:"error"`
	Code       string     `json:"code"`
	DocumentID string     `json:"documentId,omitempty"`
	Matched    int        `json:"matched,omitempty"`
	Limit      int        `json:"limit,omitempty"`
	ModifiedAt *time.Time `json:"modifiedAt,omitempty"`
}

// matchChangedJSON is the 409 of a change by search whose matches are no
// longer the confirmed set. Matched (the number the change would cover
// now, informational) is sent also when it is 0.
type matchChangedJSON struct {
	Error   string `json:"error"`
	Code    string `json:"code"`
	Matched int    `json:"matched"`
}

// refuseDocument answers 400 with code and msg.
func (s *Server) refuseDocument(w http.ResponseWriter, code, msg string) {
	s.writeJSON(w, http.StatusBadRequest, documentError{Error: msg, Code: code})
}

// failDocument answers an error of a document change.
func (s *Server) failDocument(w http.ResponseWriter, r *http.Request, err error) {
	var taken *store.ASNTakenError
	var tooMany *store.TooManyError
	var matchChanged *store.MatchChangedError
	var ve *store.ValidationError
	var docChanged *store.DocumentChangedError
	switch {
	case errors.As(err, &matchChanged):
		s.writeJSON(w, http.StatusConflict, matchChangedJSON{
			Error: "the matches of the search changed since they were counted", Code: codeMatchChanged, Matched: matchChanged.Count,
		})
	case errors.As(err, &docChanged):
		s.writeJSON(w, http.StatusConflict, documentError{
			Error: "the document was changed meanwhile", Code: codeDocChanged, ModifiedAt: &docChanged.ModifiedAt,
		})
	case errors.As(err, &taken):
		s.writeJSON(w, http.StatusConflict, documentError{
			Error: "another document has this archive serial number", Code: codeASNTaken, DocumentID: taken.DocumentID,
		})
	case errors.Is(err, store.ErrOwnerMismatch):
		s.refuseDocument(w, codeOwnerMismatch, "a tag, correspondent or document type is not one of the document owner's")
	case errors.As(err, &tooMany):
		s.writeJSON(w, http.StatusBadRequest, documentError{
			Error: "too many documents for one change", Code: codeTooMany, Matched: tooMany.Count, Limit: tooMany.Limit,
		})
	case errors.Is(err, store.ErrSeveralOwners):
		s.refuseDocument(w, codeSeveralOwners,
			"the documents belong to several users; tags, correspondents and types can only be set for one user's documents")
	case errors.As(err, &ve) && ve.Field == "title":
		s.refuseDocument(w, codeInvalidTitle, "invalid title: at most 1024 characters, no control characters")
	case errors.As(err, &ve) && ve.Field == "notes":
		s.refuseDocument(w, codeInvalidNotes,
			"invalid notes: at most 100000 characters, no control characters other than tabs and line breaks")
	case errors.As(err, &ve) && ve.Field == "tags":
		// The store compares the parsed IDs, so the same tag spelled in
		// another case is caught too.
		s.refuseDocument(w, codeTagAddRemove, "a tag cannot be added and removed at once")
	case errors.As(err, &ve) && (ve.Field == "tag" || ve.Field == "correspondent" || ve.Field == "type" || ve.Field == "q"):
		s.failSearch(w, r, err)
	default:
		s.failStore(w, r, err)
	}
}

// documentPatchJSON is the body of PATCH /api/documents/{id}: every field
// is optional, and only the fields sent change. null clears
// archiveSerialNumber, correspondentId and documentTypeId; tagIds replaces
// the tags. null for any other field is refused. expectedModifiedAt is no
// field to change but a precondition: the modifiedAt exactly as GET or the
// last PATCH answered it (RFC 3339); when the document's modifiedAt is
// another, nothing changes and the answer is 409 document_changed.
type documentPatchJSON struct {
	Title               optional[string]    `json:"title"`
	DocumentDate        optional[time.Time] `json:"documentDate"`
	Notes               optional[string]    `json:"notes"`
	IsFavorite          optional[bool]      `json:"isFavorite"`
	IsInbox             optional[bool]      `json:"isInbox"`
	ArchiveSerialNumber optional[int32]     `json:"archiveSerialNumber"`
	CorrespondentID     optional[string]    `json:"correspondentId"`
	DocumentTypeID      optional[string]    `json:"documentTypeId"`
	TagIDs              optional[[]string]  `json:"tagIds"`
	ExpectedModifiedAt  optional[time.Time] `json:"expectedModifiedAt"`
}

// patch turns the body into a store patch; the name of a refused field is
// returned when a value is null where it may not be.
func (in documentPatchJSON) patch() (store.DocumentPatch, string) {
	var p store.DocumentPatch
	for _, f := range []struct {
		name      string
		set, null bool
	}{
		{"title", in.Title.Set, in.Title.Null}, {"documentDate", in.DocumentDate.Set, in.DocumentDate.Null},
		{"notes", in.Notes.Set, in.Notes.Null}, {"isFavorite", in.IsFavorite.Set, in.IsFavorite.Null},
		{"isInbox", in.IsInbox.Set, in.IsInbox.Null}, {"tagIds", in.TagIDs.Set, in.TagIDs.Null},
		{"expectedModifiedAt", in.ExpectedModifiedAt.Set, in.ExpectedModifiedAt.Null},
	} {
		if f.set && f.null {
			return p, f.name
		}
	}
	if in.Title.Set {
		p.Title = &in.Title.Value
	}
	if in.DocumentDate.Set {
		p.DocumentDate = &in.DocumentDate.Value
	}
	if in.Notes.Set {
		p.Notes = &in.Notes.Value
	}
	if in.IsFavorite.Set {
		p.IsFavorite = &in.IsFavorite.Value
	}
	if in.IsInbox.Set {
		p.IsInbox = &in.IsInbox.Value
	}
	if in.ArchiveSerialNumber.Set {
		p.SetASN = true
		if !in.ArchiveSerialNumber.Null {
			p.ArchiveSerialNumber = &in.ArchiveSerialNumber.Value
		}
	}
	if in.CorrespondentID.Set {
		p.SetCorrespondent = true
		if !in.CorrespondentID.Null {
			p.CorrespondentID = &in.CorrespondentID.Value
		}
	}
	if in.DocumentTypeID.Set {
		p.SetDocumentType = true
		if !in.DocumentTypeID.Null {
			p.DocumentTypeID = &in.DocumentTypeID.Value
		}
	}
	if in.TagIDs.Set {
		p.SetTags, p.TagIDs = true, in.TagIDs.Value
		if p.TagIDs == nil {
			p.TagIDs = []string{}
		}
	}
	if in.ExpectedModifiedAt.Set {
		p.ExpectedModifiedAt = &in.ExpectedModifiedAt.Value
	}
	return p, ""
}

// handlePatchDocument changes the fields of one document that the body
// sends (documentPatchJSON) and writes document.update in the same
// transaction: the document as target, the names of the changed fields,
// and the owner's ID for an admin's change of another user's document.
// Answers: 200 with the document (as GET), 400 with a code for refused
// input (owner_mismatch for a link to a row that is not the document
// owner's, invalid_title, invalid_notes, invalid_asn, invalid_field,
// nothing_to_change), 404 outside the scope, 409 asn_taken naming the
// document that has the number, 409 document_changed with the current
// modifiedAt when expectedModifiedAt no longer holds, 503 without the audit log or after
// repeated deadlocks (Retry-After).
func (s *Server) handlePatchDocument(w http.ResponseWriter, r *http.Request) {
	if !s.documentsAvailable(w, r) {
		return
	}
	var in documentPatchJSON
	if !s.decodeLimit(w, r, &in, maxPatchBody) {
		return
	}
	p, bad := in.patch()
	if bad != "" {
		s.refuseDocument(w, codeInvalidField, bad+" cannot be null")
		return
	}
	fields := p.Fields()
	if len(fields) == 0 {
		s.refuseDocument(w, codeNothingToChange, "nothing to change")
		return
	}
	if p.SetASN && p.ArchiveSerialNumber != nil && *p.ArchiveSerialNumber <= 0 {
		s.refuseDocument(w, codeInvalidASN, "the archive serial number must be a positive number")
		return
	}
	e := s.auditEntry(r, "document.update")
	e.SetTarget(store.TargetDocument, "", "")
	e.Detail("fields", fields)
	d, err := s.store.PatchDocument(withAudit(r, e), requestScope(r), r.PathValue("id"), p)
	if err != nil {
		s.failDocument(w, r, err)
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

// bulkMatchJSON is the search of a bulk change by search: the parameters
// of GET /api/documents with the same names and meaning (inbox and
// favorite as booleans), read by the same code (listQuery, ownerScope).
type bulkMatchJSON struct {
	Q             string   `json:"q"`
	Tag           []string `json:"tag"`
	Correspondent []string `json:"correspondent"`
	Type          []string `json:"type"`
	Inbox         *bool    `json:"inbox"`
	Favorite      *bool    `json:"favorite"`
	Owner         string   `json:"owner"`
	TZ            string   `json:"tz"`
}

// values is m as the list's query parameters.
func (m *bulkMatchJSON) values() url.Values {
	v := url.Values{}
	if m.Q != "" {
		v.Set("q", m.Q)
	}
	for _, p := range []struct {
		name string
		ids  []string
	}{{"tag", m.Tag}, {"correspondent", m.Correspondent}, {"type", m.Type}} {
		for _, id := range p.ids {
			v.Add(p.name, id)
		}
	}
	if m.Inbox != nil {
		v.Set("inbox", strconv.FormatBool(*m.Inbox))
	}
	if m.Favorite != nil {
		v.Set("favorite", strconv.FormatBool(*m.Favorite))
	}
	if m.Owner != "" {
		v.Set("owner", m.Owner)
	}
	if m.TZ != "" {
		v.Set("tz", m.TZ)
	}
	return v
}

// auditDetails are the search's parameters as document.search records
// them.
func (m *bulkMatchJSON) auditDetails() map[string]any {
	out := map[string]any{}
	v := m.values()
	for _, k := range searchAuditParams {
		switch vals := v[k]; {
		case len(vals) == 0:
		case k == "tag" || k == "correspondent" || k == "type":
			out[k] = vals
		default:
			out[k] = vals[0]
		}
	}
	return out
}

// bulkJSON is the body of POST /api/documents/bulk.
type bulkJSON struct {
	// IDs selects documents by ID; Match selects every match of a search.
	// Exactly one of them is sent.
	IDs   []string       `json:"ids"`
	Match *bulkMatchJSON `json:"match"`
	// DryRun (only with match) changes nothing and answers what the
	// search covers.
	DryRun bool `json:"dryRun"`
	// Fingerprint is required with match without dryRun and refused
	// otherwise: the dry run's fingerprint, passed back unchanged.
	Fingerprint string `json:"fingerprint"`

	AddTags       []string         `json:"addTags"`
	RemoveTags    []string         `json:"removeTags"`
	Correspondent optional[string] `json:"correspondent"`
	DocumentType  optional[string] `json:"documentType"`
	Inbox         *bool            `json:"inbox"`
	Favorite      *bool            `json:"favorite"`
}

// handleBulkDocuments changes several documents at once, all or nothing
// (store.BulkUpdateDocuments): by ids (at most store.MaxBulkIDs after
// removing repeats, each in the scope, else 404), or by match, every match
// of a search across all pages, resolved by the server with the list's
// own filter code and visibility (at most store.MaxBulkMatches, else 400
// too_many_documents with matched and limit). The change: addTags,
// removeTags, correspondent and documentType (a UUID, or null to clear),
// inbox, favorite. Tags, or a correspondent or type to set, need
// documents of a single owner (400 several_owners).
//
// With dryRun and match it answers {matched, owners, limit, fingerprint}
// and changes nothing: the count the UI confirms before sending the
// change (fingerprint is left out above the limit). A change by match must
// send that fingerprint (else 400 invalid_selection, as is a fingerprint
// with ids or dryRun); it is applied only when the matches are still
// exactly the counted set, else 409 match_changed with matched (the
// number it would cover now) and nothing changes: the client counts again
// and asks again. Otherwise {updated}: the number of changed documents (0
// for a search that matched nothing at its dry run either). A change writes document.bulk_update (count, fields,
// selection, ids, the search for match, bulkRun) and one document.update
// per document (fields, bulkRun, and ownerId for another user's
// document), in the same transaction.
func (s *Server) handleBulkDocuments(w http.ResponseWriter, r *http.Request) {
	if !s.documentsAvailable(w, r) {
		return
	}
	var in bulkJSON
	if !s.decode(w, r, &in) {
		return
	}
	if (in.IDs == nil) == (in.Match == nil) {
		s.refuseDocument(w, codeInvalidSelect, "send either ids or match")
		return
	}
	if in.DryRun && in.Match == nil {
		s.refuseDocument(w, codeInvalidSelect, "dryRun needs match")
		return
	}
	if in.Fingerprint != "" && (in.Match == nil || in.DryRun) {
		s.refuseDocument(w, codeInvalidSelect, "fingerprint goes only with a change by search")
		return
	}
	sel := store.BulkSelection{IDs: in.IDs}
	sc := requestScope(r)
	if in.Match != nil {
		q, _, err := listQuery(in.Match.values())
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
			return
		}
		q.Cursor, q.Sort, q.Ascending = "", "", false
		// The owner narrows the match; the scope stays the caller's, so
		// the audit sees the acting user (ownerId on another user's
		// documents).
		if in.Match.Owner != "" {
			osc, ok := s.ownerScope(w, r, in.Match.Owner)
			if !ok {
				return
			}
			sel.Owner = &osc.UserID
		}
		sel.Match = &q
		if !in.DryRun && in.Fingerprint == "" {
			s.refuseDocument(w, codeInvalidSelect, "a change by search needs the fingerprint of its dry run")
			return
		}
		sel.Fingerprint = in.Fingerprint
	}
	if in.DryRun {
		m, err := s.store.CountBulkMatches(r.Context(), sc, sel)
		if err != nil {
			s.failDocument(w, r, err)
			return
		}
		s.writeJSON(w, http.StatusOK, bulkDryRunJSON{Matched: m.Count, Owners: m.Owners, Limit: store.MaxBulkMatches, Fingerprint: m.Fingerprint})
		return
	}
	if in.IDs != nil {
		if len(in.IDs) == 0 {
			s.refuseDocument(w, codeEmptySelection, "no documents selected")
			return
		}
		unique := map[string]bool{}
		for _, id := range in.IDs {
			unique[id] = true
		}
		if len(unique) > store.MaxBulkIDs {
			s.writeJSON(w, http.StatusBadRequest, documentError{
				Error: "too many documents for one change", Code: codeTooMany, Matched: len(unique), Limit: store.MaxBulkIDs,
			})
			return
		}
	}
	c := store.BulkChange{AddTags: in.AddTags, RemoveTags: in.RemoveTags, IsInbox: in.Inbox, IsFavorite: in.Favorite}
	if in.Correspondent.Set {
		c.SetCorrespondent = true
		if !in.Correspondent.Null {
			c.CorrespondentID = &in.Correspondent.Value
		}
	}
	if in.DocumentType.Set {
		c.SetDocumentType = true
		if !in.DocumentType.Null {
			c.DocumentTypeID = &in.DocumentType.Value
		}
	}
	fields := c.Fields()
	if len(fields) == 0 {
		s.refuseDocument(w, codeNothingToChange, "nothing to change")
		return
	}

	e := s.auditEntry(r, "document.bulk_update")
	e.Detail("fields", fields)
	e.Detail("bulkRun", newBulkRun())
	if in.Match != nil {
		e.Detail("selection", "match")
		e.Detail("match", in.Match.auditDetails())
	} else {
		e.Detail("selection", "ids")
	}
	changed, err := s.store.BulkUpdateDocuments(withAudit(r, e), sc, sel, c, "document.update")
	if err != nil {
		s.failDocument(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]int{"updated": len(changed)})
}

// bulkDryRunJSON is the answer of a dry run.
type bulkDryRunJSON struct {
	Matched     int    `json:"matched"`
	Owners      int    `json:"owners"`
	Limit       int    `json:"limit"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// newBulkRun is a random ID that ties a bulk change's summary entry to its
// per-document entries.
func newBulkRun() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return hex.EncodeToString(b)
}
