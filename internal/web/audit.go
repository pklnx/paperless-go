package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/store"
)

// The audit log records who logged in and who changed which login. Changes
// and logins write their entry in the same transaction (store.WithAudit):
// if the entry cannot be written, nothing changes and the request answers
// 503. Failed logins are written on a best-effort basis, and a logout ends
// the session even when its entry is lost.

const errAuditUnavailable = "the audit log cannot be written; nothing was done"

// auditEntry starts an entry for r: the logged-in user and the client.
func (s *Server) auditEntry(r *http.Request, action string) *store.AuditEntry {
	e := &store.AuditEntry{Actor: store.ActorSession, Action: action, ClientIP: s.clientIP(r), UserAgent: r.UserAgent()}
	if sess := currentSession(r); sess != nil {
		e.SetUser(sess.UserID, sess.UserName)
	}
	return e
}

// withAudit returns r's context with e to be written by the next store
// transaction.
func withAudit(r *http.Request, e *store.AuditEntry) context.Context {
	return store.WithAudit(r.Context(), e)
}

func (s *Server) failAudit(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("audit log not written; request refused", "method", r.Method, "path", r.URL.Path, "err", err)
	s.writeJSON(w, http.StatusServiceUnavailable, apiError{Error: errAuditUnavailable})
}

// auditBestEffort writes e and only logs a failure.
func (s *Server) auditBestEffort(r *http.Request, e *store.AuditEntry) {
	if err := s.store.Audit(r.Context(), *e); err != nil {
		s.log.Error("audit log entry lost", "action", e.Action, "err", err)
	}
}

// Reasons of a failed login.
const (
	reasonWrongPassword = "wrong_password"
	reasonUnknownUser   = "unknown_user"
	reasonLocked        = "locked"
)

// auditLoginFailed records a refused login. Without a known user, the name
// is kept only if it is a valid user name (see loggableUserName), so that a
// password typed into the name field is not stored.
func (s *Server) auditLoginFailed(r *http.Request, reason, name string, u *store.User) {
	e := s.auditEntry(r, "auth.login_failed")
	e.Detail("method", "password")
	e.Detail("reason", reason)
	if u != nil {
		e.SetUser(u.ID, u.Name)
	} else if auth.ValidateUserName(name) == nil {
		e.Detail("name", name)
	}
	s.auditBestEffort(r, e)
}

func auditUser(e *store.AuditEntry, u *store.User) *store.AuditEntry {
	e.SetTarget(store.TargetUser, strconv.FormatInt(u.ID, 10), u.Name)
	return e
}

// Paging of GET /api/audit.
const (
	auditPageDefault = 100
	auditPageMax     = 500
	auditDefaultDays = 30
)

type auditTargetJSON struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type auditEntryJSON struct {
	ID        int64            `json:"id"`
	At        time.Time        `json:"at"`
	Actor     string           `json:"actor"`
	User      string           `json:"user,omitempty"`
	APIKey    string           `json:"apiKey,omitempty"` // the name of the API key
	Action    string           `json:"action"`
	Target    *auditTargetJSON `json:"target"`
	ClientIP  string           `json:"clientIp,omitempty"`
	UserAgent string           `json:"userAgent,omitempty"`
	Details   json.RawMessage  `json:"details"`
}

type auditPageJSON struct {
	Entries    []auditEntryJSON `json:"entries"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// handleAudit lists audit entries, newest first: a user's own, or for
// admins everyone's (requestScope). Reading the log writes no entry.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope := requestScope(r)
	now := s.now()
	p := store.AuditPage{Viewer: scope, Actions: q["action"], Since: now.AddDate(0, 0, -auditDefaultDays), Until: now.Add(time.Minute)}
	var err error
	if v := q.Get("since"); v != "" {
		if p.Since, err = parseAuditBound(v, false); err != nil {
			s.fail(w, r, http.StatusBadRequest, "since: "+err.Error(), nil)
			return
		}
	}
	if v := q.Get("until"); v != "" {
		if p.Until, err = parseAuditBound(v, true); err != nil {
			s.fail(w, r, http.StatusBadRequest, "until: "+err.Error(), nil)
			return
		}
	}
	if !p.Since.Before(p.Until) {
		s.fail(w, r, http.StatusBadRequest, "since must be before until", nil)
		return
	}
	if _, err := store.AuditActionPatterns(p.Actions); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	userName, actor := q.Get("user"), q.Get("actor")
	if !scope.All && (userName != "" || actor != "") {
		s.fail(w, r, http.StatusForbidden, "only admins can see other users' entries", nil)
		return
	}
	switch actor {
	case "", store.ActorSession, store.ActorAPIKey, store.ActorCLI:
		p.Actor = actor
	default:
		s.fail(w, r, http.StatusBadRequest, "actor must be session, api_key or cli", nil)
		return
	}
	if userName != "" {
		u, err := s.store.GetUserByName(r.Context(), auth.NormalizeUserName(userName))
		if err != nil {
			s.failStore(w, r, err)
			return
		}
		p.UserID = &u.ID
	}
	if c := q.Get("cursor"); c != "" {
		at, id, err := decodeAuditCursor(c)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "invalid cursor", nil)
			return
		}
		p.CursorAt, p.CursorID = &at, id
	}
	limit := auditPageDefault
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > auditPageMax {
			s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("limit must be 1 to %d", auditPageMax), nil)
			return
		}
	}
	p.Limit = limit + 1 // one more tells whether there is a next page
	recs, err := s.store.ListAudit(r.Context(), p)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := auditPageJSON{Entries: make([]auditEntryJSON, 0, min(len(recs), limit))}
	if len(recs) > limit {
		last := recs[limit-1]
		out.NextCursor = encodeAuditCursor(last.At, last.ID)
		recs = recs[:limit]
	}
	for _, rec := range recs {
		e := auditEntryJSON{
			ID: rec.ID, At: rec.At.UTC(), Actor: rec.Actor, User: rec.UserName, APIKey: rec.APIKeyName, Action: rec.Action,
			ClientIP: rec.ClientIP, UserAgent: rec.UserAgent, Details: rec.Details,
		}
		if rec.TargetType != "" {
			e.Target = &auditTargetJSON{Type: rec.TargetType, ID: rec.TargetID, Name: rec.TargetName}
		}
		out.Entries = append(out.Entries, e)
	}
	s.writeJSON(w, http.StatusOK, out)
}

// parseAuditBound reads YYYY-MM-DD (midnight UTC) or RFC 3339. A plain date
// as the end includes that day.
func parseAuditBound(v string, end bool) (time.Time, error) {
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		if end {
			t = t.AddDate(0, 0, 1)
		}
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("want YYYY-MM-DD or RFC 3339, got %q", v)
	}
	return t, nil
}

func encodeAuditCursor(at time.Time, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(id, 10)))
}

func decodeAuditCursor(c string) (time.Time, int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, 0, err
	}
	ts, idText, ok := strings.Cut(string(b), "|")
	if !ok {
		return time.Time{}, 0, errors.New("malformed cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, 0, err
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	return at, id, err
}
