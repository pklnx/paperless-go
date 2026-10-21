package web

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// testClock is the server's clock in auth tests; tests move it instead of
// waiting.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type authFixture struct {
	t      *testing.T
	st     *store.Store
	srv    *httptest.Server
	client *http.Client
	clock  *testClock
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	return newAuthFixtureOpts(t, Options{})
}

// newAuthFixtureOpts is newAuthFixture with TrustedProxies from opts.
func newAuthFixtureOpts(t *testing.T, opts Options) *authFixture {
	t.Helper()
	st := storetest.New(t)
	clock := &testClock{now: time.Now()}
	s := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		AllowedHosts: []string{"127.0.0.1"}, Hasher: storetest.Hasher, Now: clock.Now, TrustedProxies: opts.TrustedProxies,
	})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &authFixture{t: t, st: st, srv: srv, client: &http.Client{Jar: jar}, clock: clock}
}

// as returns a copy of the fixture with its own cookie jar, i.e. another
// browser.
func (f *authFixture) as() *authFixture {
	jar, _ := cookiejar.New(nil)
	g := *f
	g.client = &http.Client{Jar: jar}
	return &g
}

func (f *authFixture) addUser(name, password string, admin bool) *store.User {
	f.t.Helper()
	h, err := storetest.Hasher.Hash(context.Background(), password)
	if err != nil {
		f.t.Fatal(err)
	}
	u, err := f.st.CreateUser(context.Background(), name, h, admin)
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *authFixture) do(method, path string, body any) (*http.Response, map[string]any) {
	f.t.Helper()
	var r io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", f.srv.URL)
	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func (f *authFixture) expect(method, path string, body any, want int) map[string]any {
	f.t.Helper()
	resp, out := f.do(method, path, body)
	if resp.StatusCode != want {
		f.t.Fatalf("%s %s: status %d, want %d: %v", method, path, resp.StatusCode, want, out)
	}
	return out
}

func (f *authFixture) login(name, password string) *http.Response {
	f.t.Helper()
	resp, _ := f.do("POST", "/api/session", map[string]string{"username": name, "password": password})
	return resp
}

func (f *authFixture) loginOK(name, password string) {
	f.t.Helper()
	if resp := f.login(name, password); resp.StatusCode != 200 {
		f.t.Fatalf("login %s: %d", name, resp.StatusCode)
	}
}

// A path behind the login that answers 200 to every logged-in user who
// may do anything: the session itself. With a must-change password,
// GET /api/users answers 403 instead, so tests use that for "not
// restricted".
const sessionCheckPath = "/api/session"

func TestLoginRequired(t *testing.T) {
	f := newAuthFixture(t)

	// Without any user, the UI is told to explain the setup.
	out := f.expect("GET", "/api/session", nil, 401)
	if out["setupRequired"] != true {
		t.Fatalf("no users: %v", out)
	}
	f.addUser("alice", "correct horse battery", false)
	if out := f.expect("GET", "/api/session", nil, 401); out["setupRequired"] != false {
		t.Fatalf("with a user: %v", out)
	}

	// Every API endpoint needs a session, also unknown and write ones.
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/users"}, {"POST", "/api/users"}, {"PUT", "/api/profile/password"},
		{"DELETE", "/api/users/alice"}, {"GET", "/api/does-not-exist"}, {"PUT", "/api/session"},
		{"GET", "/api/session/elsewhere"}, {"GET", "/elsewhere"},
	} {
		if out := f.expect(c.method, c.path, nil, 401); out["error"] != "login required" {
			t.Errorf("%s %s: %v", c.method, c.path, out)
		}
	}
	// The UI code and the health check are public.
	for _, p := range []string{"/", "/healthz"} {
		resp, err := f.client.Get(f.srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == 401 {
			t.Errorf("%s needs a login", p)
		}
	}
}

// TestRouteTable walks every route: without a session, only public and
// login routes answer anything but 401.
func TestRouteTable(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	s := &Server{}
	var zero route
	if zero.access != accessSession {
		t.Fatal("a route without access is not session-only")
	}
	seen := map[access]int{}
	for _, rt := range s.routes() {
		method, path, ok := strings.Cut(rt.pattern, " ")
		if !ok {
			method, path = "GET", rt.pattern
		}
		path = strings.NewReplacer("{$}", "", "{name}", "alice").Replace(path)
		if strings.HasSuffix(path, "/") && path != "/" {
			path += "x"
		}
		seen[rt.access]++
		resp, out := f.do(method, path, map[string]string{})
		switch rt.access {
		case accessSession:
			if resp.StatusCode != 401 || out["error"] != "login required" {
				t.Errorf("%s: %d %v, want 401 without a session", rt.pattern, resp.StatusCode, out)
			}
		case accessPublic:
			if resp.StatusCode == 401 {
				t.Errorf("public %s: 401", rt.pattern)
			}
			if strings.HasPrefix(path, "/api/") {
				t.Errorf("public API route %s", rt.pattern)
			}
		case accessLogin:
			if !strings.HasPrefix(rt.pattern, "GET /api/session") && !strings.HasPrefix(rt.pattern, "POST /api/session") &&
				!strings.HasPrefix(rt.pattern, "DELETE /api/session") {
				t.Errorf("login access outside /api/session: %s", rt.pattern)
			}
		}
	}
	// The public routes are exactly these three.
	var public []string
	for _, rt := range s.routes() {
		if rt.access == accessPublic {
			public = append(public, rt.pattern)
		}
	}
	if got := strings.Join(public, ","); got != "GET /healthz,GET /{$},GET /assets/" {
		t.Errorf("public routes: %s", got)
	}
	if seen[accessSession] == 0 || seen[accessLogin] != 3 {
		t.Errorf("routes per access: %v", seen)
	}
}

func TestUnknownAPIPathWithSession(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	resp, out := f.do("GET", "/api/does-not-exist", nil)
	if resp.StatusCode != 404 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") || out["error"] != "not found" {
		t.Fatalf("unknown API path: %d %q %v", resp.StatusCode, resp.Header.Get("Content-Type"), out)
	}
}

