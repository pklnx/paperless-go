package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pklnx/paperless-go/internal/auth"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// restrictedPath is session-only and answers 200 to every user who may do
// anything: a session restricted to the 2FA setup gets 403 there.
const restrictedPath = "/api/audit"

// passwordStep logs in with the password and returns the 2FA challenge.
func (f *authFixture) passwordStep(name, password string) string {
	f.t.Helper()
	resp, out := f.do("POST", "/api/session", map[string]string{"username": name, "password": password})
	challenge, _ := out["challenge"].(string)
	if resp.StatusCode != 200 || out["twoFactorRequired"] != true || challenge == "" {
		f.t.Fatalf("password step: %d %v", resp.StatusCode, out)
	}
	return challenge
}

func (f *authFixture) secondStep(challenge, code string, want int) map[string]any {
	f.t.Helper()
	return f.expect("POST", "/api/session/2fa", map[string]string{"challenge": challenge, "code": code}, want)
}

// setUpTOTP sets up 2FA through the profile endpoints and returns the
// secret and the recovery codes.
func (f *authFixture) setUpTOTP() (string, []string) {
	f.t.Helper()
	setup := f.expect("POST", "/api/profile/2fa/setup", nil, 200)
	secret, _ := setup["secret"].(string)
	qr, _ := setup["qrDataUrl"].(string)
	uri, _ := setup["otpauthUri"].(string)
	if secret == "" || !strings.HasPrefix(qr, "data:image/png;base64,") || !strings.HasPrefix(uri, "otpauth://totp/paperless-go:") {
		f.t.Fatalf("setup: %v", setup)
	}
	out := f.expect("POST", "/api/profile/2fa/confirm", map[string]string{"code": f.code(secret)}, 200)
	raw, _ := out["recoveryCodes"].([]any)
	if len(raw) != auth.RecoveryCodeCount {
		f.t.Fatalf("recovery codes: %v", out)
	}
	codes := make([]string, len(raw))
	for i, c := range raw {
		codes[i], _ = c.(string)
	}
	return secret, codes
}

func TestAdminMustSetUpTOTP(t *testing.T) {
	f := newAuthFixture(t)
	f.addUserWithout2FA("admin", "correct horse battery", true)
	// Without 2FA the admin gets a session that only allows the setup.
	f.loginOK("admin", "correct horse battery")
	if out := f.expect("GET", "/api/session", nil, 200); out["user"].(map[string]any)["twoFactorRequired"] != true ||
		out["user"].(map[string]any)["twoFactorEnabled"] != false {
		t.Fatalf("session: %v", out)
	}
	if out := f.expect("GET", restrictedPath, nil, 403); out["twoFactorSetupRequired"] != true {
		t.Fatalf("restricted: %v", out)
	}
	f.expect("GET", "/api/users", nil, 403)
	if out := f.expect("GET", "/api/profile/2fa", nil, 200); out["required"] != true || out["enabled"] != false {
		t.Fatalf("2fa state: %v", out)
	}
	setup := f.expect("POST", "/api/profile/2fa/setup", nil, 200)
	secret, _ := setup["secret"].(string)
	f.expect("POST", "/api/profile/2fa/confirm", map[string]string{"code": "000000"}, 401)
	code := f.code(secret)
	out := f.expect("POST", "/api/profile/2fa/confirm", map[string]string{"code": code}, 200)
	if codes, _ := out["recoveryCodes"].([]any); len(codes) != auth.RecoveryCodeCount {
		t.Fatalf("recovery codes: %v", out)
	}
	f.expect("GET", restrictedPath, nil, 200)
	if out := f.expect("GET", "/api/profile/2fa", nil, 200); out["enabled"] != true || out["recoveryCodesLeft"] != float64(auth.RecoveryCodeCount) {
		t.Fatalf("2fa state after setup: %v", out)
	}
	f.expect("POST", "/api/profile/2fa/setup", nil, 409)
	// Admins cannot turn it off.
	f.expect("DELETE", "/api/profile/2fa", map[string]string{"currentPassword": "correct horse battery", "code": code}, 403)

	// The confirmation code is used up: it does not work for the next login.
	f.expect("DELETE", "/api/session", nil, 204)
	f.secondStep(f.passwordStep("admin", "correct horse battery"), code, 401)
}

