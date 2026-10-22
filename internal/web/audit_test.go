package web

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/store"
)

// auditLog returns all audit entries, oldest first. The database clock sets
// their time; a day around now covers any skew to the test's clock.
func auditLog(t *testing.T, st *store.Store) []store.AuditRecord {
	t.Helper()
	var out []store.AuditRecord
	f := store.AuditFilter{Since: time.Now().Add(-24 * time.Hour), Until: time.Now().Add(24 * time.Hour)}
	if err := st.ExportAudit(context.Background(), f, func(r store.AuditRecord) error {
		out = append(out, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func details(t *testing.T, r store.AuditRecord) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(r.Details, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// newEntries returns the entries written since the log had n of them.
func newEntries(t *testing.T, st *store.Store, n int) []store.AuditRecord {
	t.Helper()
	return auditLog(t, st)[n:]
}

// breakAudit makes every audit insert fail: the current month's partition
// is detached and keeps its name, so it cannot be created again.
func breakAudit(t *testing.T, dbURL string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `DO $$ BEGIN
		EXECUTE format('ALTER TABLE audit_log DETACH PARTITION %I',
			'audit_log_' || to_char(now() AT TIME ZONE 'UTC', 'YYYY_MM'));
	END $$`); err != nil {
		t.Fatal(err)
	}
}

func TestAuditLogins(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.addUser("alice", "correct horse battery", false)
	type want struct {
		action string
		user   *store.User
		detail map[string]any
	}
	step := func(name string, do func(), w *want) {
		t.Helper()
		n := len(auditLog(t, f.st))
		do()
		got := newEntries(t, f.st, n)
		if w == nil {
			if len(got) != 0 {
				t.Errorf("%s: logged %+v", name, got)
			}
			return
		}
		if len(got) != 1 {
			t.Fatalf("%s: %d entries: %+v", name, len(got), got)
		}
		e := got[0]
		if e.Action != w.action || e.Actor != store.ActorSession || e.ClientIP != "127.0.0.1" || !strings.HasPrefix(e.UserAgent, "Go-http-client/") {
			t.Errorf("%s: %+v", name, e)
		}
		if w.user != nil && (e.UserID == nil || *e.UserID != w.user.ID || e.UserName != w.user.Name) {
			t.Errorf("%s: user %v %q", name, e.UserID, e.UserName)
		}
		if w.user == nil && e.UserID != nil {
			t.Errorf("%s: user %v", name, *e.UserID)
		}
		d := details(t, e)
		for k, v := range w.detail {
			if d[k] != v {
				t.Errorf("%s: %s = %v, want %v (%v)", name, k, d[k], v, d)
			}
		}
		if _, ok := w.detail["name"]; !ok {
			if _, has := d["name"]; has {
				t.Errorf("%s: name recorded: %v", name, d)
			}
		}
	}
	login := func(name, pw string, status int) func() {
		return func() {
			if resp := f.login(name, pw); resp.StatusCode != status {
				t.Fatalf("login %s: %d", name, resp.StatusCode)
			}
		}
	}
	step("password", login("alice", "correct horse battery", 200),
		&want{"auth.login", alice, map[string]any{"method": "password"}})
	step("logout", func() { f.expect("DELETE", "/api/session", nil, 204) },
		&want{"auth.logout", alice, nil})
	step("logout without session", func() { f.expect("DELETE", "/api/session", nil, 204) }, nil)
	step("wrong password", login("alice", "wrong horse battery", 401),
		&want{"auth.login_failed", alice, map[string]any{"method": "password", "reason": "wrong_password"}})
	step("unknown user", login("nobody", "wrong horse battery", 401),
		&want{"auth.login_failed", nil, map[string]any{"reason": "unknown_user", "name": "nobody"}})
	// A password typed into the name field is no valid name: not kept.
	step("password as name", login("my secret password", "x", 401),
		&want{"auth.login_failed", nil, map[string]any{"reason": "unknown_user"}})
	step("too long", login("carl", strings.Repeat("x", 2000), 401),
		&want{"auth.login_failed", nil, map[string]any{"reason": "wrong_password", "name": "carl"}})

	if err := f.st.SetUserLocked(context.Background(), alice.ID, true); err != nil {
		t.Fatal(err)
	}
	step("locked", login("alice", "correct horse battery", 403),
		&want{"auth.login_failed", alice, map[string]any{"reason": "locked"}})
	// Over the limit: refused without an entry, so a flood cannot grow the log.
	for range 10 {
		if f.login("bob", "wrong horse battery").StatusCode == 429 {
			break
		}
	}
	step("limited", login("bob", "wrong horse battery", 429), nil)
}

// TestAuditChanges runs every change of users and the own password once
// and checks that each writes exactly one entry, by the admin.
func TestAuditChanges(t *testing.T) {
	f := newAuthFixture(t)
	admin := f.addUser("root", "correct horse battery", true)
	alice := f.addUser("alice", "correct horse battery", false)
	f.loginOK("root", "correct horse battery")
	aliceID := strconv.FormatInt(alice.ID, 10)
	var carolID string
	for _, c := range []struct {
		method, path string
		body         any
		status       int
		action       string
		target       *string
		detail       map[string]any
	}{
		{"POST", "/api/users", map[string]any{"name": "carol"}, 201, "user.add", nil, map[string]any{"admin": false}},
		{"PATCH", "/api/users/alice", map[string]any{"locked": true}, 204, "user.lock", &aliceID, nil},
		{"PATCH", "/api/users/alice", map[string]any{"locked": false}, 204, "user.unlock", &aliceID, nil},
		{"PATCH", "/api/users/alice", map[string]any{"admin": true}, 204, "user.promote", &aliceID, nil},
		{"PATCH", "/api/users/alice", map[string]any{"admin": false}, 204, "user.demote", &aliceID, nil},
		{"POST", "/api/users/alice/password", nil, 200, "user.password_reset", &aliceID, nil},
		{"DELETE", "/api/users/carol", nil, 204, "user.remove", &carolID, nil},
		{"PUT", "/api/profile/password", map[string]any{"current": "correct horse battery", "new": "another horse battery"}, 204, "profile.password_change", nil, nil},
	} {
		n := len(auditLog(t, f.st))
		out := f.expect(c.method, c.path, c.body, c.status)
		got := newEntries(t, f.st, n)
		if len(got) != 1 {
			t.Fatalf("%s %s: %d entries", c.method, c.path, len(got))
		}
		e := got[0]
		if e.Action != c.action || e.Actor != store.ActorSession || e.UserID == nil || *e.UserID != admin.ID || e.UserName != "root" {
			t.Errorf("%s %s: %+v", c.method, c.path, e)
		}
		if c.action == "user.add" {
			if e.TargetType != store.TargetUser || e.TargetName != "carol" || e.TargetID == "" {
				t.Errorf("user.add target: %+v", e)
			}
			carolID = e.TargetID
		} else if c.target != nil && (e.TargetType != store.TargetUser || e.TargetID != *c.target) {
			t.Errorf("%s %s: target %q %q", c.method, c.path, e.TargetType, e.TargetID)
		}
		d := details(t, e)
		for k, v := range c.detail {
			if d[k] != v {
				t.Errorf("%s %s: %s = %v, want %v", c.method, c.path, k, d[k], v)
			}
		}
		// Generated passwords never reach the log.
		if pw, _ := out["password"].(string); pw != "" && strings.Contains(string(e.Details), pw) {
			t.Errorf("%s %s: password recorded: %s", c.method, c.path, e.Details)
		}
		if strings.Contains(string(e.Details), "horse") {
			t.Errorf("%s %s: password recorded: %s", c.method, c.path, e.Details)
		}
	}
	// Refused changes write nothing.
	n := len(auditLog(t, f.st))
	f.expect("POST", "/api/users", map[string]any{"name": "root"}, 409)
	f.expect("PATCH", "/api/users/nobody", map[string]any{"locked": true}, 404)
	if got := newEntries(t, f.st, n); len(got) != 0 {
		t.Errorf("refused change logged: %+v", got)
	}
}

// Without the log, a change is refused and not done, a logout still logs
// out, and a login creates no session.
func TestAuditChangesFailClosed(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("root", "correct horse battery", true)
	alice := f.addUser("alice", "correct horse battery", false)
	f.loginOK("root", "correct horse battery")
	breakAudit(t, f.dbURL)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"PATCH", "/api/users/alice", map[string]any{"locked": true}},
		{"PATCH", "/api/users/alice", map[string]any{"admin": true}},
		{"POST", "/api/users/alice/password", nil},
		{"DELETE", "/api/users/alice", nil},
		{"POST", "/api/users", map[string]any{"name": "carol"}},
		{"PUT", "/api/profile/password", map[string]any{"current": "correct horse battery", "new": "another horse battery"}},
	} {
		if out := f.expect(c.method, c.path, c.body, 503); out["error"] != errAuditUnavailable {
			t.Errorf("%s %s: %v", c.method, c.path, out)
		}
	}
	ctx := context.Background()
	u, err := f.st.GetUserByID(ctx, alice.ID)
	if err != nil || u.LockedAt != nil || u.IsAdmin || u.MustChangePassword {
		t.Fatalf("alice changed without an entry: %+v, %v", u, err)
	}
	if _, err := f.st.GetUserByName(ctx, "carol"); err == nil {
		t.Fatal("carol created without an entry")
	}
	// The own password is unchanged: the old one still logs in below.
	f.expect("DELETE", "/api/session", nil, 204)
	f.expect("GET", "/api/users", nil, 401)
	// The password step creates no session and writes nothing; the second
	// step needs its entry.
	if resp := f.login("root", "correct horse battery"); resp.StatusCode != 503 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	f.expect("GET", "/api/session", nil, 401)
}

func auditEntries(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	list, _ := out["entries"].([]any)
	res := make([]map[string]any, 0, len(list))
	for _, e := range list {
		res = append(res, e.(map[string]any))
	}
	return res
}

// GET /api/audit: users see their own entries, admins everyone's.
func TestAuditPage(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	f.addUser("admin", "correct horse battery", true)
	alice := f.addUser("alice", "correct horse battery", false)
	if err := f.st.Audit(ctx, store.AuditEntry{Actor: store.ActorCLI, Action: "user.add"}); err != nil {
		t.Fatal(err)
	}
	a := f.as()
	a.loginOK("alice", "correct horse battery")
	a.login("alice", "wrong horse battery")
	f.loginOK("admin", "correct horse battery")

	// alice sees her own rows: the login and the failed login, newest first.
	mine := auditEntries(t, a.expect("GET", "/api/audit", nil, 200))
	if len(mine) != 2 || mine[0]["action"] != "auth.login_failed" || mine[1]["action"] != "auth.login" {
		t.Fatalf("alice's log %v", mine)
	}
	if mine[1]["user"] != "alice" || mine[1]["clientIp"] != "127.0.0.1" || mine[1]["userAgent"] == nil || mine[1]["target"] != nil {
		t.Fatalf("alice's login row %v", mine[1])
	}
	// Reading the log writes nothing.
	if again := auditEntries(t, a.expect("GET", "/api/audit", nil, 200)); len(again) != 2 {
		t.Fatalf("reading wrote rows: %d", len(again))
	}
	a.expect("GET", "/api/audit?user=admin", nil, 403)
	a.expect("GET", "/api/audit?actor=cli", nil, 403)

	// The admin sees everyone: alice's two, the command, the own login.
	all := auditEntries(t, f.expect("GET", "/api/audit", nil, 200))
	if len(all) != 4 {
		t.Fatalf("admin sees %d rows: %v", len(all), all)
	}
	if rows := auditEntries(t, f.expect("GET", "/api/audit?user=alice", nil, 200)); len(rows) != 2 {
		t.Fatalf("user=alice: %v", rows)
	}
	if rows := auditEntries(t, f.expect("GET", "/api/audit?actor=cli", nil, 200)); len(rows) != 1 || rows[0]["action"] != "user.add" || rows[0]["actor"] != "cli" {
		t.Fatalf("actor=cli: %v", rows)
	}
	if rows := auditEntries(t, f.expect("GET", "/api/audit?action=users", nil, 200)); len(rows) != 1 {
		t.Fatalf("action group: %v", rows)
	}
	if rows := auditEntries(t, f.expect("GET", "/api/audit?action=auth.login", nil, 200)); len(rows) != 2 {
		t.Fatalf("exact action: %v", rows)
	}
	// A name without a user selects the entries carrying it: none here.
	if rows := auditEntries(t, f.expect("GET", "/api/audit?user=nobody", nil, 200)); len(rows) != 0 {
		t.Fatalf("user=nobody: %v", rows)
	}

	// Pages of 1 add up to everything, newest first, without repeats.
	var ids []float64
	cursor := ""
	for range 10 {
		out := f.expect("GET", "/api/audit?limit=1&cursor="+url.QueryEscape(cursor), nil, 200)
		for _, e := range auditEntries(t, out) {
			ids = append(ids, e["id"].(float64))
		}
		next, _ := out["nextCursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(ids) != 4 {
		t.Fatalf("paged ids %v", ids)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] >= ids[i-1] {
			t.Fatalf("not newest first: %v", ids)
		}
	}

	// A period without entries, and bad parameters.
	if rows := auditEntries(t, f.expect("GET", "/api/audit?since=2020-01-01&until=2020-01-31", nil, 200)); len(rows) != 0 {
		t.Fatalf("old period: %v", rows)
	}
	for _, q := range []string{"since=yesterday", "until=2020-13-01", "since=2026-02-01&until=2026-01-01", "action=everything", "actor=root", "cursor=%21", "limit=501", "limit=0"} {
		f.expect("GET", "/api/audit?"+q, nil, 400)
	}

	// Removing alice keeps her entries, under her name at the time.
	if err := f.st.DeleteUser(ctx, alice.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if rows := auditEntries(t, f.expect("GET", "/api/audit", nil, 200)); len(rows) != 4 || rows[2]["user"] != "alice" {
		t.Fatalf("after removing alice: %v", rows)
	}
	// and the filter by name still finds them.
	if rows := auditEntries(t, f.expect("GET", "/api/audit?user=alice", nil, 200)); len(rows) != 2 || rows[0]["user"] != "alice" {
		t.Fatalf("user=alice after removing her: %v", rows)
	}
}

// The log needs a login.
func TestAuditNeedsLogin(t *testing.T) {
	f := newAuthFixture(t)
	f.expect("GET", "/api/audit", nil, 401)
}