func TestLoginLogout(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", true)

	if resp := f.login("alice", "wrong horse battery"); resp.StatusCode != 401 {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}
	if resp := f.login("nobody", "correct horse battery"); resp.StatusCode != 401 {
		t.Fatalf("unknown user: %d", resp.StatusCode)
	}
	_, wrongPw := f.do("POST", "/api/session", map[string]string{"username": "alice", "password": "x"})
	_, unknown := f.do("POST", "/api/session", map[string]string{"username": "nobody", "password": "x"})
	if wrongPw["error"] != unknown["error"] {
		t.Fatalf("answers differ: %v / %v", wrongPw, unknown)
	}
	f.expect("POST", "/api/session", map[string]string{"username": "alice"}, 401)
	f.expect("POST", "/api/session", map[string]string{"username": "alice", "password": strings.Repeat("x", auth.MaxPasswordLength+1)}, 401)
	f.expect("POST", "/api/session", map[string]string{"user": "alice"}, 400)

	// Names are case-insensitive.
	resp := f.login(" Alice ", "correct horse battery")
	if resp.StatusCode != 200 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	var c *http.Cookie
	for _, k := range resp.Cookies() {
		if k.Name == cookieName {
			c = k
		}
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Secure || c.MaxAge != int(auth.MaxSessionAge.Seconds()) {
		t.Fatalf("cookie: %+v", c)
	}
	out := f.expect("GET", "/api/session", nil, 200)
	if user, _ := out["user"].(map[string]any); user["name"] != "alice" || user["admin"] != true || user["mustChangePassword"] != false {
		t.Fatalf("session: %v", out)
	}
	f.expect("GET", "/api/users", nil, 200)

	u, _ := f.st.GetUserByName(context.Background(), "alice")
	if u.LastLoginAt == nil || !u.LastLoginAt.Equal(f.clock.Now().Truncate(time.Microsecond)) {
		t.Errorf("last login: %v", u.LastLoginAt)
	}

	f.expect("DELETE", "/api/session", nil, 204)
	f.expect("GET", "/api/users", nil, 401)
	// Logging out without a session is fine.
	f.expect("DELETE", "/api/session", nil, 204)
}

func TestLoginWithStolenCookieAfterLogout(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	u, _ := url.Parse(f.srv.URL)
	old := f.client.Jar.Cookies(u)

	f.expect("DELETE", "/api/session", nil, 204)
	f.client.Jar.SetCookies(u, old) // the old token is dead on the server
	f.expect("GET", sessionCheckPath, nil, 401)
}

// A login always issues a new token, and ends the session of the cookie
// the browser sent: a token planted before the login never becomes valid.
func TestLoginReplacesToken(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	u, _ := url.Parse(f.srv.URL)
	first := f.client.Jar.Cookies(u)
	f.loginOK("alice", "correct horse battery")
	second := f.client.Jar.Cookies(u)
	if len(first) != 1 || len(second) != 1 || first[0].Value == second[0].Value {
		t.Fatalf("cookies %v then %v", first, second)
	}
	other := f.as()
	other.client.Jar.SetCookies(u, first)
	other.expect("GET", sessionCheckPath, nil, 401)
	f.expect("GET", sessionCheckPath, nil, 200)
}

func TestSessionEnds(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	alice := f.addUser("alice", "correct horse battery", false)
	f.addUser("admin", "correct horse battery", true)

	// A new password ends existing sessions.
	f.loginOK("alice", "correct horse battery")
	f.expect("GET", sessionCheckPath, nil, 200)
	h, _ := storetest.Hasher.Hash(ctx, "another horse battery")
	if err := f.st.SetUserPassword(ctx, alice.ID, h, false); err != nil {
		t.Fatal(err)
	}
	f.expect("GET", sessionCheckPath, nil, 401)

	// So does locking, and a locked user cannot log in again.
	f.loginOK("alice", "another horse battery")
	f.expect("GET", sessionCheckPath, nil, 200)
	if err := f.st.SetUserLocked(ctx, alice.ID, true); err != nil {
		t.Fatal(err)
	}
	f.expect("GET", sessionCheckPath, nil, 401)
	if resp := f.login("alice", "another horse battery"); resp.StatusCode != 403 {
		t.Fatalf("locked login: %d", resp.StatusCode)
	}
	// A wrong password for a locked user gets the usual answer.
	if resp := f.login("alice", "wrong horse battery"); resp.StatusCode != 401 {
		t.Fatalf("locked, wrong password: %d", resp.StatusCode)
	}
}

// Sessions end after IdleTimeout without use and after MaxSessionAge at
// the latest; use within the idle time keeps them alive. The server clock
// moves, nothing waits.
func TestSessionExpiresOnServerClock(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)

	f.loginOK("alice", "correct horse battery")
	f.clock.Advance(auth.IdleTimeout - time.Minute)
	f.expect("GET", sessionCheckPath, nil, 200) // used: idle time starts again
	f.clock.Advance(auth.IdleTimeout - time.Minute)
	f.expect("GET", sessionCheckPath, nil, 200)
	f.clock.Advance(auth.IdleTimeout + time.Second)
	f.expect("GET", sessionCheckPath, nil, 401)

	// Used every day, a session still ends after MaxSessionAge.
	f.loginOK("alice", "correct horse battery")
	start := f.clock.Now()
	for f.clock.Now().Sub(start) < auth.MaxSessionAge-24*time.Hour {
		f.clock.Advance(24 * time.Hour)
		f.expect("GET", sessionCheckPath, nil, 200)
	}
	f.clock.Advance(24 * time.Hour)
	f.expect("GET", sessionCheckPath, nil, 401)

	// The cleanup removes ended sessions.
	s := New(f.st, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Now: f.clock.Now})
	s.deleteExpiredSessions(context.Background())
	now := f.clock.Now()
	if n, err := f.st.DeleteExpiredSessions(context.Background(), now, now.Add(-auth.IdleTimeout)); err != nil || n != 0 {
		t.Fatalf("sessions left after the cleanup: %d, %v", n, err)
	}
}

