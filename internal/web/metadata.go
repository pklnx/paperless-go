package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pklnx/paperless-go/internal/store"
)

// The metadata API: tags, correspondents and document types, per owner.
// Reads are accessRead, changes accessWrite. The scope comes from
// requestScope, so another user's row answers 404 and an admin may change
// everyone's; an admin's change of another user's row records the owner in
// its audit entry. Each change writes its audit entry in the same
// transaction (tag.create, tag.update, tag.delete, likewise correspondent.*
// and document_type.*), with the row's name, a rename's old name and, for
// an update, the names of the fields sent: never other values.

// maxMetadataName is the longest name, in characters after trimming.
const maxMetadataName = 128

var colorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// Codes of refused metadata changes, for the UI.
const (
	codeNameTaken       = "name_taken"
	codeTagCycle        = "tag_cycle"
	codeTagNameConflict = "tag_name_conflict"
	codeOwnerMismatch   = "owner_mismatch"
	codeUnknownOwner    = "unknown_owner"
	codeInvalidName     = "invalid_name"
	codeInvalidColor    = "invalid_color"
)

// metadataError is a refused metadata change: a fixed message and a code.
// A tag_name_conflict names the clashing child when the lookup found it.
type metadataError struct {
	Error     string `json:"error"`
	Code      string `json:"code"`
	ChildID   string `json:"childId,omitempty"`
	ChildName string `json:"childName,omitempty"`
}

func (s *Server) refuseMetadata(w http.ResponseWriter, status int, code, msg string) {
	s.writeJSON(w, status, metadataError{Error: msg, Code: code})
}

// failMetadata answers an error of a metadata change. forOwner says the
// request named an owner (ownerId), so ErrUnknownUser means that owner,
// not the caller.
func (s *Server) failMetadata(w http.ResponseWriter, r *http.Request, err error, forOwner bool) {
	var conflict *store.TagNameConflictError
	switch {
	case errors.Is(err, store.ErrNameTaken):
		s.refuseMetadata(w, http.StatusConflict, codeNameTaken, "this name is taken")
	case errors.Is(err, store.ErrTagCycle):
		s.refuseMetadata(w, http.StatusConflict, codeTagCycle, "a tag cannot be placed under itself or one of its sub-tags")
	case errors.As(err, &conflict):
		s.writeJSON(w, http.StatusConflict, metadataError{
			Error: "a sub-tag would get the name of an existing top-level tag", Code: codeTagNameConflict,
			ChildID: conflict.ChildID, ChildName: conflict.ChildName,
		})
	case errors.Is(err, store.ErrOwnerMismatch):
		s.refuseMetadata(w, http.StatusBadRequest, codeOwnerMismatch, "the parent tag is not one of the owner's tags")
	case forOwner && errors.Is(err, store.ErrUnknownUser):
		s.refuseMetadata(w, http.StatusBadRequest, codeUnknownOwner, "unknown owner")
	default:
		s.failStore(w, r, err)
	}
}

// optional is a field of a PATCH body: Set when the body has it, Null when
// it is null.
type optional[T any] struct {
	Set, Null bool
	Value     T
}

// UnmarshalJSON is called for every value of the field, null included.
func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

// cleanName trims a name and checks it; ok is false after answering 400.
func (s *Server) cleanName(w http.ResponseWriter, name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxMetadataName {
		s.refuseMetadata(w, http.StatusBadRequest, codeInvalidName, "invalid name: 1 to 128 characters")
		return "", false
	}
	return name, true
}

func (s *Server) checkColor(w http.ResponseWriter, color string) bool {
	if !colorPattern.MatchString(color) {
		s.refuseMetadata(w, http.StatusBadRequest, codeInvalidColor, "invalid color: #RRGGBB")
		return false
	}
	return true
}

// listScope is the scope of a metadata list or the counts: the request's,
// or with owner=me only the caller's own rows, or for an admin
// owner=<user ID> only that user's rows (the pickers for another user's
// document). A non-admin may name only themselves.
func (s *Server) listScope(w http.ResponseWriter, r *http.Request) (store.Scope, bool) {
	return s.ownerScope(w, r, r.URL.Query().Get("owner"))
}

