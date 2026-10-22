package web

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/store"
)

// Session cookie names. Over HTTPS the __Host- prefix makes the browser
// insist on Secure, Path=/ and no Domain, so no other host can set it.
const (
	cookieName       = "paperless_go_session"
	secureCookieName = "__Host-" + cookieName
)

// touchInterval limits how often a session's last use is written.
const touchInterval = time.Minute

type sessionKey struct{}

// sessionTokenKey holds the token of the session in sessionKey.
type sessionTokenKey struct{}

// profilePasswordPath is the one endpoint a user with a generated password
// may use (besides the session endpoints).
const profilePasswordPath = "/api/profile/password" //nolint:gosec // a URL path, not a credential

// The passkey login endpoints, which need no session.
//
//nolint:gosec // URL paths, not credentials
const (
	passkeyLoginBeginPath  = "/api/session/passkey/begin"
	passkeyLoginFinishPath = "/api/session/passkey/finish"
)

type passwordChangeJSON struct {
	Error                  string `json:"error"`
	PasswordChangeRequired bool   `json:"passwordChangeRequired"`
}

// currentSession returns the session of a request that passed authenticate.
func currentSession(r *http.Request) *store.Session {
	sess, _ := r.Context().Value(sessionKey{}).(*store.Session)
	return sess
}

// userID returns the logged-in user of a request that passed authenticate.
// Without a session it returns 0, which matches no owner.
func userID(r *http.Request) int64 {
	if sess := currentSession(r); sess != nil {
		return sess.UserID
	}
	return 0
}

// requestScope is what the request's principal may see and change in the
// store: a session's or API key's user, and every owner's rows for an
// admin (an admin's key does no more than the admin's session). Without a
// principal it is the empty scope, which matches no owner and cannot
// create rows. Every handler that reads or writes documents or metadata
// takes its scope from here.
func requestScope(r *http.Request) store.Scope {
	sess := currentSession(r)
	if sess == nil {
		return store.Scope{}
	}
	return store.Scope{UserID: sess.UserID, All: sess.IsAdmin}
}

// secureRequest reports whether the browser reached the server over HTTPS,
// directly or through a reverse proxy or `tailscale serve`. X-Forwarded-Proto
// counts only from a trusted proxy (PAPERLESS_GO_TRUSTED_PROXIES) or from
// loopback, where `tailscale serve` and a proxy on the same host connect
// from: anybody else could set it.
func (s *Server) secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if r.Header.Get("X-Forwarded-Proto") != "https" {
		return false
	}
	peer := peerAddr(r)
	return peer.IsLoopback() || s.trustedProxy(peer)
}

// sessionTokens returns the tokens of all of the request's session
// cookies, the __Host- one first. A browser can hold both: a __Host- cookie
// from a time the server saw HTTPS and a plain one from after, for example
// when X-Forwarded-Proto stopped counting because the proxy is not trusted.
//
// Each token costs a database lookup, so at most maxSessionCookies distinct
// tokens per name are returned, however many cookies a request carries.
func sessionTokens(r *http.Request) []string {
	return cookieValues(r, []string{secureCookieName, cookieName}, maxSessionCookies)
}

// cookieValues returns the values of the request's cookies with the given
// names, name by name in that order and each name in header order. Empty
// values and values already returned are skipped; at most perName values
// of one name are returned, the rest of that name is not read.
func cookieValues(r *http.Request, names []string, perName int) []string {
	var values []string
	seen := make(map[string]bool)
	cookies := r.Cookies()
	for _, name := range names {
		n := 0
		for _, c := range cookies {
			if c.Name != name || c.Value == "" || seen[c.Value] {
				continue
			}
			if n == perName {
				break
			}
			seen[c.Value] = true
			values = append(values, c.Value)
			n++
		}
	}
	return values
}

// maxSessionCookies is how many cookies of one session cookie name count.
// A browser sends one per name and path; more are junk or an attack.
const maxSessionCookies = 2

