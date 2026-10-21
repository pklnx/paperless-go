package web

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"time"

	"rsc.io/qr"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/store"
)

// Two-factor authentication with TOTP, ported from mail-archive. Admins
// always need it; with PAPERLESS_GO_REQUIRE_2FA everybody does. Users who
// need it but have not set it up yet (also an admin after the upgrade that
// introduced it) log in with their password and can then only set it up
// (see authenticate). Users who turned it on voluntarily are asked for a
// code at every login too.

// twoFactorChallengeTTL is how long the second login step may take.
const twoFactorChallengeTTL = 5 * time.Minute

// totpIssuer names the server in authenticator apps.
const totpIssuer = "paperless-go"

const (
	errInvalidSecondFactor = "invalid two-factor code"
	errLoginExpired        = "the login has expired; log in again"
	errTwoFactorChanged    = "two-factor authentication changed meanwhile; reload and try again"
)

// twoFactorSetupJSON answers requests of a user who must set up 2FA first.
type twoFactorSetupJSON struct {
	Error                  string `json:"error"`
	TwoFactorSetupRequired bool   `json:"twoFactorSetupRequired"`
}

// twoFactorRequired reports whether the user must use 2FA.
func (s *Server) twoFactorRequired(admin bool) bool {
	return admin || s.require2FA
}

// twoFactorSetupPathAllowed lists what a user who must still set up 2FA may
// do (besides the session endpoints).
func twoFactorSetupPathAllowed(method, path string) bool {
	switch {
	case method == http.MethodGet && path == "/api/profile/2fa":
		return true
	case method == http.MethodPost && (path == "/api/profile/2fa/setup" || path == "/api/profile/2fa/confirm"):
		return true
	}
	return false
}

// twoFactorReady answers 503 when the server has no secret key. serve
// refuses to start without one; this guards servers built otherwise.
func (s *Server) twoFactorReady(w http.ResponseWriter, r *http.Request) bool {
	if s.sealer == nil || len(s.secretKey) == 0 {
		s.fail(w, r, http.StatusServiceUnavailable, "two-factor authentication needs PAPERLESS_GO_SECRET_KEY on the server", nil)
		return false
	}
	return true
}

// blocked answers 429 if failed attempts for name or the client address are
// over the limit.
func (s *Server) blocked(w http.ResponseWriter, r *http.Request, name string) bool {
	wait := s.limiter.Blocked(name, s.clientAddr(r))
	if wait <= 0 {
		return false
	}
	s.tooMany(w, wait, "too many failed attempts; try again later")
	return true
}

type twoFactorJSON struct {
	Enabled           bool  `json:"enabled"`
	Required          bool  `json:"required"`
	Admin             bool  `json:"admin"`
	SetupPending      bool  `json:"setupPending"`
	RecoveryCodesLeft int64 `json:"recoveryCodesLeft"`
	// Only in the answer of POST /api/profile/2fa/setup.
	Secret     string `json:"secret,omitempty"`
	OtpauthURI string `json:"otpauthUri,omitempty"`
	QRDataURL  string `json:"qrDataUrl,omitempty"`
}

func (s *Server) handleGetTwoFactor(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.GetTwoFactorState(r.Context(), userID(r))
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, twoFactorJSON{
		Enabled: st.Enabled, Required: s.twoFactorRequired(st.IsAdmin), Admin: st.IsAdmin, SetupPending: st.SetupPending,
		RecoveryCodesLeft: st.RecoveryCodesLeft,
	})
}

// handleBeginTwoFactor creates a new secret and returns it once, with the
// otpauth URI and a QR code (PNG, rendered on the server: no QR code library
// in the UI).
func (s *Server) handleBeginTwoFactor(w http.ResponseWriter, r *http.Request) {
	if !s.twoFactorReady(w, r) {
		return
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	if err := s.store.BeginTwoFactorSetup(r.Context(), userID(r), secret, s.sealer); err != nil {
		if errors.Is(err, store.ErrConflict) {
			s.fail(w, r, http.StatusConflict, "two-factor authentication is already on", nil)
			return
		}
		s.failStore(w, r, err)
		return
	}
	sess := currentSession(r)
	uri := (&url.URL{
		Scheme:   "otpauth",
		Host:     "totp",
		Path:     "/" + totpIssuer + ":" + sess.UserName,
		RawQuery: url.Values{"secret": {secret}, "issuer": {totpIssuer}}.Encode(),
	}).String()
	code, err := qr.Encode(uri, qr.M)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	s.writeJSON(w, http.StatusOK, twoFactorJSON{
		Required: s.twoFactorRequired(sess.IsAdmin), Admin: sess.IsAdmin, SetupPending: true,
		Secret: secret, OtpauthURI: uri, QRDataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()),
	})
}

