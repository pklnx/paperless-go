package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/pklnx/paperless-go/internal/store"
)

// The saved views API (#12, part 4): GET, POST, PATCH and DELETE
// /api/saved-views. Saved views are personal: every call works on the
// caller's own views only, also for an admin (another user's view answers
// 404). Reads are accessRead, changes accessWrite. Each change writes its
// audit entry in the same transaction (saved_view.create, .update,
// .delete) with the view's name, a rename's old name and, for an update,
// the names of the fields sent: never the filter.
//
// The filter is the app's filterJSON object (DocumentArchive's
// DocumentFilter.swift). The web keeps its own list parameters under the
// savedViewKey key and leaves every other key as it is, so a view
// survives a round trip through the app; the mapping between the two
// formats is the archive import's (#10). Only the web's part is checked:
// it must be a version-1 object of the fields below whose parameters
// listQuery accepts.

// savedViewKey is the filter key of the web's parameters.
const savedViewKey = "paperless-go"

// maxSavedViewFilter is the largest filter, in bytes as sent.
const maxSavedViewFilter = 16 << 10

const codeInvalidFilter = "invalid_filter"

// savedViewParams is the web's part of a filter: the parameters of GET
// /api/documents it applies, and an admin's "Everyone's documents".
type savedViewParams struct {
	V             int      `json:"v"`
	Q             string   `json:"q"`
	Tag           []string `json:"tag"`
	Correspondent []string `json:"correspondent"`
	Type          []string `json:"type"`
	Inbox         *bool    `json:"inbox"`
	Favorite      *bool    `json:"favorite"`
	Sort          string   `json:"sort"`
	Order         string   `json:"order"`
	Everyone      bool     `json:"everyone"`
}

// savedViewFields are the exact keys of savedViewParams.
var savedViewFields = map[string]bool{
	"v": true, "q": true, "tag": true, "correspondent": true, "type": true,
	"inbox": true, "favorite": true, "sort": true, "order": true, "everyone": true,
}

// exactKeys reports whether raw is one JSON object whose keys are all in
// allowed, each at most once, by exact name.
func exactKeys(raw json.RawMessage, allowed map[string]bool) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		k, ok := tok.(string)
		if !ok || !allowed[k] || seen[k] {
			return false
		}
		seen[k] = true
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return false
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return false
	}
	_, err := dec.Token()
	return err == io.EOF
}

var uuidPattern = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// checkSavedViewFilter checks a filter: a JSON object of at most
// maxSavedViewFilter bytes whose savedViewKey part, if present, is valid.
// It returns the reason of a refusal, or "".
func checkSavedViewFilter(f json.RawMessage) string {
	if len(f) > maxSavedViewFilter {
		return "filter too large"
	}
	var top map[string]json.RawMessage
	if t := bytes.TrimSpace(f); len(t) == 0 || t[0] != '{' || json.Unmarshal(t, &top) != nil {
		return "filter must be a JSON object"
	}
	raw, ok := top[savedViewKey]
	if !ok {
		return ""
	}
	// The keys are checked by their exact names first: encoding/json
	// matches field names case-insensitively, so "Q" next to "q" would
	// otherwise pass unchecked and one of them win.
	if !exactKeys(raw, savedViewFields) {
		return "invalid " + savedViewKey + " filter"
	}
	var p savedViewParams
	if json.Unmarshal(raw, &p) != nil {
		return "invalid " + savedViewKey + " filter"
	}
	if p.V != 1 {
		return "unknown " + savedViewKey + " filter version"
	}
	// Only what the web can show exactly: one ID per kind, and the inbox
	// and favourites as a filter or not at all (the web's URL holds no
	// "not in the inbox"). A view the web would open differently is
	// refused rather than shown wrong.
	if len(p.Tag) > 1 || len(p.Correspondent) > 1 || len(p.Type) > 1 {
		return "at most one tag, correspondent and type"
	}
	if p.Inbox != nil && !*p.Inbox || p.Favorite != nil && !*p.Favorite {
		return "inbox and favorite must be true or null"
	}
	v := url.Values{}
	set := func(k, s string) {
		if s != "" {
			v.Set(k, s)
		}
	}
	set("q", p.Q)
	set("sort", p.Sort)
	set("order", p.Order)
	for k, b := range map[string]*bool{"inbox": p.Inbox, "favorite": p.Favorite} {
		if b != nil {
			v.Set(k, strconv.FormatBool(*b))
		}
	}
	for k, ids := range map[string][]string{"tag": p.Tag, "correspondent": p.Correspondent, "type": p.Type} {
		for _, id := range ids {
			if !uuidPattern.MatchString(id) {
				return "invalid " + k + " id"
			}
		}
		v[k] = ids
	}
	if _, _, err := listQuery(v); err != nil {
		return err.Error()
	}
	return ""
}