// sessionToken returns the token of the request's valid session, as found
// by authenticate, and without one the first session cookie's token.
func sessionToken(r *http.Request) string {
	if token, ok := r.Context().Value(sessionTokenKey{}).(string); ok {
		return token
	}
	if tokens := sessionTokens(r); len(tokens) > 0 {
		return tokens[0]
	}
	return ""
}

// deleteOldSessions ends the sessions of all of the request's session
// cookies before a login, so a token planted before the login is never
// promoted to a valid session.
func (s *Server) deleteOldSessions(r *http.Request) {
	for _, token := range sessionTokens(r) {
		_ = s.store.DeleteSession(r.Context(), auth.HashSessionToken(token))
	}
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAge time.Duration) {
	s.setHostCookie(w, r, cookieName, token, maxAge)
}

func (s *Server) clearSessionCookies(w http.ResponseWriter, r *http.Request) {
	s.clearHostCookies(w, r, cookieName)
}

// setHostCookie sets the cookie name, over HTTPS as "__Host-" + name; a
// negative maxAge expires it.
func (s *Server) setHostCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge time.Duration) {
	c := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
	if maxAge < 0 {
		c.MaxAge = -1 // expire now; a sub-second duration would round to no Max-Age
	}
	if s.secureRequest(r) {
		c.Name, c.Secure = "__Host-"+name, true
	}
	http.SetCookie(w, c)
	if !c.Secure {
		// Drop a __Host- cookie from a time the server saw HTTPS, so it
		// does not outlive its value next to the plain one. A browser that
		// is on HTTPS (behind a proxy that no longer counts) removes it;
		// one on plain HTTP ignores a Secure cookie, which does no harm.
		http.SetCookie(w, &http.Cookie{Name: "__Host-" + name, Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	}
}

// clearHostCookies expires both names of the cookie name.
func (s *Server) clearHostCookies(w http.ResponseWriter, r *http.Request, name string) {
	s.setHostCookie(w, r, name, "", -1)
	if s.secureRequest(r) {
		// Also drop a cookie set before HTTPS was in front of the server.
		http.SetCookie(w, &http.Cookie{Name: name, Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	}
}

// session looks up the request's session; nil means none or not valid.
func (s *Server) session(r *http.Request) (*store.Session, error) {
	sess, _, err := s.sessionWithToken(r)
	return sess, err
}

// sessionWithToken looks up the request's session and returns it with its
// token. Of several session cookies the first valid one counts, not the
// first present one: a stale cookie next to a valid one does not log the
// user out.
func (s *Server) sessionWithToken(r *http.Request) (*store.Session, string, error) {
	now := s.now()
	for _, token := range sessionTokens(r) {
		hash := auth.HashSessionToken(token)
		sess, err := s.store.GetSession(r.Context(), hash, now, now.Add(-auth.IdleTimeout))
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		if now.Sub(sess.LastSeenAt) > touchInterval {
			if err := s.store.TouchSession(r.Context(), hash, now); err != nil {
				s.log.Warn("update session", "err", err)
			}
		}
		return sess, token, nil
	}
	return nil, "", nil
}

// authenticate lets a request through only with a valid session or API
// key, except to routes whose access in the route table is public, and to
// login routes for requests without a key. A key works only on routes
// with accessRead or accessWrite and the matching scope (requiredScope).
// A path without a route, or with a method no route takes, counts as
// session-only, so it answers 401 without a login. New endpoints are
// protected by default (accessSession is the zero value). /healthz and the
// UI code (/, /assets/) are public; they contain no data.
func (s *Server) authenticate(mux *http.ServeMux, accessOf map[string]access) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		a := accessOf[pattern]
		if a == accessPublic {
			mux.ServeHTTP(w, r)
			return
		}
		// A key request never falls back to the session cookie.
		if key, ok := bearerKey(r); ok {
			if r = s.authenticateKey(w, r, key, a); r != nil {
				mux.ServeHTTP(w, r)
			}
			return
		}
		if a == accessLogin {
			mux.ServeHTTP(w, r)
			return
		}
		sess, token, err := s.sessionWithToken(r)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return
		}
		if sess == nil {
			s.fail(w, r, http.StatusUnauthorized, "login required", nil)
			return
		}
		// A generated password, and a required 2FA that is not set up yet,
		// both restrict the session to finishing the setup: first the
		// password, then 2FA.
		if sess.MustChangePassword && (r.Method != http.MethodPut || r.URL.Path != profilePasswordPath) {
			s.writeJSON(w, http.StatusForbidden, passwordChangeJSON{Error: "choose your own password first", PasswordChangeRequired: true})
			return
		}
		if !sess.MustChangePassword && s.twoFactorRequired(sess.IsAdmin) && !sess.TwoFactorEnabled &&
			!twoFactorSetupPathAllowed(r.Method, r.URL.Path) {
			s.writeJSON(w, http.StatusForbidden, twoFactorSetupJSON{Error: "set up two-factor authentication first", TwoFactorSetupRequired: true})
			return
		}
		ctx := context.WithValue(r.Context(), sessionKey{}, sess)
		mux.ServeHTTP(w, r.WithContext(context.WithValue(ctx, sessionTokenKey{}, token)))
	})
}