// The only admin, created before 2FA existed, still gets in after the
// upgrade and sets 2FA up: the upgrade locks nobody out.
func TestUpgradeKeepsOnlyAdmin(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	f.addUserWithout2FA("admin", "correct horse battery", true)
	if m, err := f.st.MigrateDown(ctx); err != nil || m != "00006_two_factor.sql" {
		t.Fatalf("roll back 2FA: %q, %v", m, err)
	}
	if _, err := f.st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	f.loginOK("admin", "correct horse battery")
	f.expect("GET", restrictedPath, nil, 403)
	secret, _ := f.setUpTOTP()
	f.expect("GET", restrictedPath, nil, 200)
	f.expect("DELETE", "/api/session", nil, 204)
	f.secondStep(f.passwordStep("admin", "correct horse battery"), f.code(secret), 200)
	f.expect("GET", "/api/users", nil, 200)
}

func TestVoluntaryTwoFactorNeedsCodeAtLogin(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	secret, recovery := f.setUpTOTP()
	f.expect("DELETE", "/api/session", nil, 204)

	// The password alone gives no session.
	challenge := f.passwordStep("alice", "correct horse battery")
	f.expect("GET", restrictedPath, nil, 401)
	f.secondStep(challenge, "000000", 401)
	code := f.code(secret)
	if out := f.secondStep(challenge, code, 200); out["user"].(map[string]any)["twoFactorEnabled"] != true {
		t.Fatalf("login: %v", out)
	}
	f.expect("GET", restrictedPath, nil, 200)
	// The challenge is used up.
	f.secondStep(challenge, code, 401)

	// A recovery code works once, also typed in capitals without dashes.
	f.expect("DELETE", "/api/session", nil, 204)
	typed := strings.ToUpper(strings.ReplaceAll(recovery[0], "-", ""))
	f.secondStep(f.passwordStep("alice", "correct horse battery"), typed, 200)
	if out := f.expect("GET", "/api/profile/2fa", nil, 200); out["recoveryCodesLeft"] != float64(auth.RecoveryCodeCount-1) {
		t.Fatalf("codes left: %v", out)
	}
	f.expect("DELETE", "/api/session", nil, 204)
	f.secondStep(f.passwordStep("alice", "correct horse battery"), recovery[0], 401)
}

func TestTOTPCodeReplay(t *testing.T) {
	f := newAuthFixture(t)
	u := f.addUser("alice", "correct horse battery", false)
	secret, _ := f.enableTOTP(u)
	code := f.code(secret)
	f.as().secondStep(f.passwordStep("alice", "correct horse battery"), code, 200)
	// The same code again, still within its window, is refused.
	other := f.as()
	other.secondStep(other.passwordStep("alice", "correct horse battery"), code, 401)
	// A code of an earlier step than the last accepted one, too.
	earlier, err := auth.GenerateTOTP(secret, f.clock.Now().Add(-auth.TOTPPeriod))
	if err != nil {
		t.Fatal(err)
	}
	other.secondStep(other.passwordStep("alice", "correct horse battery"), earlier, 401)
}

func TestTwoFactorChallengeExpires(t *testing.T) {
	f := newAuthFixture(t)
	u := f.addUser("alice", "correct horse battery", false)
	secret, _ := f.enableTOTP(u)
	challenge := f.passwordStep("alice", "correct horse battery")
	f.clock.Advance(twoFactorChallengeTTL)
	code, err := auth.GenerateTOTP(secret, f.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if out := f.secondStep(challenge, code, 401); out["error"] != errLoginExpired {
		t.Fatalf("expired challenge: %v", out)
	}
	f.secondStep("not a challenge", code, 401)
	// Housekeeping removes it.
	n, err := f.st.DeleteExpiredTwoFactorChallenges(context.Background(), f.clock.Now())
	if err != nil || n != 1 {
		t.Fatalf("delete expired: %d, %v", n, err)
	}
}

func TestSecondFactorRateLimit(t *testing.T) {
	f := newAuthFixture(t)
	u := f.addUser("alice", "correct horse battery", false)
	f.enableTOTP(u)
	// A correct password does not reset the counter of wrong codes.
	for range 5 {
		f.secondStep(f.passwordStep("alice", "correct horse battery"), "000000", 401)
	}
	resp, _ := f.do("POST", "/api/session", map[string]string{"username": "alice", "password": "correct horse battery"})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("password after 5 wrong codes: %d", resp.StatusCode)
	}
}

