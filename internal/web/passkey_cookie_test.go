package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/store"
)

// noCookies returns a copy of the fixture whose client keeps no cookies,
// like a script.
func (f *authFixture) noCookies() *authFixture {
	g := *f
	g.client = &http.Client{}
	return &g
}

// openLogins counts the stored passkey logins.
func (f *authFixture) openLogins() int {
	f.t.Helper()
	conn, err := pgx.Connect(f.t.Context(), f.dbURL)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = conn.Close(f.t.Context()) }()
	var n int
	if err := conn.QueryRow(f.t.Context(), `SELECT count(*) FROM webauthn_ceremonies WHERE kind = 'login'`).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// raw sends a request with the given headers and cookies and no cookie
// jar, and returns the response with its body read.
func (f *authFixture) raw(method, path string, body any, header http.Header, cookies ...*http.Cookie) (*http.Response, map[string]any) {
	f.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, f.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", f.srv.URL)
	for k, v := range header {
		req.Header[k] = v
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

// setCookie returns the response's cookie name, or nil.
func setCookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// expired reports whether resp expires the cookie name.
func expired(resp *http.Response, name string) bool {
	c := setCookie(resp, name)
	return c != nil && c.MaxAge < 0
}

const securePasskeyCookieName = "__Host-" + passkeyCookieName

// A browser that loads the login page again and again (each load starts an
// autofill login) holds one open login: its cookie replaces the earlier
// one. Behind a proxy that is not trusted all clients share one address,
// so without that the cap would lock everybody out after 20 loads.
func TestPasskeyLoginCookieReplaces(t *testing.T) {
	f := newPasskeyFixture(t)
	for i := range store.MaxOpenLoginCeremoniesPerClient + 5 {
		resp, out := f.do("POST", passkeyLoginBeginPath, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("begin %d: %d %v", i, resp.StatusCode, out)
		}
		c := setCookie(resp, passkeyCookieName)
		if c == nil || c.Value != out["token"] || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" ||
			c.MaxAge != int(passkeyCeremonyTTL.Seconds()) || c.Secure {
			t.Fatalf("cookie: %+v", c)
		}
	}
	if n := f.openLogins(); n != 1 {
		t.Fatalf("%d open logins, want 1", n)
	}
}

// Without the cookie every begin counts, as for a script.
func TestPasskeyLoginWithoutCookieCounts(t *testing.T) {
	f := newPasskeyFixture(t).noCookies()
	for range store.MaxOpenLoginCeremoniesPerClient {
		f.beginPasskeyLogin()
	}
	resp, out := f.do("POST", passkeyLoginBeginPath, nil)
	if resp.StatusCode != 429 || out["error"] != errTooManyOpenPasskey {
		t.Fatalf("over the cap: %d %v", resp.StatusCode, out)
	}
	// A refused begin sets no cookie.
	if c := setCookie(resp, passkeyCookieName); c != nil {
		t.Fatalf("cookie on a refused begin: %+v", c)
	}
}

// A refused begin neither sets nor clears the browser's cookie.
func TestPasskeyRefusedBeginKeepsCookie(t *testing.T) {
	f := newPasskeyFixture(t)
	token, _ := f.beginPasskeyLogin()
	// A script from the same address uses up the cap and the begin limit.
	script := f.noCookies()
	for range store.MaxOpenLoginCeremoniesPerClient - 1 {
		script.beginPasskeyLogin()
	}
	for range passkeyBeginLimit - store.MaxOpenLoginCeremoniesPerClient {
		script.expect("POST", passkeyLoginBeginPath, nil, 429)
	}
	resp, out := f.do("POST", passkeyLoginBeginPath, nil)
	if resp.StatusCode != 429 || out["error"] != errTooManyPasskey {
		t.Fatalf("begin over the limit: %d %v", resp.StatusCode, out)
	}
	if len(resp.Header.Values("Set-Cookie")) != 0 {
		t.Fatalf("cookies on a refused begin: %v", resp.Header.Values("Set-Cookie"))
	}
	if c := f.jarCookie(passkeyCookieName); c == nil || c.Value != token {
		t.Fatalf("cookie after a refused begin: %+v", c)
	}
	// Without passkeys: 503, and no cookie either.
	off := newAuthFixture(t)
	resp, _ = off.do("POST", passkeyLoginBeginPath, nil)
	if resp.StatusCode != 503 || len(resp.Header.Values("Set-Cookie")) != 0 {
		t.Fatalf("passkeys off: %d %v", resp.StatusCode, resp.Header.Values("Set-Cookie"))
	}
}

// jarCookie returns the cookie name in the fixture's jar, or nil.
func (f *authFixture) jarCookie(name string) *http.Cookie {
	f.t.Helper()
	req, _ := http.NewRequest("GET", f.srv.URL+"/", nil)
	for _, c := range f.client.Jar.Cookies(req.URL) {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// A correct password drops the browser's unused autofill login and its
// cookie, also when a second factor follows; a wrong one keeps it.
func TestPasswordLoginDropsPasskeyLogin(t *testing.T) {
	f := newPasskeyFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.beginPasskeyLogin()
	if resp := f.login("alice", "wrong horse battery"); resp.StatusCode != 401 || len(resp.Header.Values("Set-Cookie")) != 0 {
		t.Fatalf("wrong password: %d %v", resp.StatusCode, resp.Header.Values("Set-Cookie"))
	}
	if n := f.openLogins(); n != 1 {
		t.Fatalf("%d open logins after a wrong password, want 1", n)
	}
	resp := f.login("alice", "correct horse battery")
	if resp.StatusCode != 200 || !expired(resp, passkeyCookieName) || !expired(resp, securePasskeyCookieName) {
		t.Fatalf("password login: %d %v", resp.StatusCode, resp.Header.Values("Set-Cookie"))
	}
	if n := f.openLogins(); n != 0 {
		t.Fatalf("%d open logins after a password login, want 0", n)
	}
	if c := f.jarCookie(passkeyCookieName); c != nil {
		t.Fatalf("cookie kept: %+v", c)
	}

	// With 2FA: already at the password step, since the UI stops offering
	// the passkey then.
	b := f.as()
	bob := f.addUser("bob", "correct horse battery", false)
	f.enableTOTP(bob)
	b.beginPasskeyLogin()
	b.passwordStep("bob", "correct horse battery")
	if n := f.openLogins(); n != 0 {
		t.Fatalf("%d open logins after the password step, want 0", n)
	}
	if c := b.jarCookie(passkeyCookieName); c != nil {
		t.Fatalf("cookie kept at the password step: %+v", c)
	}
}

// A passkey login uses up its ceremony and expires the cookie.
func TestPasskeyFinishExpiresCookie(t *testing.T) {
	f := newPasskeyFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	k := newSoftKey(t)
	f.addPasskey(k, "Phone", "correct horse battery", "")
	a := f.as()
	token, pk := a.beginPasskeyLogin()
	resp, out := a.do("POST", passkeyLoginFinishPath, map[string]any{"token": token, "credential": k.get(pk)})
	if resp.StatusCode != 200 || !expired(resp, passkeyCookieName) {
		t.Fatalf("finish: %d %v %v", resp.StatusCode, out, resp.Header.Values("Set-Cookie"))
	}
	if c := a.jarCookie(passkeyCookieName); c != nil {
		t.Fatalf("cookie kept: %+v", c)
	}
	if n := f.openLogins(); n != 0 {
		t.Fatalf("%d open logins, want 0", n)
	}
}

// Two tabs share the cookie: the second tab's begin replaces the first
// tab's login, which then answers "expired" (the UI starts a new one).
func TestPasskeyLoginTwoTabs(t *testing.T) {
	f := newPasskeyFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	k := newSoftKey(t)
	f.addPasskey(k, "Phone", "correct horse battery", "")
	browser := f.as()
	tokenA, pkA := browser.beginPasskeyLogin()
	browser.beginPasskeyLogin()
	out := browser.expect("POST", passkeyLoginFinishPath, map[string]any{"token": tokenA, "credential": k.get(pkA)}, 401)
	if out["error"] != "the passkey login has expired; try again" {
		t.Fatalf("first tab: %v", out)
	}
}

// Over HTTPS the cookie is the __Host- one. A browser can hold both names
// (HTTPS once, then a proxy that no longer counts): a begin replaces both,
// and a password login drops both and expires both names.
func TestPasskeyLoginCookieOverHTTPS(t *testing.T) {
	f := newAuthFixtureOpts(t, Options{PublicURL: testPublicURL, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
	f.addUser("alice", "correct horse battery", false)
	https := http.Header{"X-Forwarded-Proto": {"https"}}
	resp, out := f.raw("POST", passkeyLoginBeginPath, nil, https)
	secure := setCookie(resp, securePasskeyCookieName)
	if resp.StatusCode != 200 || secure == nil || !secure.Secure || secure.Value != out["token"] || setCookie(resp, passkeyCookieName) != nil {
		t.Fatalf("over HTTPS: %d %v", resp.StatusCode, resp.Header.Values("Set-Cookie"))
	}
	resp, _ = f.raw("POST", passkeyLoginBeginPath, nil, nil)
	plain := setCookie(resp, passkeyCookieName)
	if plain == nil || plain.Secure || !expired(resp, securePasskeyCookieName) {
		t.Fatalf("over HTTP: %v", resp.Header.Values("Set-Cookie"))
	}
	both := []*http.Cookie{{Name: securePasskeyCookieName, Value: secure.Value}, {Name: passkeyCookieName, Value: plain.Value}}

	// A begin with both replaces both.
	resp, out = f.raw("POST", passkeyLoginBeginPath, nil, nil, both...)
	if resp.StatusCode != 200 {
		t.Fatalf("begin with both: %d %v", resp.StatusCode, out)
	}
	if n := f.openLogins(); n != 1 {
		t.Fatalf("%d open logins after a begin with both cookies, want 1", n)
	}

	// A password login with both drops both.
	resp, _ = f.raw("POST", passkeyLoginBeginPath, nil, nil)
	again := setCookie(resp, passkeyCookieName)
	both = []*http.Cookie{{Name: securePasskeyCookieName, Value: out["token"].(string)}, {Name: passkeyCookieName, Value: again.Value}}
	resp, _ = f.raw("POST", "/api/session", map[string]string{"username": "alice", "password": "correct horse battery"}, nil, both...)
	if resp.StatusCode != 200 || !expired(resp, passkeyCookieName) || !expired(resp, securePasskeyCookieName) {
		t.Fatalf("password login: %d %v", resp.StatusCode, resp.Header.Values("Set-Cookie"))
	}
	if n := f.openLogins(); n != 0 {
		t.Fatalf("%d open logins after a password login with both cookies, want 0", n)
	}
	// Over HTTPS both names are expired too.
	resp, _ = f.raw("POST", passkeyLoginBeginPath, nil, https)
	c := setCookie(resp, securePasskeyCookieName)
	resp, _ = f.raw("POST", "/api/session", map[string]string{"username": "alice", "password": "correct horse battery"}, https, c)
	if resp.StatusCode != 200 || !expired(resp, passkeyCookieName) || !expired(resp, securePasskeyCookieName) {
		t.Fatalf("password login over HTTPS: %d %v", resp.StatusCode, resp.Header.Values("Set-Cookie"))
	}
}

// One network cannot hold more than its share of open passkey logins,
// though each of its clients stays within its own.
func TestPasskeyLoginCapPerNetwork(t *testing.T) {
	f := newAuthFixtureOpts(t, Options{PublicURL: testPublicURL, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}).noCookies()
	per := store.MaxOpenLoginCeremoniesPerClient
	clients := store.MaxOpenLoginCeremoniesPerNetMid / per
	fill := func(client func(i int) string) {
		t.Helper()
		for i := range clients {
			for range per {
				if resp, out := f.doFrom(client(i), "POST", passkeyLoginBeginPath, nil); resp.StatusCode != 200 {
					t.Fatalf("%s: %d %v", client(i), resp.StatusCode, out)
				}
			}
		}
	}
	over := func(client string) {
		t.Helper()
		resp, out := f.doFrom(client, "POST", passkeyLoginBeginPath, nil)
		if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" || out["error"] != errTooManyOpenPasskey {
			t.Fatalf("%s over its network's share: %d %v", client, resp.StatusCode, out)
		}
	}
	// Five /64s of one /56, then a sixth.
	fill(func(i int) string { return fmt.Sprintf("2001:db8:0:%x::1", i) })
	over(fmt.Sprintf("2001:db8:0:%x::1", clients))
	if resp, _ := f.doFrom("2001:db8:0:100::1", "POST", passkeyLoginBeginPath, nil); resp.StatusCode != 200 {
		t.Fatalf("another /56: %d", resp.StatusCode)
	}
	// Five IPv4 addresses of one /24, then a sixth.
	fill(func(i int) string { return fmt.Sprintf("198.51.100.%d", i+1) })
	over("198.51.100.99")
	if resp, _ := f.doFrom("203.0.113.1", "POST", passkeyLoginBeginPath, nil); resp.StatusCode != 200 {
		t.Fatalf("another /24: %d", resp.StatusCode)
	}
}

// The limits group IPv6 by /64 with its /56 and /48 as networks, and IPv4
// by address with its /24.
func TestLimitKeys(t *testing.T) {
	for in, want := range map[string]store.ClientKeys{
		"192.0.2.77":              {Addr: "192.0.2.77", NetMid: "192.0.2.0/24"},
		"::ffff:192.0.2.77":       {Addr: "192.0.2.77", NetMid: "192.0.2.0/24"},
		"2001:db8:1:2345::5":      {Addr: "2001:db8:1:2345::/64", NetMid: "2001:db8:1:2300::/56", NetWide: "2001:db8:1::/48"},
		"2001:db8:1:23ff:ffff::1": {Addr: "2001:db8:1:23ff::/64", NetMid: "2001:db8:1:2300::/56", NetWide: "2001:db8:1::/48"},
		"fe80::1%eth0":            {Addr: "fe80::/64", NetMid: "fe80::/56", NetWide: "fe80::/48"},
	} {
		if got := limitKeys(netip.MustParseAddr(in)); got != want {
			t.Errorf("%s: %+v, want %+v", in, got, want)
		}
	}
	s := &Server{trustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}
	r, _ := http.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:1234"
	r.Header.Set("X-Forwarded-For", "2001:db8:aa:bbcc::1")
	if got, want := s.clientKeys(r), (store.ClientKeys{Addr: "2001:db8:aa:bbcc::/64", NetMid: "2001:db8:aa:bb00::/56", NetWide: "2001:db8:aa::/48"}); got != want {
		t.Errorf("behind a proxy: %+v, want %+v", got, want)
	}
	r.RemoteAddr = "pipe"
	if got := s.clientKeys(r); got != (store.ClientKeys{Addr: "pipe"}) {
		t.Errorf("no address: %+v", got)
	}
}

// An X-Forwarded-For the server ignores is logged once per server, with
// the peer only; from a trusted proxy or without the header, never.
func TestWarnIgnoredForwardedFor(t *testing.T) {
	const msg = "X-Forwarded-For from a peer that is not a trusted proxy is ignored"
	run := func(trusted []netip.Prefix, remote string, xff ...string) (warnings int, logged string) {
		t.Helper()
		var buf bytes.Buffer
		h := New(nil, slog.New(slog.NewTextHandler(&buf, nil)), Options{Pinger: fakeDB{}, TrustedProxies: trusted}).Handler()
		for range 2 {
			r, _ := http.NewRequest("GET", "http://localhost/healthz", nil)
			r.RemoteAddr = remote
			for _, v := range xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if rec := serve(h, r); rec.Code != 200 {
				t.Fatalf("status %d", rec.Code)
			}
		}
		return strings.Count(buf.String(), msg), buf.String()
	}
	n, logged := run(nil, "192.0.2.1:1234", "198.51.100.7\nlevel=ERROR fake")
	if n != 1 || !strings.Contains(logged, "peer=192.0.2.1") || !strings.Contains(logged, "PAPERLESS_GO_TRUSTED_PROXIES") ||
		strings.Contains(logged, "198.51.100.7") || strings.Contains(logged, "fake") {
		t.Fatalf("untrusted peer: %d warnings: %s", n, logged)
	}
	// Loopback, as from `tailscale serve`, is not trusted for it either.
	if n, logged := run(nil, "[::1]:1234", "198.51.100.7"); n != 1 || !strings.Contains(logged, "peer=::1") {
		t.Fatalf("loopback: %d warnings: %s", n, logged)
	}
	if n, logged := run([]netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, "192.0.2.1:1234", "198.51.100.7"); n != 0 {
		t.Fatalf("trusted proxy: %s", logged)
	}
	if n, logged := run(nil, "192.0.2.1:1234"); n != 0 {
		t.Fatalf("without the header: %s", logged)
	}
}