type userJSON struct {
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
	// MustChangePassword: the user logged in with a generated password and
	// must choose their own before anything else works.
	MustChangePassword bool `json:"mustChangePassword"`
	// TwoFactorEnabled and TwoFactorRequired: a user who must use 2FA but
	// has not set it up can only do that until it is on.
	TwoFactorEnabled  bool `json:"twoFactorEnabled"`
	TwoFactorRequired bool `json:"twoFactorRequired"`
}

func (s *Server) userJSON(name string, admin, mustChange, twoFactor bool) userJSON {
	return userJSON{Name: name, Admin: admin, MustChangePassword: mustChange, TwoFactorEnabled: twoFactor, TwoFactorRequired: s.twoFactorRequired(admin)}
}

type sessionJSON struct {
	User userJSON `json:"user"`
}

type noSessionJSON struct {
	Error string `json:"error"`
	// SetupRequired is true while no user exists; the UI then explains how
	// to create the first admin.
	SetupRequired bool `json:"setupRequired"`
	// PasskeyOrigin is where passkey logins work; empty when they are off.
	PasskeyOrigin string `json:"passkeyOrigin,omitempty"`
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.session(r)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	if sess != nil {
		s.writeJSON(w, http.StatusOK, sessionJSON{User: s.userJSON(sess.UserName, sess.IsAdmin, sess.MustChangePassword, sess.TwoFactorEnabled)})
		return
	}
	n, err := s.store.CountUsers(r.Context())
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	s.writeJSON(w, http.StatusUnauthorized, noSessionJSON{Error: "login required", SetupRequired: n == 0, PasskeyOrigin: s.publicOrigin})
}

type loginInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

const errWrongLogin = "wrong user name or password"

type retryJSON struct {
	Error      string `json:"error"`
	RetryAfter int    `json:"retryAfter"` // seconds
}