func TestResetDuringLoginWins(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("admin", "correct horse battery", true)
	u := f.addUser("alice", "correct horse battery", false)
	secret, _ := f.enableTOTP(u)

	alice := f.as()
	alice.loginOK("alice", "correct horse battery")
	challenge := f.as().passwordStep("alice", "correct horse battery")

	f.loginOK("admin", "correct horse battery")
	if on := f.listedTwoFactor(); !on["alice"] || !on["admin"] {
		t.Fatalf("2FA in user list before reset: %v", on)
	}
	f.expect("POST", "/api/users/alice/2fa/reset", nil, 204)
	f.expect("POST", "/api/users/admin/2fa/reset", nil, 403) // not your own
	f.expect("POST", "/api/users/nobody/2fa/reset", nil, 404)
	if on := f.listedTwoFactor(); on["alice"] || !on["admin"] {
		t.Fatalf("2FA in user list after reset: %v", on)
	}

	// The pending login and the existing session are both gone.
	f.secondStep(challenge, f.code(secret), 401)
	alice.expect("GET", restrictedPath, nil, 401)
	// Alice logs in with the password alone again (2FA is optional for her)
	// and cannot reset anyone.
	delete(f.secrets, "alice")
	alice.loginOK("alice", "correct horse battery")
	alice.expect("GET", restrictedPath, nil, 200)
	alice.expect("POST", "/api/users/admin/2fa/reset", nil, 403)
}

func TestRecoveryCodeUsedOnceConcurrently(t *testing.T) {
	f := newAuthFixture(t)
	u := f.addUser("alice", "correct horse battery", false)
	_, recovery := f.enableTOTP(u)
	const tries = 6
	challenges := make([]string, tries)
	for i := range challenges {
		challenges[i] = f.as().passwordStep("alice", "correct horse battery")
	}
	codes := make([]int, tries)
	var wg sync.WaitGroup
	for i := range challenges {
		wg.Go(func() {
			resp, _ := f.as().do("POST", "/api/session/2fa", map[string]string{"challenge": challenges[i], "code": recovery[1]})
			codes[i] = resp.StatusCode
		})
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == 200 {
			ok++
		} else if c != 401 {
			t.Fatalf("status %d", c)
		}
	}
	if ok != 1 {
		t.Fatalf("%d logins with one recovery code (statuses %v)", ok, codes)
	}
}

func TestDisableAndRegenerate(t *testing.T) {
	f := newAuthFixture(t)
	u := f.addUser("alice", "correct horse battery", false)
	secret, recovery := f.enableTOTP(u)
	f.secondStep(f.passwordStep("alice", "correct horse battery"), f.code(secret), 200)
	other := f.as()
	other.secondStep(other.passwordStep("alice", "correct horse battery"), f.code(secret), 200)

	// New recovery codes need a current code; the old ones stop working.
	f.expect("POST", "/api/profile/2fa/recovery-codes", map[string]string{"code": "000000"}, 401)
	out := f.expect("POST", "/api/profile/2fa/recovery-codes", map[string]string{"code": f.code(secret)}, 200)
	fresh, _ := out["recoveryCodes"].([]any)
	if len(fresh) != auth.RecoveryCodeCount {
		t.Fatalf("new codes: %v", out)
	}
	third := f.as()
	third.secondStep(third.passwordStep("alice", "correct horse battery"), recovery[2], 401)

	// Turning it off needs the password and a code.
	f.expect("DELETE", "/api/profile/2fa", map[string]string{"currentPassword": "wrong wrong wrong", "code": f.code(secret)}, 401)
	f.expect("DELETE", "/api/profile/2fa", map[string]string{"currentPassword": "correct horse battery", "code": "000000"}, 401)
	if st, err := f.st.GetTwoFactorState(context.Background(), u.ID); err != nil || !st.Enabled {
		t.Fatalf("2FA after a wrong code: %+v, %v", st, err)
	}
	f.expect("DELETE", "/api/profile/2fa", map[string]string{"currentPassword": "correct horse battery", "code": fresh[0].(string)}, 204)
	f.expect("GET", restrictedPath, nil, 200)
	// The other session ended.
	other.expect("GET", restrictedPath, nil, 401)
	f.expect("DELETE", "/api/session", nil, 204)
	delete(f.secrets, "alice")
	f.loginOK("alice", "correct horse battery") // password only again
}