type savedViewJSON struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Symbol     string          `json:"symbol"`
	SortIndex  int64           `json:"sortIndex"`
	Filter     json.RawMessage `json:"filter"`
	CreatedAt  time.Time       `json:"createdAt"`
	ModifiedAt time.Time       `json:"modifiedAt"`
}

func toSavedViewJSON(v *store.SavedView) savedViewJSON {
	return savedViewJSON{
		ID: v.ID, Name: v.Name, Symbol: v.Symbol, SortIndex: v.SortIndex, Filter: v.Filter,
		CreatedAt: v.CreatedAt, ModifiedAt: v.ModifiedAt,
	}
}

// ownScope is the scope of the saved views: the caller's own, also for an
// admin.
func ownScope(r *http.Request) store.Scope {
	return store.Scope{UserID: requestScope(r).UserID}
}

// checkSortIndex refuses a sort index outside ±store.MaxSavedViewSortIndex
// with 400; ok is false after answering.
func (s *Server) checkSortIndex(w http.ResponseWriter, r *http.Request, i int64) bool {
	if i > store.MaxSavedViewSortIndex || i < -store.MaxSavedViewSortIndex {
		s.fail(w, r, http.StatusBadRequest, "sortIndex must be between -(2^53-1) and 2^53-1", nil)
		return false
	}
	return true
}

// refuseFilter answers 400 invalid_filter with reason.
func (s *Server) refuseFilter(w http.ResponseWriter, reason string) {
	s.refuseMetadata(w, http.StatusBadRequest, codeInvalidFilter, "invalid filter: "+reason)
}

// failSavedView answers an error of a saved-view change.
func (s *Server) failSavedView(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrInvalidFilter) {
		s.refuseFilter(w, "filter must be a JSON object")
		return
	}
	s.failStore(w, r, err)
}

func (s *Server) handleListSavedViews(w http.ResponseWriter, r *http.Request) {
	views, err := s.store.ListSavedViews(r.Context(), ownScope(r))
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := make([]savedViewJSON, len(views))
	for i, v := range views {
		out[i] = toSavedViewJSON(v)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"savedViews": out})
}

func (s *Server) handleCreateSavedView(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string          `json:"name"`
		Filter    json.RawMessage `json:"filter"`
		SortIndex *int64          `json:"sortIndex"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	name, ok := s.cleanName(w, in.Name)
	if !ok {
		return
	}
	if in.Filter == nil || string(in.Filter) == "null" {
		in.Filter = json.RawMessage("{}")
	}
	if reason := checkSavedViewFilter(in.Filter); reason != "" {
		s.refuseFilter(w, reason)
		return
	}
	if in.SortIndex != nil && !s.checkSortIndex(w, r, *in.SortIndex) {
		return
	}
	vi := store.SavedViewInput{Name: name, Filter: in.Filter, SortLast: in.SortIndex == nil}
	if in.SortIndex != nil {
		vi.SortIndex = *in.SortIndex
	}
	v, err := s.store.CreateSavedView(withAudit(r, s.metadataEntry(r, store.TargetSavedView, "create")), ownScope(r), vi)
	if err != nil {
		s.failSavedView(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, toSavedViewJSON(v))
}

func (s *Server) handlePatchSavedView(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      optional[string]          `json:"name"`
		Filter    optional[json.RawMessage] `json:"filter"`
		SortIndex optional[int64]           `json:"sortIndex"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	var p store.SavedViewPatch
	var fields []string
	if in.Name.Set {
		name, ok := s.cleanName(w, in.Name.Value)
		if !ok {
			return
		}
		p.Name, fields = &name, append(fields, "name")
	}
	if in.Filter.Set {
		if in.Filter.Null {
			s.refuseFilter(w, "filter must be a JSON object")
			return
		}
		if reason := checkSavedViewFilter(in.Filter.Value); reason != "" {
			s.refuseFilter(w, reason)
			return
		}
		p.Filter, fields = in.Filter.Value, append(fields, "filter")
	}
	if in.SortIndex.Set {
		if in.SortIndex.Null {
			s.fail(w, r, http.StatusBadRequest, "sortIndex must be a number", nil)
			return
		}
		if !s.checkSortIndex(w, r, in.SortIndex.Value) {
			return
		}
		p.SortIndex, fields = &in.SortIndex.Value, append(fields, "sortIndex")
	}
	if len(fields) == 0 {
		s.fail(w, r, http.StatusBadRequest, "nothing to change", nil)
		return
	}
	e := s.metadataEntry(r, store.TargetSavedView, "update")
	e.Detail("fields", fields)
	v, err := s.store.PatchSavedView(withAudit(r, e), ownScope(r), r.PathValue("id"), p)
	if err != nil {
		s.failSavedView(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, toSavedViewJSON(v))
}

func (s *Server) handleDeleteSavedView(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteSavedView(withAudit(r, s.metadataEntry(r, store.TargetSavedView, "delete")), ownScope(r), r.PathValue("id"))
	if err != nil {
		s.failSavedView(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