// ownerScope is listScope for the owner value v; ok is false after
// answering.
func (s *Server) ownerScope(w http.ResponseWriter, r *http.Request, v string) (store.Scope, bool) {
	sc := requestScope(r)
	switch v {
	case "":
		return sc, true
	case "me":
		return store.Scope{UserID: sc.UserID}, true
	default:
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			s.fail(w, r, http.StatusBadRequest, "owner must be me or a user ID", nil)
			return sc, false
		}
		if id != sc.UserID && !sc.All {
			s.fail(w, r, http.StatusForbidden, "only admins can list another user's rows", nil)
			return sc, false
		}
		return store.Scope{UserID: id}, true
	}
}

// createOwner checks the optional ownerId of a create: an admin may create
// a row for another user (a tag for that user's document), nobody else.
func (s *Server) createOwner(w http.ResponseWriter, r *http.Request, ownerID *int64) (int64, bool) {
	if ownerID == nil {
		return 0, true
	}
	sc := requestScope(r)
	if *ownerID != sc.UserID && !sc.All {
		s.fail(w, r, http.StatusForbidden, "only admins can create rows for another user", nil)
		return 0, false
	}
	if *ownerID <= 0 {
		s.refuseMetadata(w, http.StatusBadRequest, codeUnknownOwner, "unknown owner")
		return 0, false
	}
	return *ownerID, true
}

// metadataEntry starts the audit entry of a change of a row of kind
// target; the store fills in the row.
func (s *Server) metadataEntry(r *http.Request, target, verb string) *store.AuditEntry {
	e := s.auditEntry(r, target+"."+verb)
	e.SetTarget(target, "", "")
	return e
}

// Tags.

type tagJSON struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	OwnerID       int64     `json:"ownerId"`
	ParentID      *string   `json:"parentId"`
	Color         string    `json:"color"`
	Symbol        string    `json:"symbol"`
	CreatedAt     time.Time `json:"createdAt"`
	ModifiedAt    time.Time `json:"modifiedAt"`
	DocumentCount *int64    `json:"documentCount,omitempty"`
	// SubtreeCount is the number of distinct documents of the tag and its
	// sub-tags: what the list shows when filtered by the tag.
	SubtreeCount *int64 `json:"subtreeCount,omitempty"`
}

func toTagJSON(t *store.Tag) tagJSON {
	return tagJSON{
		ID: t.ID, Name: t.Name, OwnerID: t.OwnerID, ParentID: t.ParentID, Color: t.ColorHex, Symbol: t.Symbol,
		CreatedAt: t.CreatedAt, ModifiedAt: t.ModifiedAt,
	}
}

func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.listScope(w, r)
	if !ok {
		return
	}
	rows, err := s.store.ListTagsWithCounts(r.Context(), sc)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := make([]tagJSON, len(rows))
	for i, row := range rows {
		out[i] = toTagJSON(row.Tag)
		out[i].DocumentCount, out[i].SubtreeCount = &row.DocumentCount, &row.SubtreeCount
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"tags": out})
}