func TestRequire2FAForEveryone(t *testing.T) {
	f := newAuthFixtureOpts(t, Options{Require2FA: true})
	f.addUser("alice", "correct horse battery", false)
	f.loginOK("alice", "correct horse battery")
	if out := f.expect("GET", restrictedPath, nil, 403); out["twoFactorSetupRequired"] != true {
		t.Fatalf("restricted: %v", out)
	}
	secret, _ := f.setUpTOTP()
	f.expect("DELETE", "/api/session", nil, 204)
	code := f.code(secret)
	f.secondStep(f.passwordStep("alice", "correct horse battery"), code, 200)
	f.expect("GET", restrictedPath, nil, 200)
	f.expect("DELETE", "/api/profile/2fa", map[string]string{"currentPassword": "correct horse battery", "code": f.code(secret)}, 403)
}

func TestGeneratedPasswordThenTOTP(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("admin", "correct horse battery", true)
	f.loginOK("admin", "correct horse battery")
	out := f.expect("POST", "/api/users", map[string]any{"name": "boss", "admin": true}, 201)
	pw := out["password"].(string)

	boss := f.as()
	boss.loginOK("boss", pw)
	// First the password, then 2FA.
	boss.expect("POST", "/api/profile/2fa/setup", nil, 403)
	boss.expect("PUT", "/api/profile/password", map[string]string{"current": pw, "new": "the boss passphrase"}, 204)
	if out := boss.expect("GET", restrictedPath, nil, 403); out["twoFactorSetupRequired"] != true {
		t.Fatalf("after the password: %v", out)
	}
	boss.expect("POST", "/api/profile/2fa/setup", nil, 200)
}

func TestTwoFactorWithoutSecretKey(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct horse battery", false)
	// The same database, served without a key: every 2FA endpoint answers
	// 503 instead of failing later.
	s := New(f.st, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{AllowedHosts: []string{"127.0.0.1"}, Hasher: storetest.Hasher, Now: f.clock.Now})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	g := f.as()
	g.srv = srv
	g.loginOK("alice", "correct horse battery")
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/profile/2fa/setup"}, {"POST", "/api/profile/2fa/confirm"},
		{"DELETE", "/api/profile/2fa"}, {"POST", "/api/profile/2fa/recovery-codes"}, {"POST", "/api/session/2fa"},
	} {
		g.expect(c.method, c.path, map[string]string{}, 503)
	}
}

