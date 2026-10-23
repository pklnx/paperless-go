package web

import (
	"net/http"

	"github.com/pklnx/paperless-go/internal/store"
)

// The trash (#12, part 1; store/documents_trash.go): DELETE moves a
// document to the trash, restore brings it back, purge asks for its
// permanent deletion, which the sweeper carries out within seconds (it is
// woken) or at its next round. All are accessWrite, in the request's
// scope: another user's document answers 404, an admin may act on
// everyone's (audited with ownerId). The bulk forms are actions of POST
// /api/documents/bulk (handleBulkDocuments).

// Codes of refused trash changes.
const (
	codeInTrash    = "in_trash"
	codeNotInTrash = "not_in_trash"
)

// trashAction is one of the single-document routes.
type trashAction struct {
	audit string
	run   func(s *Server, r *http.Request, sc store.Scope, id string) (*store.Document, error)
}

// handleTrashDocument moves a live document to the trash and writes
// document.delete in the same transaction. Answers: 204, 404 outside the
// scope (also for a document due for purge), 409 in_trash for one in the
// trash already, 503 without the audit log.
func (s *Server) handleTrashDocument(w http.ResponseWriter, r *http.Request) {
	s.trash(w, r, trashAction{audit: "document.delete", run: func(s *Server, r *http.Request, sc store.Scope, id string) (*store.Document, error) {
		return nil, s.store.TrashDocument(r.Context(), sc, id)
	}})
}

// handleRestoreDocument brings a trashed document back and answers 200
// with it (as GET); it writes document.restore. 404 outside the scope and
// for a document due for purge, 409 not_in_trash for a live one.
func (s *Server) handleRestoreDocument(w http.ResponseWriter, r *http.Request) {
	s.trash(w, r, trashAction{audit: "document.restore", run: func(s *Server, r *http.Request, sc store.Scope, id string) (*store.Document, error) {
		return s.store.RestoreDocument(r.Context(), sc, id)
	}})
}

// handlePurgeDocument asks for the permanent deletion of a trashed
// document and answers 204; it writes document.purge. The document is
// hidden from then on. 404 and 409 not_in_trash as restore: permanent
// deletion is always two steps.
func (s *Server) handlePurgeDocument(w http.ResponseWriter, r *http.Request) {
	s.trash(w, r, trashAction{audit: "document.purge", run: func(s *Server, r *http.Request, sc store.Scope, id string) (*store.Document, error) {
		if err := s.store.PurgeDocument(r.Context(), sc, id); err != nil {
			return nil, err
		}
		s.wakeSweeper()
		return nil, nil
	}})
}

func (s *Server) trash(w http.ResponseWriter, r *http.Request, a trashAction) {
	if !s.documentsAvailable(w, r) {
		return
	}
	e := s.auditEntry(r, a.audit)
	e.SetTarget(store.TargetDocument, "", "")
	r = r.WithContext(withAudit(r, e))
	d, err := a.run(s, r, requestScope(r), r.PathValue("id"))
	if err != nil {
		s.failDocument(w, r, err)
		return
	}
	if d == nil {
		w.WriteHeader(http.StatusNoContent)
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

// wakeSweeper tells this process's sweeper that a purge was asked for.
func (s *Server) wakeSweeper() {
	if s.sweepWake != nil {
		s.sweepWake()
	}
}