func (s *Server) handleCreateTag(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string  `json:"name"`
		ParentID *string `json:"parentId"`
		Color    *string `json:"color"`
		OwnerID  *int64  `json:"ownerId"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	name, ok := s.cleanName(w, in.Name)
	if !ok {
		return
	}
	ti := store.TagInput{Name: name, ParentID: in.ParentID}
	if in.Color != nil {
		if !s.checkColor(w, *in.Color) {
			return
		}
		ti.ColorHex = *in.Color
	}
	if ti.OwnerID, ok = s.createOwner(w, r, in.OwnerID); !ok {
		return
	}
	t, err := s.store.CreateTag(withAudit(r, s.metadataEntry(r, store.TargetTag, "create")), requestScope(r), ti)
	if err != nil {
		s.failMetadata(w, r, err, in.OwnerID != nil)
		return
	}
	s.writeJSON(w, http.StatusCreated, toTagJSON(t))
}

func (s *Server) handlePatchTag(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     optional[string] `json:"name"`
		Color    optional[string] `json:"color"`
		ParentID optional[string] `json:"parentId"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	var p store.TagPatch
	var fields []string
	if in.Name.Set {
		name, ok := s.cleanName(w, in.Name.Value)
		if !ok {
			return
		}
		p.Name, fields = &name, append(fields, "name")
	}
	if in.Color.Set {
		if in.Color.Null {
			s.refuseMetadata(w, http.StatusBadRequest, codeInvalidColor, "invalid color: #RRGGBB")
			return
		}
		if !s.checkColor(w, in.Color.Value) {
			return
		}
		p.ColorHex, fields = &in.Color.Value, append(fields, "color")
	}
	if in.ParentID.Set {
		p.SetParent, fields = true, append(fields, "parentId")
		if !in.ParentID.Null {
			p.ParentID = &in.ParentID.Value
		}
	}
	if len(fields) == 0 {
		s.fail(w, r, http.StatusBadRequest, "nothing to change", nil)
		return
	}
	e := s.metadataEntry(r, store.TargetTag, "update")
	e.Detail("fields", fields)
	t, err := s.store.PatchTag(withAudit(r, e), requestScope(r), r.PathValue("id"), p)
	if err != nil {
		s.failMetadata(w, r, err, false)
		return
	}
	s.writeJSON(w, http.StatusOK, toTagJSON(t))
}

func (s *Server) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteTag(withAudit(r, s.metadataEntry(r, store.TargetTag, "delete")), requestScope(r), r.PathValue("id"))
	if err != nil {
		s.failMetadata(w, r, err, false)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Correspondents and document types: a name only, as far as the web goes.

type namedJSON struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	OwnerID       int64     `json:"ownerId"`
	CreatedAt     time.Time `json:"createdAt"`
	ModifiedAt    time.Time `json:"modifiedAt"`
	DocumentCount *int64    `json:"documentCount,omitempty"`
}

func toCorrespondentJSON(c *store.Correspondent) namedJSON {
	return namedJSON{ID: c.ID, Name: c.Name, OwnerID: c.OwnerID, CreatedAt: c.CreatedAt, ModifiedAt: c.ModifiedAt}
}

func toDocumentTypeJSON(t *store.DocumentType) namedJSON {
	return namedJSON{ID: t.ID, Name: t.Name, OwnerID: t.OwnerID, CreatedAt: t.CreatedAt, ModifiedAt: t.ModifiedAt}
}

type namedCreateJSON struct {
	Name    string `json:"name"`
	OwnerID *int64 `json:"ownerId"`
}

type namedPatchJSON struct {
	Name optional[string] `json:"name"`
}

// namedPatch reads the body of a correspondent's or document type's PATCH
// and returns the new name; ok is false after answering 400.
func (s *Server) namedPatch(w http.ResponseWriter, r *http.Request) (*string, bool) {
	var in namedPatchJSON
	if !s.decode(w, r, &in) {
		return nil, false
	}
	if !in.Name.Set {
		s.fail(w, r, http.StatusBadRequest, "nothing to change", nil)
		return nil, false
	}
	name, ok := s.cleanName(w, in.Name.Value)
	if !ok {
		return nil, false
	}
	return &name, true
}

func (s *Server) handleListCorrespondents(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.listScope(w, r)
	if !ok {
		return
	}
	rows, err := s.store.ListCorrespondentsWithCounts(r.Context(), sc)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := make([]namedJSON, len(rows))
	for i, row := range rows {
		out[i] = toCorrespondentJSON(row.Correspondent)
		out[i].DocumentCount = &row.DocumentCount
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"correspondents": out})
}