type twoFactorCodeInput struct {
	Code string `json:"code"`
}

type recoveryCodesJSON struct {
	RecoveryCodes []string `json:"recoveryCodes"`
}

func (s *Server) handleConfirmTwoFactor(w http.ResponseWriter, r *http.Request) {
	if !s.twoFactorReady(w, r) {
		return
	}
	var in twoFactorCodeInput
	if !s.decode(w, r, &in) {
		return
	}
	sess := currentSession(r)
	if s.blocked(w, r, sess.UserName) {
		return
	}
	codes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	err = s.store.ConfirmTwoFactorSetup(withAudit(r, s.auditEntry(r, "profile.2fa_enable")), sess.UserID, auth.NormalizeTOTPCode(in.Code), s.now(), s.sealer, codes, s.secretKey)
	if errors.Is(err, store.ErrTwoFactorInvalid) {
		s.limiter.Fail(sess.UserName, s.clientAddr(r))
		s.auditSecondFactorFailed(r, "profile.2fa_enable", reasonSecondFactor)
		s.fail(w, r, http.StatusUnauthorized, errInvalidSecondFactor, nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.limiter.Succeed(sess.UserName)
	s.log.Info("2fa enabled", "user", sess.UserName)
	s.writeJSON(w, http.StatusOK, recoveryCodesJSON{RecoveryCodes: codes})
}

// startTwoFactorLogin answers a correct password of a user with 2FA: no
// session yet, but a challenge for the second step.
func (s *Server) startTwoFactorLogin(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !s.twoFactorReady(w, r) {
		return
	}
	if old := sessionToken(r); old != "" {
		_ = s.store.DeleteSession(r.Context(), auth.HashSessionToken(old))
	}
	now := s.now()
	challenge, err := s.store.CreateTwoFactorChallenge(r.Context(), u.ID, u.TwoFactorVersion, now, now.Add(twoFactorChallengeTTL))
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"twoFactorRequired": true, "challenge": challenge})
}