// TestAuditTwoFactor checks the audit entries of every 2FA event: each
// in the same transaction as its change, with the factor used.
func TestAuditTwoFactor(t *testing.T) {
	f := newAuthFixture(t)
	admin := f.addUser("root", "correct horse battery", true)
	alice := f.addUser("alice", "correct horse battery", false)
	aliceID := strconv.FormatInt(alice.ID, 10)
	one := func(name, action string, user *store.User, detail map[string]any, do func()) {
		t.Helper()
		n := len(auditLog(t, f.st))
		do()
		got := newEntries(t, f.st, n)
		if len(got) != 1 {
			t.Fatalf("%s: %d entries: %+v", name, len(got), got)
		}
		e := got[0]
		if e.Action != action || e.UserID == nil || *e.UserID != user.ID {
			t.Errorf("%s: %+v", name, e)
		}
		d := details(t, e)
		for k, v := range detail {
			if d[k] != v {
				t.Errorf("%s: %s = %v, want %v (%v)", name, k, d[k], v, d)
			}
		}
		if strings.Contains(string(e.Details), "secret") {
			t.Errorf("%s: secret in details: %s", name, e.Details)
		}
	}
	alicePw := "correct horse battery"
	f.loginOK("alice", alicePw)
	var secret string
	var recovery []string
	one("wrong code to confirm", "profile.2fa_failed", alice,
		map[string]any{"attempted": "profile.2fa_enable", "reason": "second_factor"}, func() {
			f.expect("POST", "/api/profile/2fa/setup", nil, 200)
			f.expect("POST", "/api/profile/2fa/confirm", map[string]string{"code": "000000"}, 401)
		})
	one("enable", "profile.2fa_enable", alice, nil, func() { secret, recovery = f.setUpTOTP() })
	f.expect("DELETE", "/api/session", nil, 204)

	one("login with TOTP", "auth.login", alice, map[string]any{"method": "2fa", "factor": "totp"}, func() {
		f.secondStep(f.passwordStep("alice", alicePw), f.code(secret), 200)
	})
	f.expect("DELETE", "/api/session", nil, 204)
	one("wrong code", "auth.login_failed", alice, map[string]any{"method": "2fa", "reason": "second_factor"}, func() {
		f.secondStep(f.passwordStep("alice", alicePw), "000000", 401)
	})
	one("login with a recovery code", "auth.login", alice,
		map[string]any{"method": "2fa", "factor": "recovery_code", "recoveryCodesLeft": float64(auth.RecoveryCodeCount - 1)}, func() {
			f.secondStep(f.passwordStep("alice", alicePw), recovery[0], 200)
		})
	// Refused codes and passwords in the profile are recorded too.
	one("wrong code for new recovery codes", "profile.2fa_failed", alice,
		map[string]any{"attempted": "profile.recovery_codes", "reason": "second_factor"}, func() {
			f.expect("POST", "/api/profile/2fa/recovery-codes", map[string]string{"code": "000000"}, 401)
		})
	one("wrong password to disable", "profile.2fa_failed", alice,
		map[string]any{"attempted": "profile.2fa_disable", "reason": "wrong_password"}, func() {
			f.expect("DELETE", "/api/profile/2fa", map[string]string{"currentPassword": "wrong horse battery", "code": f.code(secret)}, 401)
		})
	one("wrong code to disable", "profile.2fa_failed", alice,
		map[string]any{"attempted": "profile.2fa_disable", "reason": "second_factor"}, func() {
			f.expect("DELETE", "/api/profile/2fa", map[string]string{"currentPassword": alicePw, "code": "000000"}, 401)
		})
	var fresh []any
	one("new recovery codes", "profile.recovery_codes", alice, map[string]any{"factor": "totp"}, func() {
		out := f.expect("POST", "/api/profile/2fa/recovery-codes", map[string]string{"code": f.code(secret)}, 200)
		fresh, _ = out["recoveryCodes"].([]any)
	})
	one("disable with a recovery code", "profile.2fa_disable", alice, map[string]any{"factor": "recovery_code"}, func() {
		f.expect("DELETE", "/api/profile/2fa", map[string]string{"currentPassword": alicePw, "code": fresh[3].(string)}, 204)
	})
	f.expect("DELETE", "/api/session", nil, 204)

	// An admin resets it.
	f.enableTOTP(alice)
	f.loginOK("root", "correct horse battery")
	one("reset", "user.2fa_reset", admin, nil, func() { f.expect("POST", "/api/users/alice/2fa/reset", nil, 204) })
	if e := auditLog(t, f.st); e[len(e)-1].TargetType != store.TargetUser || e[len(e)-1].TargetID != aliceID {
		t.Errorf("reset target: %+v", e[len(e)-1])
	}

	// Without the log, nothing changes.
	breakAudit(t, f.dbURL)
	f.enableTOTP(alice)
	f.expect("POST", "/api/users/alice/2fa/reset", nil, 503)
	if st, err := f.st.GetTwoFactorState(context.Background(), alice.ID); err != nil || !st.Enabled {
		t.Fatalf("reset without an entry: %+v, %v", st, err)
	}
}

// listedTwoFactor returns twoFactorEnabled per name from the admin user list.
func (f *authFixture) listedTwoFactor() map[string]bool {
	f.t.Helper()
	users, _ := f.expect("GET", "/api/users", nil, 200)["users"].([]any)
	on := map[string]bool{}
	for _, u := range users {
		m, _ := u.(map[string]any)
		name, _ := m["name"].(string)
		on[name], _ = m["twoFactorEnabled"].(bool)
	}
	return on
}
