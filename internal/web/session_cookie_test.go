package web

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// cookieRequest is a request to docs.example from remote, without TLS
// unless tlsOn is set, with X-Forwarded-Proto proto if not empty, and the
// cookies in this order.
func cookieRequest(method, path, remote, proto string, tlsOn bool, cookies ...*http.Cookie) *http.Request {
	req := httptest.NewRequest(method, "https://docs.example"+path, nil)
	req.RemoteAddr = remote
	req.Header.Set("Origin", "https://docs.example")
	if proto != "" {
		req.Header.Set("X-Forwarded-Proto", proto)
	}
	req.TLS = nil
	if tlsOn {
		req.TLS = &tls.ConnectionState{}
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return req
}

// newSession starts a session of the user and returns its token.
func newSession(t *testing.T, st *store.Store, userID int64) string {
	t.Helper()
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.StartSession(context.Background(), hash, userID, now, now.Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	return token
}

// sessionExists reports whether the session of token is still stored.
func sessionExists(t *testing.T, st *store.Store, token string) bool {
	t.Helper()
	now := time.Now()
	_, err := st.GetSession(context.Background(), auth.HashSessionToken(token), now, now.Add(-auth.IdleTimeout))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

// On a request over HTTPS only the __Host- cookie logs in, on a plain
// request only the plain one. A plain cookie planted over plain HTTP or
// from a sibling host name, alone or next to a __Host- cookie of an ended
// session, must not log the user into the planter's session (login CSRF).
func TestSessionCookieOfTheScheme(t *testing.T) {
	st := storetest.New(t)
	s := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		AllowedHosts: []string{"docs.example"}, Hasher: storetest.Hasher,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	})
	h := s.Handler()
	alice := storetest.User(t, st, "alice", false)
	mallory := storetest.User(t, st, "mallory", false)
	planted := &http.Cookie{Name: cookieName, Value: newSession(t, st, mallory.ID)}
	stale := &http.Cookie{Name: secureCookieName, Value: "stale-token-of-an-ended-session"}
	own := newSession(t, st, alice.ID)
	ownPlain := &http.Cookie{Name: cookieName, Value: own}
	ownSecure := &http.Cookie{Name: secureCookieName, Value: own}

	type cookieCase struct {
		name    string
		cookies []*http.Cookie
		want    int
	}
	check := func(t *testing.T, remote, proto string, tlsOn bool, c cookieCase) {
		t.Helper()
		for _, path := range []string{"/api/session", "/api/tags"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, cookieRequest("GET", path, remote, proto, tlsOn, c.cookies...))
			if rec.Code != c.want {
				t.Errorf("%s, %s: %d, want %d: %s", c.name, path, rec.Code, c.want, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "mallory") {
				t.Errorf("%s, %s: logged in as the planter: %s", c.name, path, rec.Body)
			}
		}
	}

	// Secure: X-Forwarded-Proto from loopback (also IPv4-mapped) or a
	// trusted proxy.
	for _, remote := range []string{"127.0.0.1:4000", "[::ffff:127.0.0.1]:4000", "10.1.2.3:4000"} {
		t.Run("secure from "+remote, func(t *testing.T) {
			for _, c := range []cookieCase{
				{"planted plain next to a stale __Host-", []*http.Cookie{stale, planted}, 401},
				{"planted plain alone", []*http.Cookie{planted}, 401},
				{"own session in a plain cookie", []*http.Cookie{ownPlain}, 401},
				{"own session in __Host- next to a planted plain", []*http.Cookie{planted, ownSecure}, 200},
			} {
				check(t, remote, "https", false, c)
			}
		})
	}
	t.Run("plain", func(t *testing.T) {
		for _, c := range []cookieCase{
			{"own session in a __Host- cookie", []*http.Cookie{ownSecure}, 401},
			{"own session in a plain cookie next to a stale __Host-", []*http.Cookie{stale, ownPlain}, 200},
		} {
			check(t, "192.0.2.1:1234", "", false, c)
		}
	})
	// The header from an untrusted peer does not count: the request is a
	// plain one.
	t.Run("untrusted proxy claims HTTPS", func(t *testing.T) {
		for _, c := range []cookieCase{
			{"own session in a plain cookie", []*http.Cookie{ownPlain}, 200},
			{"own session in a __Host- cookie", []*http.Cookie{ownSecure}, 401},
		} {
			check(t, "192.0.2.1:1234", "https", false, c)
		}
	})
	// Completes the matrix: a request with r.TLS set counts as HTTPS without
	// any header (secureRequest checks it first). The server has no TLS
	// listener today, so no deployment reaches this; the test pins the
	// function, not a setup. /api/tags needs a session, so the plain cookies
	// get 401 there too.
	t.Run("direct TLS", func(t *testing.T) {
		for _, c := range []cookieCase{
			{"own session in __Host- next to a planted plain", []*http.Cookie{planted, ownSecure}, 200},
			{"planted plain alone", []*http.Cookie{planted}, 401},
			{"own session in a plain cookie", []*http.Cookie{ownPlain}, 401},
		} {
			check(t, "192.0.2.1:1234", "", true, c)
		}
	})
	// A logout over an HTTPS request ends the session of the planted plain
	// cookie too, not only the one of the scheme's __Host- cookie, and
	// expires both cookie names. Fresh sessions: own and planted above are
	// shared with the other cases.
	t.Run("logout over a secure request", func(t *testing.T) {
		ownToken := newSession(t, st, alice.ID)
		plantedToken := newSession(t, st, mallory.ID)
		ownC := &http.Cookie{Name: secureCookieName, Value: ownToken}
		plantedC := &http.Cookie{Name: cookieName, Value: plantedToken}
		req := cookieRequest("DELETE", "/api/session", "10.1.2.3:4000", "https", false, plantedC, ownC)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("logout: %d, want 204: %s", rec.Code, rec.Body)
		}
		for name, token := range map[string]string{"own __Host- session": ownToken, "planted plain session": plantedToken} {
			if sessionExists(t, st, token) {
				t.Errorf("%s outlived the logout", name)
			}
		}
		expired := map[string]bool{}
		for _, c := range rec.Result().Cookies() {
			if c.MaxAge < 0 {
				expired[c.Name] = true
			}
		}
		if !expired[secureCookieName] || !expired[cookieName] {
			t.Errorf("expired cookies %v, want both %s and %s", expired, secureCookieName, cookieName)
		}
		// The planted plain cookie, sent as a plain request from a peer that is
		// not a trusted proxy, is where the session would still log in if the
		// logout had ended only the scheme's cookie session.
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, cookieRequest("GET", "/api/session", "192.0.2.1:1234", "", false, plantedC))
		if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "mallory") {
			t.Errorf("planted cookie after the logout over plain HTTP: %d: %s", rec.Code, rec.Body)
		}
	})
}