func (s *Server) tooMany(w http.ResponseWriter, wait time.Duration, msg string) {
	secs := int(math.Ceil(wait.Seconds()))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	s.writeJSON(w, http.StatusTooManyRequests, retryJSON{Error: msg, RetryAfter: secs})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in loginInput
	if !s.decode(w, r, &in) {
		return
	}
	name := auth.NormalizeUserName(in.Username)
	addr := s.clientAddr(r)
	if wait, first := s.limiter.BlockedFirst(name, addr); wait > 0 {
		if first {
			s.logRefused(r, "password", reasonRateLimited, name)
		}
		s.tooMany(w, wait, "too many failed logins; try again later")
		return
	}
	if len(in.Password) > auth.MaxPasswordLength {
		// No stored password is that long; skip the hashing work.
		s.limiter.Fail(name, addr)
		s.auditLoginFailed(r, "password", reasonWrongPassword, name, nil)
		s.fail(w, r, http.StatusUnauthorized, errWrongLogin, nil)
		return
	}

	ctx := r.Context()
	u, err := s.store.GetUserByName(ctx, name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	ok, rehash := false, false
	if u == nil {
		s.hasher.VerifyDummy(ctx, in.Password)
	} else {
		ok, rehash, err = s.hasher.Verify(ctx, u.PasswordHash, in.Password)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return
		}
	}
	if !ok {
		s.limiter.Fail(name, addr)
		if u == nil {
			s.auditLoginFailed(r, "password", reasonUnknownUser, name, nil)
		} else {
			s.auditLoginFailed(r, "password", reasonWrongPassword, name, u)
		}
		s.fail(w, r, http.StatusUnauthorized, errWrongLogin, nil)
		return
	}
	// The browser's passkey login (autofill) stays unused after a correct
	// password, also when a second factor follows: the UI stops offering
	// it then.
	s.dropPasskeyLogin(w, r)
	if u.LockedAt != nil {
		s.auditLoginFailed(r, "password", reasonLocked, name, u)
		s.fail(w, r, http.StatusForbidden, "this user is locked", nil)
		return
	}

	if rehash {
		if h, err := s.hasher.Hash(ctx, in.Password); err == nil {
			if err := s.store.RehashUserPassword(ctx, u.ID, h); err != nil {
				s.log.Warn("renew password hash", "user", u.Name, "err", err)
			}
		}
	}
	// With 2FA on (required or voluntary), the password is only the first
	// step: the limiter is reset after the second one.
	if u.TwoFactorEnabled {
		s.startTwoFactorLogin(w, r, u)
		return
	}
	s.limiter.Succeed(name)
	// A fresh token on every login: a token planted before the login is
	// never promoted to a valid session.
	s.deleteOldSessions(r)
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	now := s.now()
	e := s.auditEntry(r, "auth.login")
	e.SetUser(u.ID, u.Name)
	e.Detail("method", "password")
	if err := s.store.StartSession(withAudit(r, e), hash, u.ID, now, now.Add(auth.MaxSessionAge), truncateUserAgent(r.UserAgent())); err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("login", "user", u.Name, "addr", s.clientIP(r))
	s.setSessionCookie(w, r, token, auth.MaxSessionAge)
	s.writeJSON(w, http.StatusOK, sessionJSON{User: s.userJSON(u.Name, u.IsAdmin, u.MustChangePassword, false)})
}

// handleLogout ends the session. A logout never fails because of the audit
// log: without its entry the session is deleted anyway.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Only a valid session writes a logout entry; every other session
	// cookie's session is deleted too.
	sess, valid, err := s.sessionWithToken(r)
	if err != nil {
		sess = nil
	}
	for _, token := range sessionTokens(r) {
		hash := auth.HashSessionToken(token)
		ctx := r.Context()
		if sess != nil && token == valid {
			e := s.auditEntry(r, "auth.logout")
			e.SetUser(sess.UserID, sess.UserName)
			ctx = withAudit(r, e)
		}
		err := s.store.EndSession(ctx, hash)
		if errors.Is(err, store.ErrAuditUnavailable) {
			s.log.Error("audit log entry lost", "action", "auth.logout", "err", err)
			err = s.store.DeleteSession(r.Context(), hash)
		}
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return
		}
	}
	s.clearSessionCookies(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// truncateUserAgent makes a user agent storable for a session: valid UTF-8
// without NUL (header values may hold other bytes) of at most 256 bytes,
// cut at a character boundary.
func truncateUserAgent(ua string) string {
	ua = strings.ReplaceAll(strings.ToValidUTF8(ua, "�"), "\x00", "�")
	if len(ua) <= 256 {
		return ua
	}
	cut := 256
	for cut > 0 && !utf8.RuneStart(ua[cut]) {
		cut--
	}
	return ua[:cut]
}

// loggableUserName returns name if it is a valid user name. Anything else
// may be a password typed into the wrong field and is not logged.
func loggableUserName(name string) string {
	if auth.ValidateUserName(name) != nil {
		return "(invalid name)"
	}
	return name
}