func TestLoginRateLimit(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	for range 5 {
		if resp := f.login("alice", "wrong horse battery"); resp.StatusCode != 401 {
			t.Fatalf("status %d", resp.StatusCode)
		}
	}
	// Blocked: even the right password waits, and the block is announced.
	resp, out := f.do("POST", "/api/session", map[string]string{"username": "alice", "password": "correct horse battery"})
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "60" || out["retryAfter"] != float64(60) {
		t.Fatalf("blocked login: %d %q %v", resp.StatusCode, resp.Header.Get("Retry-After"), out)
	}
	// Other names are not affected.
	f.addUser("bob", "correct horse battery", false)
	if resp := f.login("bob", "correct horse battery"); resp.StatusCode != 200 {
		t.Fatalf("other user: %d", resp.StatusCode)
	}
	// After the block, the right password works.
	f.clock.Advance(time.Minute)
	if resp := f.as().login("alice", "correct horse battery"); resp.StatusCode != 200 {
		t.Fatalf("after the block: %d", resp.StatusCode)
	}
}

// Behind a trusted proxy the limits count per client from
// X-Forwarded-For; from anyone else the header is ignored.
func TestLoginRateLimitBehindProxy(t *testing.T) {
	f := newAuthFixtureOpts(t, Options{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
	n := 0
	post := func(client string) int {
		n++ // a new name each time: only the address counts
		b, _ := json.Marshal(map[string]string{"username": fmt.Sprintf("u%d", n), "password": "wrong horse battery"})
		req, _ := http.NewRequest("POST", f.srv.URL+"/api/session", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", f.srv.URL)
		req.Header.Set("X-Forwarded-For", client)
		resp, err := f.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	// 20 failures from one client block that client's address only.
	for range 20 {
		if code := post("198.51.100.7"); code != 401 {
			t.Fatalf("status %d", code)
		}
	}
	if code := post("198.51.100.7"); code != 429 {
		t.Fatalf("blocked client: %d", code)
	}
	if code := post("198.51.100.8"); code != 401 {
		t.Fatalf("other client: %d", code)
	}
}

func TestSecureCookie(t *testing.T) {
	st := storetest.New(t)
	s := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{AllowedHosts: []string{"docs.example"}, Hasher: storetest.Hasher})
	storetest.User(t, st, "alice", false)
	for _, c := range []struct {
		name   string
		header string
		tls    bool
	}{{"proxy", "https", false}, {"direct tls", "", true}} {
		t.Run(c.name, func(t *testing.T) {
			body := `{"username":"alice","password":"` + storetest.Password + `"}`
			req := httptest.NewRequest("POST", "https://docs.example/api/session", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "https://docs.example")
			if c.header != "" {
				req.Header.Set("X-Forwarded-Proto", c.header)
			}
			if !c.tls {
				req.TLS = nil
			} else {
				req.TLS = &tls.ConnectionState{}
			}
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			cookies := rec.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name != secureCookieName || !cookies[0].Secure {
				t.Fatalf("cookies: %+v", cookies)
			}
		})
	}
}

func TestRequestScope(t *testing.T) {
	with := func(sess *store.Session) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		if sess != nil {
			r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, sess))
		}
		return r
	}
	for _, c := range []struct {
		name string
		sess *store.Session
		want store.Scope
	}{
		{"no principal", nil, store.Scope{}},
		{"user", &store.Session{UserID: 7, UserName: "kim"}, store.Scope{UserID: 7}},
		{"admin", &store.Session{UserID: 3, UserName: "admin", IsAdmin: true}, store.Scope{UserID: 3, All: true}},
	} {
		if got := requestScope(with(c.sess)); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestClientIP(t *testing.T) {
	s := &Server{trustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("::1/128")}}
	for _, c := range []struct {
		remote string
		xff    []string
		want   string
	}{
		{"192.0.2.1:1234", nil, "192.0.2.1"},
		// An untrusted peer cannot choose its address.
		{"192.0.2.1:1234", []string{"198.51.100.7"}, "192.0.2.1"},
		{"10.0.0.2:1234", []string{"198.51.100.7"}, "198.51.100.7"},
		// Trusted proxies in the chain are skipped; the left part is the
		// client's own claim and not trusted.
		{"10.0.0.2:1234", []string{"6.6.6.6, 198.51.100.7, 10.0.0.9"}, "198.51.100.7"},
		{"[::1]:1234", []string{"198.51.100.7", "10.1.1.1"}, "198.51.100.7"},
		{"10.0.0.2:1234", []string{"not an address"}, "10.0.0.2"},
		{"10.0.0.2:1234", nil, "10.0.0.2"},
		{"10.0.0.2:1234", []string{"10.0.0.3"}, "10.0.0.2"},
		{"[::ffff:192.0.2.1]:1234", nil, "192.0.2.1"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		for _, h := range c.xff {
			r.Header.Add("X-Forwarded-For", h)
		}
		if got := s.clientIP(r).String(); got != c.want {
			t.Errorf("%s %v: %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "pipe"
	if got := s.clientAddr(r); got != "pipe" {
		t.Errorf("no address: %q", got)
	}
}

func TestTruncateUserAgent(t *testing.T) {
	if got := truncateUserAgent("a\x00b\xff"); got != "a�b�" {
		t.Errorf("invalid bytes: %q", got)
	}
	long := strings.Repeat("ä", 200) // 400 bytes
	if got := truncateUserAgent(long); len(got) > 256 || !strings.HasPrefix(long, got) || len(got) != 256 {
		t.Errorf("long: %d bytes", len(got))
	}
}

// A failing database is an internal error, not a missing login.
func TestSessionLookupFails(t *testing.T) {
	st := storetest.New(t)
	h := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{}).Handler()
	st.Close()
	r := httptest.NewRequest("GET", "http://localhost/api/users", nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: "token"})
	if rec := serve(h, r); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	if err := st.Ping(context.Background()); err == nil || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("closed store pings: %v", err)
	}
}
