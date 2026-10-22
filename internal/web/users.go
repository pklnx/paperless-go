package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/store"
)

// User management for admins: logins and roles. How many documents and
// metadata a user owns is shown, because it decides whether the user can
// be removed.

type adminUserJSON struct {
	Name               string `json:"name"`
	Admin              bool   `json:"admin"`
	Locked             bool   `json:"locked"`
	MustChangePassword bool   `json:"mustChangePassword"`
	TwoFactorEnabled   bool   `json:"twoFactorEnabled"`
	Passkeys           int64  `json:"passkeys"`
	// APIKeys counts the user's keys that are neither revoked nor expired.
	APIKeys int64 `json:"apiKeys"`
	// Owned counts the rows the user owns; any of them keeps the user from
	// being removed.
	Owned       ownedJSON  `json:"owned"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastLoginAt *time.Time `json:"lastLoginAt"`
	// Self marks the logged-in admin, whose own row cannot be changed here.
	Self bool `json:"self"`
}

type ownedJSON struct {
	Documents      int64 `json:"documents"`
	Tags           int64 `json:"tags"`
	Correspondents int64 `json:"correspondents"`
	DocumentTypes  int64 `json:"documentTypes"`
	SavedViews     int64 `json:"savedViews"`
}

// requireAdmin rejects requests from users who are not admins.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if sess := currentSession(r); sess == nil || !sess.IsAdmin {
		s.fail(w, r, http.StatusForbidden, "only admins can manage users", nil)
		return false
	}
	return true
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	owned, err := s.store.OwnedRowCounts(r.Context())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	passkeys, err := s.store.PasskeyCounts(r.Context())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	apiKeys, err := s.store.APIKeyCounts(r.Context(), s.now())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := make([]adminUserJSON, 0, len(users))
	for _, u := range users {
		o := owned[u.ID]
		out = append(out, adminUserJSON{
			Name: u.Name, Admin: u.IsAdmin, Locked: u.LockedAt != nil, MustChangePassword: u.MustChangePassword, TwoFactorEnabled: u.TwoFactorEnabled,
			Passkeys: passkeys[u.ID], APIKeys: apiKeys[u.ID],
			Owned:     ownedJSON{Documents: o.Documents, Tags: o.Tags, Correspondents: o.Correspondents, DocumentTypes: o.DocumentTypes, SavedViews: o.SavedViews},
			CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt, Self: u.ID == userID(r),
		})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

type newUserInput struct {
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
}

// generatedPasswordJSON carries a generated password. It is shown to the
// admin once and never stored or logged in plain text.
type generatedPasswordJSON struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var in newUserInput
	if !s.decode(w, r, &in) {
		return
	}
	name := auth.NormalizeUserName(in.Name)
	if err := auth.ValidateUserName(name); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	pw, hash, ok := s.generatePassword(w, r)
	if !ok {
		return
	}
	e := s.auditEntry(r, "user.add")
	e.SetTarget(store.TargetUser, "", name)
	e.Detail("admin", in.Admin)
	if _, err := s.store.CreateUserWithGeneratedPassword(withAudit(r, e), name, hash, in.Admin); err != nil {
		if errors.Is(err, store.ErrNameTaken) {
			s.fail(w, r, http.StatusConflict, fmt.Sprintf("a user named %q already exists", name), nil)
			return
		}
		s.failStore(w, r, err)
		return
	}
	s.log.Info("user added", "user", name, "admin", in.Admin, "by", currentSession(r).UserName)
	s.writeJSON(w, http.StatusCreated, generatedPasswordJSON{Name: name, Password: pw})
}

func (s *Server) generatePassword(w http.ResponseWriter, r *http.Request) (pw, hash string, ok bool) {
	pw, err := auth.GeneratePassword()
	if err == nil {
		hash, err = s.hasher.Hash(r.Context(), pw)
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return "", "", false
	}
	return pw, hash, true
}

// pathUser loads the user named in the URL for an admin action on someone
// else: admins change their own login through the profile page.
func (s *Server) pathUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	if !s.requireAdmin(w, r) {
		return nil, false
	}
	u, err := s.store.GetUserByName(r.Context(), auth.NormalizeUserName(r.PathValue("name")))
	if err != nil {
		s.failStore(w, r, err)
		return nil, false
	}
	if u.ID == userID(r) {
		s.fail(w, r, http.StatusForbidden, "you cannot change your own login here; use the profile page, or ask another admin", nil)
		return nil, false
	}
	return u, true
}

// passwordResetInput is the optional body of a password reset. Passkeys
// are removed and API keys revoked unless removePasskeys or revokeApiKeys
// is false: after a compromise a new password alone would not lock the
// attacker out.
type passwordResetInput struct {
	RemovePasskeys *bool `json:"removePasskeys"`
	RevokeAPIKeys  *bool `json:"revokeApiKeys"`
}

func (s *Server) handleResetUserPassword(w http.ResponseWriter, r *http.Request) {
	u, ok := s.pathUser(w, r)
	if !ok {
		return
	}
	var in passwordResetInput
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		s.fail(w, r, http.StatusBadRequest, "invalid request body: "+err.Error(), nil)
		return
	}
	removePasskeys := in.RemovePasskeys == nil || *in.RemovePasskeys
	revokeKeys := in.RevokeAPIKeys == nil || *in.RevokeAPIKeys
	pw, hash, ok := s.generatePassword(w, r)
	if !ok {
		return
	}
	by := userID(r)
	opts := store.PasswordReset{MustChange: true, DropPasskeys: removePasskeys, RevokeAPIKeys: revokeKeys, By: &by, Now: s.now()}
	e := auditUser(s.auditEntry(r, "user.password_reset"), u)
	if err := s.store.ResetUserPassword(withAudit(r, e), u.ID, hash, opts); err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("password reset", "user", u.Name, "passkeysRemoved", removePasskeys, "apiKeysRevoked", e.Details["apiKeysRevoked"],
		"by", currentSession(r).UserName)
	s.writeJSON(w, http.StatusOK, generatedPasswordJSON{Name: u.Name, Password: pw})
}

// userChange is the body of PATCH /api/users/{name}: exactly one field.
type userChange struct {
	Admin  *bool `json:"admin"`
	Locked *bool `json:"locked"`
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	u, ok := s.pathUser(w, r)
	if !ok {
		return
	}
	var in userChange
	if !s.decode(w, r, &in) {
		return
	}
	var err error
	switch {
	case in.Admin != nil && in.Locked == nil:
		err = s.store.SetUserAdmin(withAudit(r, auditUser(s.auditEntry(r, adminAction(*in.Admin)), u)), u.ID, *in.Admin)
	case in.Locked != nil && in.Admin == nil:
		err = s.store.SetUserLocked(withAudit(r, auditUser(s.auditEntry(r, lockAction(*in.Locked)), u)), u.ID, *in.Locked)
	default:
		s.fail(w, r, http.StatusBadRequest, "send exactly one of admin and locked", nil)
		return
	}
	if errors.Is(err, store.ErrLastAdmin) {
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("%q is the last admin who can log in", u.Name), nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("user changed", "user", u.Name, "by", currentSession(r).UserName)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	u, ok := s.pathUser(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteUser(withAudit(r, auditUser(s.auditEntry(r, "user.remove"), u)), u.ID)
	var owns *store.OwnsRowsError
	switch {
	case errors.Is(err, store.ErrLastAdmin):
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("%q is the last admin who can log in", u.Name), nil)
		return
	case errors.As(err, &owns):
		s.fail(w, r, http.StatusConflict, ownsRowsMessage(u.Name, owns), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("user removed", "user", u.Name, "by", currentSession(r).UserName)
	w.WriteHeader(http.StatusNoContent)
}

// adminAction is the audit action of granting (promote) or revoking
// (demote) the admin role.
func adminAction(admin bool) string {
	if admin {
		return "user.promote"
	}
	return "user.demote"
}

// lockAction is the audit action of locking or unlocking a user.
func lockAction(locked bool) string {
	if locked {
		return "user.lock"
	}
	return "user.unlock"
}

// ownsRowsMessage explains why a user cannot be removed.
func ownsRowsMessage(name string, e *store.OwnsRowsError) string {
	if e.Counts.Total() == 0 {
		return fmt.Sprintf("%q still owns data; delete it first", name)
	}
	return fmt.Sprintf("%q still owns %s; delete them first", name, e.Counts)
}

type passwordChangeInput struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// handleChangeOwnPassword lets every user change their password, also a
// generated one they must replace. The current password is checked with the
// same limits as a login.
func (s *Server) handleChangeOwnPassword(w http.ResponseWriter, r *http.Request) {
	var in passwordChangeInput
	if !s.decode(w, r, &in) {
		return
	}
	sess := currentSession(r)
	addr := s.clientAddr(r)
	if wait := s.limiter.Blocked(sess.UserName, addr); wait > 0 {
		s.tooMany(w, wait, "too many failed attempts; try again later")
		return
	}
	u, err := s.store.GetUserByID(r.Context(), sess.UserID)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	ok := false
	if len(in.Current) <= auth.MaxPasswordLength {
		if ok, _, err = s.hasher.Verify(r.Context(), u.PasswordHash, in.Current); err != nil {
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return
		}
	}
	if !ok {
		s.limiter.Fail(sess.UserName, addr)
		s.fail(w, r, http.StatusUnauthorized, "the current password is wrong", nil)
		return
	}
	s.limiter.Succeed(sess.UserName)
	if err := auth.ValidatePassword(in.New); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if in.New == in.Current {
		s.fail(w, r, http.StatusBadRequest, "choose a password different from the current one", nil)
		return
	}
	hash, err := s.hasher.Hash(r.Context(), in.New)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	if err := s.store.ChangeOwnPassword(withAudit(r, s.auditEntry(r, "profile.password_change")), u.ID, hash, auth.HashSessionToken(sessionToken(r))); err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("password changed", "user", u.Name)
	w.WriteHeader(http.StatusNoContent)
}
