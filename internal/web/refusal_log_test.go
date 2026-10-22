package web

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// newLoggedFixture is newAuthFixtureOpts with a server that logs into the
// returned buffer, as JSON without timestamps (so a planted secret cannot
// match one by chance).
func newLoggedFixture(t *testing.T, opts Options) (*authFixture, *syncWriter) {
	t.Helper()
	f := newAuthFixtureOpts(t, opts)
	logs := &syncWriter{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
	s := New(f.st, log, Options{
		AllowedHosts: []string{"127.0.0.1"}, Hasher: storetest.Hasher, Now: f.clock.Now, TrustedProxies: opts.TrustedProxies,
		Sealer: f.sealer, SecretKey: f.secretKey, Require2FA: opts.Require2FA, PublicURL: opts.PublicURL,
		Blobs: f.blobs, MaxUploadSize: opts.MaxUploadSize,
	})
	f.handler = s.Handler()
	f.srv.Config.Handler = f.handler
	return f, logs
}

// refusal is one "login refused" line.
type refusal struct {
	Method string `json:"method"`
	Reason string `json:"reason"`
	Addr   string `json:"addr"`
	User   string `json:"user"`
}

// refusedLines returns the "login refused" lines logged so far.
func refusedLines(t *testing.T, logs *syncWriter) []refusal {
	t.Helper()
	var out []refusal
	sc := bufio.NewScanner(strings.NewReader(logs.String()))
	for sc.Scan() {
		var line struct {
			Level string `json:"level"`
			Msg   string `json:"msg"`
			refusal
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("log line %q: %v", sc.Text(), err)
		}
		if line.Msg != "login refused" {
			continue
		}
		if line.Level != "INFO" || line.Addr != "127.0.0.1" {
			t.Errorf("login refused line %s", sc.Text())
		}
		out = append(out, line.refusal)
	}
	return out
}

// expectRefusals checks the lines logged since the first n.
func expectRefusals(t *testing.T, logs *syncWriter, n int, want ...refusal) int {
	t.Helper()
	got := refusedLines(t, logs)[n:]
	for i := range want {
		want[i].Addr = "127.0.0.1"
	}
	if len(got) != len(want) {
		t.Fatalf("login refused lines %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	return n + len(got)
}

// countRefusals counts the lines with method and reason.
func countRefusals(t *testing.T, logs *syncWriter, method, reason string) int {
	t.Helper()
	n := 0
	for _, r := range refusedLines(t, logs) {
		if r.Method == method && r.Reason == reason {
			n++
		}
	}
	return n
}

// notInLog fails if any of secrets appears in the log.
func notInLog(t *testing.T, logs *syncWriter, secrets ...string) {
	t.Helper()
	l := logs.String()
	for _, s := range secrets {
		if strings.Contains(l, s) {
			t.Errorf("%q is in the log", s)
		}
	}
	if strings.Contains(l, "login failed") {
		t.Error("an old login failed line is in the log")
	}
}

func TestLoginRefusedLogPassword(t *testing.T) {
	f, logs := newLoggedFixture(t, Options{})
	u := f.addUserWithout2FA("alice", "correct horse battery", false)
	const planted = "planted-Secret-pw-42"

	f.expect("POST", "/api/session", map[string]string{"username": "alice", "password": planted}, 401)
	n := expectRefusals(t, logs, 0, refusal{Method: "password", Reason: reasonWrongPassword, User: "alice"})
	f.expect("POST", "/api/session", map[string]string{"username": "nobody", "password": planted}, 401)
	n = expectRefusals(t, logs, n, refusal{Method: "password", Reason: reasonUnknownUser, User: "nobody"})
	// A password typed into the name field is not logged.
	f.expect("POST", "/api/session", map[string]string{"username": planted + " x", "password": planted}, 401)
	n = expectRefusals(t, logs, n, refusal{Method: "password", Reason: reasonUnknownUser, User: "(invalid name)"})
	if err := f.st.SetUserLocked(context.Background(), u.ID, true); err != nil {
		t.Fatal(err)
	}
	f.expect("POST", "/api/session", map[string]string{"username": "alice", "password": "correct horse battery"}, 403)
	expectRefusals(t, logs, n, refusal{Method: "password", Reason: reasonLocked, User: "alice"})
	notInLog(t, logs, planted, "correct horse battery")
}

// A blocked name or address logs one rate_limited line per block, however
// many attempts follow.
func TestLoginRefusedLogPasswordRateLimited(t *testing.T) {
	f, logs := newLoggedFixture(t, Options{})
	f.addUserWithout2FA("alice", "correct horse battery", false)
	for range 5 {
		f.expect("POST", "/api/session", map[string]string{"username": "alice", "password": "wrong password!"}, 401)
	}
	n := len(refusedLines(t, logs))
	for range 100 {
		f.expect("POST", "/api/session", map[string]string{"username": "alice", "password": "wrong password!"}, 429)
	}
	expectRefusals(t, logs, n, refusal{Method: "password", Reason: reasonRateLimited, User: "alice"})

	// The next block logs again.
	f.clock.Advance(time.Minute)
	f.expect("POST", "/api/session", map[string]string{"username": "alice", "password": "wrong password!"}, 401)
	for range 10 {
		f.expect("POST", "/api/session", map[string]string{"username": "alice", "password": "wrong password!"}, 429)
	}
	if got := countRefusals(t, logs, "password", reasonRateLimited); got != 2 {
		t.Fatalf("%d rate_limited lines after two blocks, want 2", got)
	}
}

func TestLoginRefusedLogSecondFactor(t *testing.T) {
	f, logs := newLoggedFixture(t, Options{})
	u := f.addUserWithout2FA("alice", "correct horse battery", false)
	secret, _ := f.enableTOTP(u)
	// The code of the next time step: a valid-looking code that is wrong
	// now, so it is a planted secret too.
	f.clock.Advance(-10 * time.Minute)
	wrong := f.code(secret)
	f.clock.Advance(10 * time.Minute)

	challenge := f.passwordStep("alice", "correct horse battery")
	f.secondStep(challenge, wrong, 401)
	n := expectRefusals(t, logs, 0, refusal{Method: "2fa", Reason: reasonSecondFactor, User: "alice"})
	// A wrong code leaves the challenge; the 5th blocks the name.
	for range 4 {
		f.secondStep(challenge, wrong, 401)
	}
	n += 4
	for range 100 {
		f.secondStep(challenge, wrong, 429)
	}
	expectRefusals(t, logs, n, refusal{Method: "2fa", Reason: reasonRateLimited, User: "alice"})
	notInLog(t, logs, wrong, secret, "correct horse battery")
}

func TestLoginRefusedLogPasskey(t *testing.T) {
	f, logs := newLoggedFixture(t, Options{PublicURL: testPublicURL})
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	k := newSoftKey(t)
	f.addPasskey(k, "Phone", "correct horse battery", "")
	anon := f.as()
	n := len(refusedLines(t, logs))

	stranger := newSoftKey(t)
	stranger.handle = []byte("not a handle")
	anon.passkeyLogin(stranger, 401)
	n = expectRefusals(t, logs, n, refusal{Method: "passkey", Reason: reasonUnknownPasskey})

	bad := *k
	bad.origin = "https://evil.example.test"
	anon.passkeyLogin(&bad, 401)
	n = expectRefusals(t, logs, n, refusal{Method: "passkey", Reason: reasonPasskeyInvalid})

	// 20 failures block the address; the begins it refuses log once.
	for range 18 {
		anon.passkeyLogin(stranger, 401)
	}
	n += 18
	for range 100 {
		anon.expect("POST", "/api/session/passkey/begin", nil, 429)
	}
	expectRefusals(t, logs, n, refusal{Method: "passkey", Reason: reasonRateLimited})
}

// The begin rate and the caps on open logins log once per window too.
func TestLoginRefusedLogPasskeyBegin(t *testing.T) {
	f, logs := newLoggedFixture(t, Options{PublicURL: testPublicURL})
	f = f.noCookies()
	for range passkeyBeginLimit + 100 {
		f.do("POST", "/api/session/passkey/begin", nil)
	}
	// One line for the open-login cap, one for the begin rate.
	expectRefusals(t, logs, 0, refusal{Method: "passkey", Reason: reasonRateLimited}, refusal{Method: "passkey", Reason: reasonRateLimited})
	if n := strings.Count(logs.String(), `"msg":"too many passkey logins in progress"`); n != 1 {
		t.Errorf("open-login cap warnings: %d", n)
	}
}

func TestLoginRefusedLogAPIKey(t *testing.T) {
	f, logs := newLoggedFixture(t, Options{})
	f.addUser("kim", keyPassword, false)
	f.loginOK("kim", keyPassword)
	revoked := f.createKey("old", []string{"read"}, "")
	f.expect("DELETE", "/api/profile/api-keys/"+itoa(f.keyID("old")), nil, 204)
	out := f.expect("POST", "/api/profile/api-keys", map[string]any{"name": "expiring", "scopes": []string{"read"}, "expiresInDays": 1, "currentPassword": keyPassword}, 201)
	expiring := out["key"].(string)
	unknown := "pl_" + strings.Repeat("c", 52)
	n := len(refusedLines(t, logs))

	if s := f.keyStatus(unknown, "GET", "/api/documents"); s != 401 {
		t.Fatalf("unknown key: %d", s)
	}
	n = expectRefusals(t, logs, n, refusal{Method: "api_key", Reason: reasonInvalid})
	if s := f.keyStatus(revoked, "GET", "/api/documents"); s != 401 {
		t.Fatalf("revoked key: %d", s)
	}
	n = expectRefusals(t, logs, n, refusal{Method: "api_key", Reason: "revoked", User: "kim"})
	f.clock.Advance(24*time.Hour + time.Second)
	if s := f.keyStatus(expiring, "GET", "/api/documents"); s != 401 {
		t.Fatalf("expired key: %d", s)
	}
	expectRefusals(t, logs, n, refusal{Method: "api_key", Reason: "expired", User: "kim"})
	notInLog(t, logs, unknown, revoked, expiring, keyPassword)
}

// TestAPIKeyUnknownAuditCap: an unknown key writes one audit entry per
// client address and window, a key that exists one per refusal; the rate
// counts both, and a blocked client logs one rate_limited line.
func TestAPIKeyUnknownAuditCap(t *testing.T) {
	f, logs := newLoggedFixture(t, Options{})
	f.addUser("kim", keyPassword, false)
	f.loginOK("kim", keyPassword)
	revoked := f.createKey("old", []string{"read"}, "")
	f.expect("DELETE", "/api/profile/api-keys/"+itoa(f.keyID("old")), nil, 204)
	unknown := func(i int) string { return "pl_" + strings.Repeat(string(rune('a'+i%26)), 52) }
	refused := func(key string) []string {
		t.Helper()
		var keys []string
		for _, r := range auditLog(t, f.st) {
			if r.Action != "auth.login_failed" {
				continue
			}
			keys = append(keys, details(t, r)["reason"].(string))
			for _, k := range []string{revoked, unknown(0), unknown(1)} {
				if strings.Contains(string(r.Details), k) || strings.Contains(r.TargetName, k) {
					t.Error("an API key is in the audit log")
				}
			}
		}
		return keys
	}

	// Three refusals of a revoked key: three entries.
	for range 3 {
		if s := f.keyStatus(revoked, "GET", "/api/documents"); s != 401 {
			t.Fatalf("revoked key: %d", s)
		}
	}
	if got := refused(""); len(got) != 3 || got[0] != "revoked" || got[2] != "revoked" {
		t.Fatalf("revoked entries %v", got)
	}

	// Mixed: the revoked key came first, the first unknown key still gets
	// its entry, the second not.
	f.keyStatus(unknown(0), "GET", "/api/documents")
	f.keyStatus(unknown(1), "GET", "/api/documents")
	f.keyStatus(revoked, "GET", "/api/documents")
	if got := refused(""); len(got) != 5 || got[3] != reasonInvalid || got[4] != "revoked" {
		t.Fatalf("mixed entries %v", got)
	}

	// A new window: 31 unknown keys give one entry, 30 invalid lines and,
	// for the 31st and every later request, one rate_limited line.
	f.clock.Advance(apiKeyWindow)
	before := len(refused(""))
	invalidBefore := countRefusals(t, logs, "api_key", reasonInvalid)
	for i := range badAPIKeyLimit {
		if s := f.keyStatus(unknown(i), "GET", "/api/documents"); s != http.StatusUnauthorized {
			t.Fatalf("unknown key %d: %d", i+1, s)
		}
	}
	for range 100 {
		if s := f.keyStatus(unknown(badAPIKeyLimit), "GET", "/api/documents"); s != http.StatusTooManyRequests {
			t.Fatalf("over the rate: %d", s)
		}
	}
	if got := refused(""); len(got) != before+1 || got[before] != reasonInvalid {
		t.Fatalf("entries in the new window %v, want one more invalid", got[before:])
	}
	if got := countRefusals(t, logs, "api_key", reasonInvalid) - invalidBefore; got != badAPIKeyLimit {
		t.Errorf("%d invalid lines, want %d", got, badAPIKeyLimit)
	}
	if got := countRefusals(t, logs, "api_key", reasonRateLimited); got != 1 {
		t.Errorf("%d rate_limited lines, want 1", got)
	}
	secrets := []string{revoked, keyPassword}
	for i := range badAPIKeyLimit + 1 {
		secrets = append(secrets, unknown(i))
	}
	notInLog(t, logs, secrets...)
}