func (s *Server) handleCreateCorrespondent(w http.ResponseWriter, r *http.Request) {
	var in namedCreateJSON
	if !s.decode(w, r, &in) {
		return
	}
	name, ok := s.cleanName(w, in.Name)
	if !ok {
		return
	}
	owner, ok := s.createOwner(w, r, in.OwnerID)
	if !ok {
		return
	}
	c, err := s.store.CreateCorrespondent(withAudit(r, s.metadataEntry(r, store.TargetCorrespondent, "create")), requestScope(r),
		store.CorrespondentInput{OwnerID: owner, Name: name})
	if err != nil {
		s.failMetadata(w, r, err, in.OwnerID != nil)
		return
	}
	s.writeJSON(w, http.StatusCreated, toCorrespondentJSON(c))
}

func (s *Server) handlePatchCorrespondent(w http.ResponseWriter, r *http.Request) {
	name, ok := s.namedPatch(w, r)
	if !ok {
		return
	}
	e := s.metadataEntry(r, store.TargetCorrespondent, "update")
	e.Detail("fields", []string{"name"})
	c, err := s.store.PatchCorrespondent(withAudit(r, e), requestScope(r), r.PathValue("id"), store.CorrespondentPatch{Name: name})
	if err != nil {
		s.failMetadata(w, r, err, false)
		return
	}
	s.writeJSON(w, http.StatusOK, toCorrespondentJSON(c))
}

func (s *Server) handleDeleteCorrespondent(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteCorrespondent(withAudit(r, s.metadataEntry(r, store.TargetCorrespondent, "delete")), requestScope(r), r.PathValue("id"))
	if err != nil {
		s.failMetadata(w, r, err, false)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListDocumentTypes(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.listScope(w, r)
	if !ok {
		return
	}
	rows, err := s.store.ListDocumentTypesWithCounts(r.Context(), sc)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := make([]namedJSON, len(rows))
	for i, row := range rows {
		out[i] = toDocumentTypeJSON(row.DocumentType)
		out[i].DocumentCount = &row.DocumentCount
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"documentTypes": out})
}

func (s *Server) handleCreateDocumentType(w http.ResponseWriter, r *http.Request) {
	var in namedCreateJSON
	if !s.decode(w, r, &in) {
		return
	}
	name, ok := s.cleanName(w, in.Name)
	if !ok {
		return
	}
	owner, ok := s.createOwner(w, r, in.OwnerID)
	if !ok {
		return
	}
	t, err := s.store.CreateDocumentType(withAudit(r, s.metadataEntry(r, store.TargetDocumentType, "create")), requestScope(r),
		store.DocumentTypeInput{OwnerID: owner, Name: name})
	if err != nil {
		s.failMetadata(w, r, err, in.OwnerID != nil)
		return
	}
	s.writeJSON(w, http.StatusCreated, toDocumentTypeJSON(t))
}

func (s *Server) handlePatchDocumentType(w http.ResponseWriter, r *http.Request) {
	name, ok := s.namedPatch(w, r)
	if !ok {
		return
	}
	e := s.metadataEntry(r, store.TargetDocumentType, "update")
	e.Detail("fields", []string{"name"})
	t, err := s.store.PatchDocumentType(withAudit(r, e), requestScope(r), r.PathValue("id"), store.DocumentTypePatch{Name: name})
	if err != nil {
		s.failMetadata(w, r, err, false)
		return
	}
	s.writeJSON(w, http.StatusOK, toDocumentTypeJSON(t))
}

func (s *Server) handleDeleteDocumentType(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteDocumentType(withAudit(r, s.metadataEntry(r, store.TargetDocumentType, "delete")), requestScope(r), r.PathValue("id"))
	if err != nil {
		s.failMetadata(w, r, err, false)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDocumentCounts answers the sidebar's counts of the scope's
// documents (owner as for the lists): {inbox, favorites, total}.
func (s *Server) handleDocumentCounts(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.listScope(w, r)
	if !ok {
		return
	}
	c, err := s.store.CountDocuments(r.Context(), sc)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]int64{"inbox": c.Inbox, "favorites": c.Favorites, "total": c.Total})
}