func (s *Server) handleTwoFactorLogin(w http.ResponseWriter, r *http.Request) {
	if !s.twoFactorReady(w, r) {
		return
	}
	var in struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	// Challenges are 256-bit random tokens; looking them up before the limit
	// check reveals nothing guessable.
	u, err := s.store.TwoFactorChallengeUser(r.Context(), in.Challenge, s.now())
	if errors.Is(err, store.ErrTwoFactorExpired) || errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusUnauthorized, errLoginExpired, nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	addr := s.clientAddr(r)
	if s.blocked(w, r, u.Name) {
		return
	}
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	now := s.now()
	e := s.auditEntry(r, "auth.login")
	e.Detail("method", "2fa")
	user, err := s.store.CompleteTwoFactorLogin(withAudit(r, e), in.Challenge, auth.NormalizeTOTPCode(in.Code), now,
		s.sealer, s.secretKey, hash, now.Add(auth.MaxSessionAge), truncateUserAgent(r.UserAgent()))
	switch {
	case errors.Is(err, store.ErrTwoFactorInvalid):
		s.limiter.Fail(u.Name, addr)
		s.log.Info("login failed: second factor", "user", u.Name, "addr", addr)
		s.auditLoginFailed(r, "2fa", reasonSecondFactor, u.Name, u)
		s.fail(w, r, http.StatusUnauthorized, errInvalidSecondFactor, nil)
		return
	case errors.Is(err, store.ErrTwoFactorExpired):
		s.fail(w, r, http.StatusUnauthorized, errLoginExpired, nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	// Only now is the login complete.
	s.limiter.Succeed(u.Name)
	s.log.Info("login", "user", user.Name, "addr", addr, "2fa", true)
	setSessionCookie(w, r, token, auth.MaxSessionAge)
	s.writeJSON(w, http.StatusOK, sessionJSON{User: s.userJSON(user.Name, user.IsAdmin, user.MustChangePassword, true)})
}

// secondFactorFailed answers a refused second factor of the logged-in user
// (turning 2FA off, new recovery codes: attempted names the change), counts
// it for the login limits and records it.
func (s *Server) secondFactorFailed(w http.ResponseWriter, r *http.Request, attempted string, err error) {
	sess := currentSession(r)
	switch {
	case errors.Is(err, store.ErrTwoFactorInvalid):
		s.limiter.Fail(sess.UserName, s.clientAddr(r))
		s.auditSecondFactorFailed(r, attempted, reasonSecondFactor)
		s.fail(w, r, http.StatusUnauthorized, errInvalidSecondFactor, nil)
	case errors.Is(err, store.ErrConflict):
		s.fail(w, r, http.StatusConflict, errTwoFactorChanged, nil)
	default:
		s.failStore(w, r, err)
	}
}

func (s *Server) handleDisableTwoFactor(w http.ResponseWriter, r *http.Request) {
	if !s.twoFactorReady(w, r) {
		return
	}
	sess := currentSession(r)
	if s.twoFactorRequired(sess.IsAdmin) {
		s.fail(w, r, http.StatusForbidden, "two-factor authentication is required for you", nil)
		return
	}
	var in struct {
		CurrentPassword string `json:"currentPassword"`
		Code            string `json:"code"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if s.blocked(w, r, sess.UserName) {
		return
	}
	u, err := s.store.GetUserByID(r.Context(), sess.UserID)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	ok := false
	if len(in.CurrentPassword) <= auth.MaxPasswordLength {
		if ok, _, err = s.hasher.Verify(r.Context(), u.PasswordHash, in.CurrentPassword); err != nil {
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return
		}
	}
	if !ok {
		s.limiter.Fail(sess.UserName, s.clientAddr(r))
		s.auditSecondFactorFailed(r, "profile.2fa_disable", reasonWrongPassword)
		s.fail(w, r, http.StatusUnauthorized, "the current password is wrong", nil)
		return
	}
	err = s.store.DisableOwnTwoFactor(withAudit(r, s.auditEntry(r, "profile.2fa_disable")), sess.UserID, auth.NormalizeTOTPCode(in.Code),
		s.now(), s.sealer, s.secretKey, s.twoFactorRequired, auth.HashSessionToken(sessionToken(r)))
	if err != nil {
		s.secondFactorFailed(w, r, "profile.2fa_disable", err)
		return
	}
	s.limiter.Succeed(sess.UserName)
	s.log.Info("2fa disabled", "user", sess.UserName)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	if !s.twoFactorReady(w, r) {
		return
	}
	var in twoFactorCodeInput
	if !s.decode(w, r, &in) {
		return
	}
	sess := currentSession(r)
	if s.blocked(w, r, sess.UserName) {
		return
	}
	codes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	err = s.store.ReplaceRecoveryCodes(withAudit(r, s.auditEntry(r, "profile.recovery_codes")), sess.UserID, auth.NormalizeTOTPCode(in.Code),
		s.now(), s.sealer, codes, s.secretKey)
	if err != nil {
		s.secondFactorFailed(w, r, "profile.recovery_codes", err)
		return
	}
	s.limiter.Succeed(sess.UserName)
	s.writeJSON(w, http.StatusOK, recoveryCodesJSON{RecoveryCodes: codes})
}

func (s *Server) handleResetUserTwoFactor(w http.ResponseWriter, r *http.Request) {
	u, ok := s.pathUser(w, r)
	if !ok {
		return
	}
	if err := s.store.ResetTwoFactor(withAudit(r, auditUser(s.auditEntry(r, "user.2fa_reset"), u)), u.ID); err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("2fa reset", "user", u.Name, "by", currentSession(r).UserName)
	w.WriteHeader(http.StatusNoContent)
}
