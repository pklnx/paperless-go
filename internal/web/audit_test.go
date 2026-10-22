package web

import (
	"context"
	"encoding/json"
	"net/netip"
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

// A user also sees the changes an admin or the command line made to them,
// with who made them, but without the admin's client, user agent, API key
// or the command's operator, and with only the allowed details.
func TestAuditPageForeignEntries(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	admin := f.addUser("admin", "correct horse battery", true)
	alice := f.addUser("alice", "correct horse battery", false)
	f.addUser("bob", "correct horse battery", false)
	aliceID := strconv.FormatInt(alice.ID, 10)
	f.loginOK("admin", "correct horse battery")
	for _, patch := range []map[string]any{{"locked": true}, {"locked": false}, {"admin": true}, {"admin": false}} {
		f.expect("PATCH", "/api/users/alice", patch, 204)
	}
	// bob's lock is not alice's: her log below has one user.lock.
	f.expect("PATCH", "/api/users/bob", map[string]any{"locked": true}, 204)
	password, _ := f.expect("POST", "/api/users/alice/password", nil, 200)["password"].(string)
	// The command line, an admin's API key, an import and an export.
	add := func(e store.AuditEntry) {
		t.Helper()
		e.SetTarget(store.TargetUser, aliceID, "alice")
		if err := f.st.Audit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	cli := store.AuditEntry{Actor: store.ActorCLI, Action: "user.password_set"}
	cli.Detail("operator", "patrick")
	cli.Detail("os_user", "nonroot")
	cli.Detail("command", "paperless-go user password")
	cli.Detail("passkeysRemoved", 2)
	add(cli)
	keyID := int64(99)
	viaKey := store.AuditEntry{Actor: store.ActorAPIKey, Action: "user.2fa_reset", APIKeyID: &keyID, APIKeyName: "admin-script",
		ClientIP: netip.MustParseAddr("192.0.2.7"), UserAgent: "curl/8.0"}
	viaKey.SetUser(admin.ID, admin.Name)
	add(viaKey)
	imp := store.AuditEntry{Actor: store.ActorCLI, Action: "document.import"}
	imp.Detail("path", "/srv/archive/alice")
	imp.Detail("imported", 3)
	imp.Detail("failed", 1)
	imp.Detail("operator", "patrick")
	add(imp)
	exp := store.AuditEntry{Actor: store.ActorCLI, Action: "audit.export"}
	exp.Detail("out", "/tmp/alice.jsonl")
	exp.Detail("user", "alice")
	exp.Detail("format", "jsonl")
	exp.Detail("future_key", "secret")
	add(exp)

	a := f.as()
	a.loginOK("alice", password)
	a.expect("PUT", "/api/profile/password", map[string]any{"current": password, "new": "another horse battery"}, 204)
	mine := auditEntries(t, a.expect("GET", "/api/audit", nil, 200))
	byAction := map[string]map[string]any{}
	var actions []string
	for _, e := range mine {
		act, _ := e["action"].(string)
		actions = append(actions, act)
		byAction[act] = e
	}
	want := "profile.password_change auth.login audit.export document.import user.2fa_reset user.password_set user.password_reset user.demote user.promote user.unlock user.lock"
	if got := strings.Join(actions, " "); got != want {
		t.Fatalf("alice's log:\n got %s\nwant %s", got, want)
	}
	for _, act := range []string{"user.lock", "user.unlock", "user.promote", "user.demote", "user.password_reset", "user.2fa_reset"} {
		e := byAction[act]
		if e["byOther"] != true || e["user"] != "admin" || e["clientIp"] != nil || e["userAgent"] != nil || e["apiKey"] != nil {
			t.Errorf("%s: %v", act, e)
		}
	}
	if e := byAction["user.2fa_reset"]; e["actor"] != "api_key" {
		t.Errorf("API key entry: %v", e)
	}
	for act, d := range map[string]map[string]any{
		"user.password_set": {"passkeysRemoved": float64(2)},
		"document.import":   {"imported": float64(3), "failed": float64(1)},
		"audit.export":      {"format": "jsonl"},
	} {
		e := byAction[act]
		if e["byOther"] != true || e["actor"] != "cli" || e["user"] != nil {
			t.Errorf("%s: %v", act, e)
		}
		got, _ := json.Marshal(e["details"])
		w, _ := json.Marshal(d)
		if string(got) != string(w) {
			t.Errorf("%s details %s, want %s", act, got, w)
		}
	}
	// alice's own rows keep their client and have no byOther.
	if e := byAction["auth.login"]; e["byOther"] != nil || e["clientIp"] != "127.0.0.1" || e["userAgent"] == nil || e["user"] != "alice" {
		t.Errorf("alice's login: %v", e)
	}
	a.expect("GET", "/api/audit?user=admin", nil, 403)
	a.expect("GET", "/api/audit?actor=cli", nil, 403)
	// The action filter covers the entries about her.
	if rows := auditEntries(t, a.expect("GET", "/api/audit?action=user.lock", nil, 200)); len(rows) != 1 || rows[0]["byOther"] != true {
		t.Errorf("action=user.lock: %v", rows)
	}

	// The admin sees the same entries in full, without byOther.
	all := auditEntries(t, f.expect("GET", "/api/audit?action=users&action=export&action=document.import", nil, 200))
	seen := map[string]bool{}
	for _, e := range all {
		if e["byOther"] != nil {
			t.Errorf("admin view with byOther: %v", e)
		}
		act, _ := e["action"].(string)
		d, _ := e["details"].(map[string]any)
		switch act {
		case "user.password_set":
			seen[act] = d["operator"] == "patrick" && d["os_user"] == "nonroot" && d["command"] != nil
		case "user.2fa_reset":
			seen[act] = e["apiKey"] == "admin-script" && e["clientIp"] == "192.0.2.7" && e["userAgent"] == "curl/8.0"
		case "user.lock":
			seen[act] = seen[act] || e["clientIp"] == "127.0.0.1" && e["userAgent"] != nil
		case "audit.export":
			seen[act] = d["out"] != nil && d["user"] == "alice" && d["future_key"] == "secret"
		case "document.import":
			seen[act] = d["path"] != nil
		}
	}
	for _, act := range []string{"user.password_set", "user.2fa_reset", "user.lock", "audit.export", "document.import"} {
		if !seen[act] {
			t.Errorf("admin view of %s incomplete: %v", act, all)
		}
	}
}

// The log needs a login.
func TestAuditNeedsLogin(t *testing.T) {
	f := newAuthFixture(t)
	f.expect("GET", "/api/audit", nil, 401)
}

// The server's entries (actor system): admins filter by the actor, the
// user they are about sees them with only the counts, others do not.
func TestAuditPageSystemEntries(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	f.addUser("admin", "correct horse battery", true)
	alice := f.addUser("alice", "correct horse battery", false)
	f.addUser("bob", "correct horse battery", false)
	e := store.AuditEntry{Actor: store.ActorSystem, Action: "document.consume"}
	e.SetTarget(store.TargetUser, strconv.FormatInt(alice.ID, 10), "alice")
	e.Detail("imported", 2)
	e.Detail("duplicates", 1)
	e.Detail("failed", 0)
	e.Detail("interrupted", true)
	e.Detail("future_key", "secret")
	if err := f.st.Audit(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := f.st.Audit(ctx, store.AuditEntry{Actor: store.ActorCLI, Action: "user.add"}); err != nil {
		t.Fatal(err)
	}

	f.loginOK("admin", "correct horse battery")
	rows := auditEntries(t, f.expect("GET", "/api/audit?actor=system", nil, 200))
	if len(rows) != 1 || rows[0]["actor"] != "system" || rows[0]["action"] != "document.consume" || rows[0]["user"] != nil || rows[0]["byOther"] != nil {
		t.Fatalf("actor=system: %v", rows)
	}
	if d, _ := rows[0]["details"].(map[string]any); d["future_key"] != "secret" {
		t.Errorf("admin's details: %v", d)
	}

	a := f.as()
	a.loginOK("alice", "correct horse battery")
	a.expect("GET", "/api/audit?actor=system", nil, 403)
	var mine []map[string]any
	for _, r := range auditEntries(t, a.expect("GET", "/api/audit", nil, 200)) {
		if r["action"] == "document.consume" {
			mine = append(mine, r)
		}
	}
	if len(mine) != 1 || mine[0]["actor"] != "system" || mine[0]["byOther"] != true || mine[0]["user"] != nil {
		t.Fatalf("alice's log: %v", mine)
	}
	got, _ := json.Marshal(mine[0]["details"])
	if want := `{"duplicates":1,"failed":0,"imported":2,"interrupted":true}`; string(got) != want {
		t.Errorf("alice's details %s, want %s", got, want)
	}

	b := f.as()
	b.loginOK("bob", "correct horse battery")
	for _, r := range auditEntries(t, b.expect("GET", "/api/audit", nil, 200)) {
		if r["action"] == "document.consume" {
			t.Errorf("bob sees alice's entry: %v", r)
		}
	}
}