// A login and logout end the sessions of both session cookie names, the
// __Host- one first, without empty values or duplicates and at most
// maxSessionCookies per name: each token costs a database lookup. Only the
// name of the request's scheme logs in.
func TestSessionTokens(t *testing.T) {
	plain := func(v string) *http.Cookie { return &http.Cookie{Name: cookieName, Value: v} }
	secure := func(v string) *http.Cookie { return &http.Cookie{Name: secureCookieName, Value: v} }
	junk := make([]*http.Cookie, 0, 102)
	for i := range 100 {
		junk = append(junk, plain(fmt.Sprintf("junk%d", i)))
	}
	junk = append(junk, secure("same"), secure("same"))
	for _, c := range []struct {
		name    string
		cookies []*http.Cookie
		want    string
	}{
		{"none", nil, ""},
		{"__Host- first", []*http.Cookie{plain("a"), secure("s")}, "s,a"},
		{"empty and duplicates", []*http.Cookie{plain(""), plain("a"), secure("a"), plain("b")}, "a,b"},
		{"capped per name", []*http.Cookie{plain("1"), plain("2"), plain("3"), secure("s1"), secure("s2"), secure("s3")}, "s1,s2,1,2"},
		{"a hundred plain, one __Host- twice", junk, "same,junk0,junk1"},
		{"other names", []*http.Cookie{{Name: "other", Value: "x"}, plain("a")}, "a"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		for _, k := range c.cookies {
			r.AddCookie(k)
		}
		if got := strings.Join(allSessionTokens(r), ","); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	s := &Server{}
	for _, c := range []struct {
		name   string
		remote string
		proto  string
		want   string
	}{
		{"secure", "127.0.0.1:4000", "https", "s1,s2"},
		{"secure, IPv4-mapped loopback", "[::ffff:127.0.0.1]:4000", "https", "s1,s2"},
		{"plain", "192.0.2.1:1234", "", "1,2"},
		{"untrusted proxy", "192.0.2.1:1234", "https", "1,2"},
	} {
		r := cookieRequest("GET", "/", c.remote, c.proto, false,
			plain("1"), plain("2"), plain("3"), secure("s1"), secure("s2"), secure("s3"))
		if got := strings.Join(s.sessionTokens(r), ","); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// addStaleCookie adds a plain session cookie of an ended session for the
// path /api to f's browser. The browser sends it before its session cookie
// for /, which has the same name.
func (f *authFixture) addStaleCookie() {
	f.t.Helper()
	u, err := url.Parse(f.srv.URL + sessionCheckPath)
	if err != nil {
		f.t.Fatal(err)
	}
	f.client.Jar.SetCookies(u, []*http.Cookie{{Name: cookieName, Value: "stale-token-of-an-ended-session", Path: "/api"}})
	if cookies := f.client.Jar.Cookies(u); len(cookies) != 2 || cookies[0].Value != "stale-token-of-an-ended-session" {
		f.t.Fatalf("cookies in the browser: %+v, want the stale one first", cookies)
	}
}

// The endpoints that end a user's other sessions keep the valid one, not
// the session of the first cookie.
func TestStaleCookieKeepsValidSession(t *testing.T) {
	t.Run("password change", func(t *testing.T) {
		f := newAuthFixture(t)
		f.addUser("kim", "kims own passphrase", false)
		f.loginOK("kim", "kims own passphrase")
		other := f.as()
		other.loginOK("kim", "kims own passphrase")
		f.addStaleCookie()
		f.expect("PUT", "/api/profile/password", map[string]string{"current": "kims own passphrase", "new": "a brand new passphrase"}, 204)
		f.expect("GET", "/api/tags", nil, 200)
		other.expect("GET", "/api/tags", nil, 401)
	})
	t.Run("2fa disable", func(t *testing.T) {
		f := newAuthFixture(t)
		u := f.addUser("kim", "correct horse battery", false)
		secret, _ := f.enableTOTP(u)
		f.loginOK("kim", "correct horse battery")
		f.addStaleCookie()
		f.expect("DELETE", "/api/profile/2fa", map[string]string{"currentPassword": "correct horse battery", "code": f.code(secret)}, 204)
		f.expect("GET", "/api/tags", nil, 200)
	})
	t.Run("passkey removal", func(t *testing.T) {
		f := newPasskeyFixture(t)
		f.addUser("kim", "correct horse battery", false)
		f.loginOK("kim", "correct horse battery")
		f.addPasskey(newSoftKey(t), "Phone", "correct horse battery", "")
		f.addStaleCookie()
		id := f.expect("GET", "/api/profile/passkeys", nil, 200)["passkeys"].([]any)[0].(map[string]any)["id"].(float64)
		f.expect("DELETE", fmt.Sprintf("/api/profile/passkeys/%d", int64(id)), nil, 200)
		f.expect("GET", "/api/tags", nil, 200)
	})
}

// A login, also the password step of a 2FA login and a passkey login,
// ends the sessions of both session cookies, and so does logout: only the
// name of the request's scheme logs in, but a session of the other name
// must not stay valid in the database where this browser cannot reach it.
func TestLoginAndLogoutEndAllCookieSessions(t *testing.T) {
	// withTwoSessions puts two valid sessions of u into f's browser, one
	// per cookie name, and returns their tokens.
	withTwoSessions := func(f *authFixture, u *store.User) []string {
		f.t.Helper()
		su, err := url.Parse(f.srv.URL)
		if err != nil {
			f.t.Fatal(err)
		}
		tokens := []string{newSession(f.t, f.st, u.ID), newSession(f.t, f.st, u.ID)}
		f.client.Jar.SetCookies(su, []*http.Cookie{
			{Name: secureCookieName, Value: tokens[0], Path: "/"},
			{Name: cookieName, Value: tokens[1], Path: "/"},
		})
		return tokens
	}
	ended := func(t *testing.T, f *authFixture, tokens []string) {
		t.Helper()
		for i, token := range tokens {
			if sessionExists(t, f.st, token) {
				t.Errorf("session %d outlived the change", i)
			}
		}
	}
	t.Run("password login", func(t *testing.T) {
		f := newAuthFixture(t)
		u := f.addUser("kim", "correct horse battery", false)
		tokens := withTwoSessions(f, u)
		f.loginOK("kim", "correct horse battery")
		ended(t, f, tokens)
	})
	t.Run("2fa password step", func(t *testing.T) {
		f := newAuthFixture(t)
		u := f.addUser("kim", "correct horse battery", false)
		f.enableTOTP(u)
		tokens := withTwoSessions(f, u)
		f.passwordStep("kim", "correct horse battery")
		ended(t, f, tokens)
	})
	t.Run("passkey login", func(t *testing.T) {
		f := newPasskeyFixture(t)
		u := f.addUser("kim", "correct horse battery", false)
		f.loginOK("kim", "correct horse battery")
		k := newSoftKey(t)
		f.addPasskey(k, "Phone", "correct horse battery", "")
		g := f.as()
		tokens := withTwoSessions(g, u)
		g.passkeyLogin(k, 200)
		ended(t, f, tokens)
	})
	t.Run("logout", func(t *testing.T) {
		f := newAuthFixture(t)
		u := f.addUser("kim", "correct horse battery", false)
		tokens := withTwoSessions(f, u)
		f.expect("DELETE", "/api/session", nil, 204)
		ended(t, f, tokens)
	})
}

// A request with a Host that is not allowed does not use up the one
// warning about an ignored X-Forwarded-For: a scanner or a LAN client would
// otherwise get its address named as the proxy, and the real
// misconfiguration would log nothing. Both requests go to the same server:
// the warning is once per server.
func TestWarnIgnoredForwardedForAfterHostCheck(t *testing.T) {
	const msg = "X-Forwarded-For from a peer that is not a trusted proxy is ignored"
	var buf bytes.Buffer
	h := New(nil, slog.New(slog.NewTextHandler(&buf, nil)), Options{AllowedHosts: []string{"docs.example"}, Pinger: fakeDB{}}).Handler()
	wrong := httptest.NewRequest("GET", "https://scanner.example/healthz", nil)
	wrong.TLS, wrong.RemoteAddr = nil, "198.51.100.9:1234"
	wrong.Header.Set("X-Forwarded-For", "203.0.113.5")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, wrong)
	if rec.Code != 403 || strings.Contains(buf.String(), msg) {
		t.Fatalf("wrong host: %d, log: %s", rec.Code, buf.String())
	}
	allowed := cookieRequest("GET", "/healthz", "192.0.2.1:1234", "", false)
	allowed.Header.Set("X-Forwarded-For", "203.0.113.5")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, allowed)
	if rec.Code != 200 || strings.Count(buf.String(), msg) != 1 || !strings.Contains(buf.String(), "peer=192.0.2.1") ||
		strings.Contains(buf.String(), "198.51.100.9") {
		t.Fatalf("allowed host: %d, log: %s", rec.Code, buf.String())
	}
}
