package store_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

func connect(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func partitions(t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(context.Background(),
		`SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		 WHERE i.inhparent = 'audit_log'::regclass ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// partitionName is the partition months after the database's current
// month (UTC).
func partitionName(t *testing.T, conn *pgx.Conn, months int) string {
	t.Helper()
	var name string
	if err := conn.QueryRow(context.Background(),
		`SELECT 'audit_log_' || to_char(date_trunc('month', now() AT TIME ZONE 'UTC') + make_interval(months => $1), 'YYYY_MM')`,
		months).Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}

type auditRow struct {
	Actor, Action                   string
	UserID                          *int64
	UserName, TargetType, TargetID  *string
	TargetName, ClientIP, UserAgent *string
	Details                         map[string]any
}

func auditRows(t *testing.T, conn *pgx.Conn) []auditRow {
	t.Helper()
	rows, err := conn.Query(context.Background(),
		`SELECT l.actor, l.action, l.user_id, l.user_name, l.target_type, l.target_id, l.target_name,
		        host(l.client_ip), ua.user_agent, l.details
		 FROM audit_log l LEFT JOIN audit_user_agents ua ON ua.id = l.user_agent_id ORDER BY l.at, l.id`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (auditRow, error) {
		var a auditRow
		err := r.Scan(&a.Actor, &a.Action, &a.UserID, &a.UserName, &a.TargetType, &a.TargetID, &a.TargetName,
			&a.ClientIP, &a.UserAgent, &a.Details)
		return a, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAuditPartitions(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	want := []string{partitionName(t, conn, 0), partitionName(t, conn, 1), partitionName(t, conn, 2), partitionName(t, conn, 3)}
	if got := partitions(t, conn); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("after migrate: %v, want %v", got, want)
	}
	// Idempotent and safe in parallel: two calls create months 4 and 5 once.
	var wg sync.WaitGroup
	created := make([]int, 2)
	for i := range 2 {
		wg.Go(func() {
			n, err := st.EnsureAuditPartitions(ctx, 5)
			if err != nil {
				t.Error(err)
			}
			created[i] = n
		})
	}
	wg.Wait()
	if created[0]+created[1] != 2 || len(partitions(t, conn)) != 6 {
		t.Fatalf("created %v, partitions %v", created, partitions(t, conn))
	}
	if n, err := st.EnsureAuditPartitions(ctx, 5); err != nil || n != 0 {
		t.Fatalf("again: %d, %v", n, err)
	}
}

func TestAuditAppendOnly(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	e := store.AuditEntry{Actor: store.ActorSession, Action: "auth.login", UserAgent: "test-agent", ClientIP: netip.MustParseAddr("192.0.2.1")}
	if err := st.Audit(ctx, e); err != nil {
		t.Fatal(err)
	}
	part := partitionName(t, connect(t, url), 0)
	statements := []string{
		"UPDATE audit_log SET action = 'x.y'",
		"DELETE FROM audit_log",
		"TRUNCATE audit_log",
		"UPDATE " + part + " SET action = 'x.y'",
		"DELETE FROM " + part,
		"TRUNCATE " + part,
		"UPDATE audit_user_agents SET user_agent = 'x'",
		"DELETE FROM audit_user_agents",
		"TRUNCATE audit_user_agents CASCADE",
	}
	for _, replica := range []bool{false, true} {
		conn := connect(t, url)
		if replica {
			if _, err := conn.Exec(ctx, "SET session_replication_role = replica"); err != nil {
				t.Fatal(err)
			}
		}
		for _, sql := range statements {
			if _, err := conn.Exec(ctx, sql); err == nil {
				t.Errorf("replica %v: %s succeeded", replica, sql)
			}
		}
	}
	if rows := auditRows(t, connect(t, url)); len(rows) != 1 || rows[0].Action != "auth.login" {
		t.Fatalf("rows changed: %+v", rows)
	}
}

// TestAuditAppendOnlyOwner checks the privileges of a non-superuser owner,
// which a superuser (as in CI) does not have.
func TestAuditAppendOnlyOwner(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	if err := st.Audit(ctx, store.AuditEntry{Actor: store.ActorCLI, Action: "user.add"}); err != nil {
		t.Fatal(err)
	}
	conn := connect(t, url)
	var canCreate bool
	if err := conn.QueryRow(ctx, "SELECT rolcreaterole OR rolsuper FROM pg_roles WHERE rolname = current_user").Scan(&canCreate); err != nil {
		t.Fatal(err)
	}
	if !canCreate {
		t.Skip("the test role cannot create roles")
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	role := "audit_owner_" + hex.EncodeToString(suffix)
	ident := pgx.Identifier{role}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE ROLE "+ident); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), url)
		if err != nil {
			return
		}
		defer func() { _ = c.Close(context.Background()) }()
		_, _ = c.Exec(context.Background(), "REASSIGN OWNED BY "+ident+" TO CURRENT_USER")
		_, _ = c.Exec(context.Background(), "DROP OWNED BY "+ident)
		_, _ = c.Exec(context.Background(), "DROP ROLE "+ident)
	})
	// Owner changes carry the owner's (revoked) privileges over.
	for _, tbl := range append([]string{"audit_log", "audit_user_agents"}, partitions(t, conn)...) {
		if _, err := conn.Exec(ctx, "ALTER TABLE "+tbl+" OWNER TO "+ident); err != nil {
			t.Fatal(err)
		}
	}
	part := partitionName(t, conn, 0)
	if _, err := conn.Exec(ctx, "SET ROLE "+ident); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"UPDATE audit_log SET action = 'x.y'",
		"DELETE FROM audit_log",
		"TRUNCATE audit_log",
		"TRUNCATE " + part,
		"DELETE FROM audit_user_agents",
	} {
		_, err := conn.Exec(ctx, sql)
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: %v", sql, err)
		}
	}
	// Inserting still works for the owner.
	if _, err := conn.Exec(ctx, "INSERT INTO audit_log (actor, action) VALUES ('cli', 'user.add')"); err != nil {
		t.Fatalf("insert as owner: %v", err)
	}
}

// The table refuses targets of other types, and documents not named by a
// UUID.
func TestAuditTargets(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	for _, c := range []struct {
		typ, id string
		ok      bool
	}{
		{store.TargetDocument, "0193a6c4-8e2f-7c1a-9b3d-4e5f60718293", true},
		{store.TargetDocument, "42", false},
		{store.TargetDocument, "0193A6C4-8E2F-7C1A-9B3D-4E5F60718293", false},
		{store.TargetDocument, "0193a6c4-8e2f-7c1a-9b3d-4e5f60718293x", false},
		{store.TargetUser, "7", true},
		{store.TargetAPIKey, "3", true},
		{"message", "7", false},
		{"account", "7", false},
	} {
		e := store.AuditEntry{Actor: store.ActorCLI, Action: "x.y"}
		e.SetTarget(c.typ, c.id, "")
		err := st.Audit(ctx, e)
		if (err == nil) != c.ok {
			t.Errorf("%s %s: %v", c.typ, c.id, err)
		}
		if err != nil && !errors.Is(err, store.ErrAuditUnavailable) {
			t.Errorf("%s %s: error %v does not wrap ErrAuditUnavailable", c.typ, c.id, err)
		}
	}
}

func TestAuditMigrationDown(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	if err := st.Audit(ctx, store.AuditEntry{Actor: store.ActorCLI, Action: "user.add"}); err != nil {
		t.Fatal(err)
	}
	// The migrations after the audit log first.
	if m, err := st.MigrateDown(ctx); err != nil || m != "00006_two_factor.sql" {
		t.Fatalf("roll back 2FA: %q, %v", m, err)
	}
	if _, err := st.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "audit log has entries") {
		t.Fatalf("down with entries: %v", err)
	}
	empty := storetest.New(t)
	if m, err := empty.MigrateDown(ctx); err != nil || m != "00006_two_factor.sql" {
		t.Fatalf("roll back 2FA: %q, %v", m, err)
	}
	if m, err := empty.MigrateDown(ctx); err != nil || m != "00005_audit_log.sql" {
		t.Fatalf("down without entries: %q, %v", m, err)
	}
	if _, err := empty.EnsureAuditPartitions(ctx, 1); !errors.Is(err, store.ErrNotMigrated) {
		t.Fatalf("partitions without the log: %v", err)
	}
}

func TestAuditWithChange(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	e := &store.AuditEntry{Actor: store.ActorCLI, Action: "user.add"}
	e.SetTarget(store.TargetUser, "", "alice")
	actx := store.WithAudit(ctx, e)
	u, err := st.CreateUser(actx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	// A second transaction with the same context writes nothing.
	if err := st.SetUserAdmin(actx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	rows := auditRows(t, conn)
	if len(rows) != 1 || *rows[0].TargetID != fmt.Sprint(u.ID) || *rows[0].TargetName != "alice" {
		t.Fatalf("rows %+v", rows)
	}
	// A failed change writes no entry.
	if _, err := st.CreateUser(store.WithAudit(ctx, &store.AuditEntry{Actor: store.ActorCLI, Action: "user.add"}), "alice", "h", false); !errors.Is(err, store.ErrNameTaken) {
		t.Fatalf("duplicate: %v", err)
	}
	// Neither does a change the store refuses: the last admin stays.
	if err := st.SetUserLocked(store.WithAudit(ctx, &store.AuditEntry{Actor: store.ActorCLI, Action: "user.lock"}), u.ID, true); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("lock the last admin: %v", err)
	}
	if n := len(auditRows(t, conn)); n != 1 {
		t.Fatalf("%d rows after failed changes", n)
	}
	// An entry that cannot be written rolls the change back.
	bad := &store.AuditEntry{Actor: store.ActorCLI, Action: "not valid"}
	if _, err := st.CreateUser(store.WithAudit(ctx, bad), "bob", "h", false); !errors.Is(err, store.ErrAuditUnavailable) {
		t.Fatalf("invalid entry: %v", err)
	}
	if _, err := st.GetUserByName(ctx, "bob"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("bob was created: %v", err)
	}
	carol, err := st.CreateUser(ctx, "carol", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	bad = &store.AuditEntry{Actor: store.ActorCLI, Action: "not valid"}
	if err := st.SetUserAdmin(store.WithAudit(ctx, bad), carol.ID, true); !errors.Is(err, store.ErrAuditUnavailable) {
		t.Fatalf("invalid entry: %v", err)
	}
	if got, _ := st.GetUserByID(ctx, carol.ID); got.IsAdmin {
		t.Fatal("carol promoted without an entry")
	}
}

// A user with log rows, a session and a logout can be removed; the rows
// keep the ID and name.
func TestAuditUserRemoval(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	storetest.User(t, st, "admin", true)
	u := storetest.User(t, st, "alice", false)
	login := &store.AuditEntry{Actor: store.ActorSession, Action: "auth.login", UserAgent: "agent"}
	login.SetUser(u.ID, u.Name)
	now := time.Now()
	if err := st.StartSession(store.WithAudit(ctx, login), bytes.Repeat([]byte{1}, 32), u.ID, now, now.Add(time.Hour), "agent"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, bytes.Repeat([]byte{2}, 32), u.ID, now, now.Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	logout := &store.AuditEntry{Actor: store.ActorSession, Action: "auth.logout"}
	logout.SetUser(u.ID, u.Name)
	if err := st.EndSession(store.WithAudit(ctx, logout), bytes.Repeat([]byte{1}, 32)); err != nil {
		t.Fatal(err)
	}
	remove := &store.AuditEntry{Actor: store.ActorCLI, Action: "user.remove"}
	remove.SetTarget(store.TargetUser, fmt.Sprint(u.ID), u.Name)
	if err := st.DeleteUser(store.WithAudit(ctx, remove), u.ID); err != nil {
		t.Fatal(err)
	}
	rows := auditRows(t, connect(t, url))
	if len(rows) != 3 || rows[0].UserID == nil || *rows[0].UserID != u.ID || *rows[0].UserName != "alice" ||
		rows[2].Action != "user.remove" || *rows[2].TargetID != fmt.Sprint(u.ID) {
		t.Fatalf("rows %+v", rows)
	}
}

// detachCurrentMonth takes the current month's partition away, as an
// archival would, and renames it so that it can be created again.
func detachCurrentMonth(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	part := partitionName(t, conn, 0)
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	for _, sql := range []string{
		"ALTER TABLE audit_log DETACH PARTITION " + part,
		"ALTER TABLE " + part + " RENAME TO detached_" + hex.EncodeToString(suffix),
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuditMissingPartition(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	detachCurrentMonth(t, conn)
	// A single insert creates the partition and tries again.
	if err := st.Audit(ctx, store.AuditEntry{Actor: store.ActorCLI, Action: "audit.export"}); err != nil {
		t.Fatalf("audit without partition: %v", err)
	}
	if n := len(auditRows(t, conn)); n != 1 {
		t.Fatalf("%d rows", n)
	}
	// In a transaction it fails once; the next attempt works.
	detachCurrentMonth(t, conn)
	e := &store.AuditEntry{Actor: store.ActorCLI, Action: "user.add"}
	if _, err := st.CreateUser(store.WithAudit(ctx, e), "alice", "h", false); !errors.Is(err, store.ErrAuditUnavailable) {
		t.Fatalf("in a transaction: %v", err)
	}
	if _, err := st.CreateUser(store.WithAudit(ctx, &store.AuditEntry{Actor: store.ActorCLI, Action: "user.add"}), "alice", "h", false); err != nil {
		t.Fatalf("next attempt: %v", err)
	}
	if n := len(auditRows(t, conn)); n != 1 {
		t.Fatalf("%d rows in the new partition", n)
	}
}

func TestAuditCleansInput(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	// A multi-byte character across byte 256 of the user agent.
	ua := strings.Repeat("a", 255) + "é" + strings.Repeat("b", 10)
	e := store.AuditEntry{Actor: store.ActorSession, Action: "auth.login_failed", UserAgent: ua + "\x00\xff"}
	e.Detail("name", "nul\x00 and \xff invalid")
	e.Detail("list", []string{"x\x00"})
	if err := st.Audit(ctx, e); err != nil {
		t.Fatalf("audit: %v", err)
	}
	rows := auditRows(t, connect(t, url))
	got := *rows[0].UserAgent
	if !utf8.ValidString(got) || len(got) > 256 || got != strings.Repeat("a", 255) {
		t.Errorf("user agent %q (%d bytes)", got, len(got))
	}
	if q := rows[0].Details["name"]; q != "nul� and � invalid" {
		t.Errorf("name %q", q)
	}
}

// Parallel inserts of the same new user agent get one id, also from two
// stores with separate caches.
func TestAuditUserAgentRace(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	other, err := store.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 20 {
		s := st
		if i%2 == 1 {
			s = other
		}
		wg.Go(func() {
			if err := s.Audit(ctx, store.AuditEntry{Actor: store.ActorSession, Action: "auth.login", UserAgent: "same agent"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var agents, rows int
	conn := connect(t, url)
	if err := conn.QueryRow(ctx, "SELECT (SELECT count(*) FROM audit_user_agents), (SELECT count(DISTINCT user_agent_id) FROM audit_log)").Scan(&agents, &rows); err != nil {
		t.Fatal(err)
	}
	if agents != 1 || rows != 1 {
		t.Fatalf("%d user agents, %d distinct ids", agents, rows)
	}
}

// TestAuditUserAgentFewConnections runs more transactions than the pool has
// connections, all waiting for one row lock, each with an audit entry
// whose user agent is new. The user agent must be looked up before the
// transaction starts: the lock holder would otherwise wait for a
// connection that only the transactions waiting for its lock hold.
func TestAuditUserAgentFewConnections(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	storetest.User(t, st, "admin", true)
	u := storetest.User(t, st, "alice", true)
	small, err := store.Open(context.Background(), url+"&pool_max_conns=2")
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close()
	// The deadline only ends a deadlock; the test waits for the goroutines.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Go(func() {
			// Every call locks the rows of all admins (keepAnAdmin).
			e := &store.AuditEntry{Actor: store.ActorSession, Action: "user.unlock", UserAgent: fmt.Sprintf("agent %d", i)}
			if err := small.SetUserLocked(store.WithAudit(ctx, e), u.ID, false); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

func TestExportAudit(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := storetest.User(t, st, "alice", false)
	for _, a := range []string{"auth.login", "user.password_reset", "profile.password_change", "user.lock"} {
		e := store.AuditEntry{Actor: store.ActorSession, Action: a}
		if a != "user.lock" {
			e.SetUser(alice.ID, alice.Name)
		}
		if err := st.Audit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	collect := func(f store.AuditFilter) []string {
		var out []string
		if err := st.ExportAudit(ctx, f, func(r store.AuditRecord) error {
			out = append(out, r.Action)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// The database clock sets at; a day around now covers any clock skew.
	all := store.AuditFilter{Since: time.Now().Add(-24 * time.Hour), Until: time.Now().Add(24 * time.Hour)}
	if got := collect(all); strings.Join(got, " ") != "auth.login user.password_reset profile.password_change user.lock" {
		t.Errorf("all: %v", got)
	}
	f := all
	f.UserID = &alice.ID
	f.Actions = []string{"profile", "login"}
	if got := collect(f); strings.Join(got, " ") != "auth.login profile.password_change" {
		t.Errorf("alice, profile and login: %v", got)
	}
	f = all
	f.Actions = []string{"user.password_reset"}
	if got := collect(f); strings.Join(got, " ") != "user.password_reset" {
		t.Errorf("exact: %v", got)
	}
	f.Until = time.Now().Add(-24 * time.Hour)
	if got := collect(f); len(got) != 0 {
		t.Errorf("before: %v", got)
	}
	if _, err := store.AuditActionPatterns([]string{"nonsense"}); err == nil {
		t.Error("unknown action accepted")
	}
	if p, _ := store.AuditActionPatterns([]string{"user.password_reset"}); p[0] != `user.password\_reset` {
		t.Errorf("underscore not escaped: %v", p)
	}
}

// ListAudit shows a user their own entries only, and an admin scope every
// user's, optionally narrowed.
func TestListAuditScope(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := storetest.User(t, st, "alice", false)
	bob := storetest.User(t, st, "bob", false)
	for _, u := range []*store.User{alice, bob, alice} {
		e := store.AuditEntry{Actor: store.ActorSession, Action: "auth.login"}
		e.SetUser(u.ID, u.Name)
		if err := st.Audit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Audit(ctx, store.AuditEntry{Actor: store.ActorCLI, Action: "user.add"}); err != nil {
		t.Fatal(err)
	}
	page := func(p store.AuditPage) []store.AuditRecord {
		t.Helper()
		p.Since, p.Until, p.Limit = time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour), 100
		recs, err := st.ListAudit(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		return recs
	}
	own := page(store.AuditPage{Viewer: store.Scope{UserID: alice.ID}})
	if len(own) != 2 || own[0].UserName != "alice" || own[1].UserName != "alice" || own[0].ID < own[1].ID {
		t.Errorf("alice's own: %+v", own)
	}
	// UserID and Actor narrow only the admin's view.
	if got := page(store.AuditPage{Viewer: store.Scope{UserID: alice.ID}, UserID: &bob.ID, Actor: store.ActorCLI}); len(got) != 2 {
		t.Errorf("alice asking for bob's: %+v", got)
	}
	if got := page(store.AuditPage{Viewer: store.Scope{UserID: alice.ID, All: true}}); len(got) != 4 {
		t.Errorf("admin: %+v", got)
	}
	if got := page(store.AuditPage{Viewer: store.Scope{All: true}, UserID: &bob.ID}); len(got) != 1 || got[0].UserName != "bob" {
		t.Errorf("admin, bob's: %+v", got)
	}
	if got := page(store.AuditPage{Viewer: store.Scope{All: true}, Actor: store.ActorCLI}); len(got) != 1 || got[0].Action != "user.add" {
		t.Errorf("admin, cli: %+v", got)
	}
	if _, err := st.ListAudit(ctx, store.AuditPage{Limit: 1}); err == nil {
		t.Error("the empty scope listed entries")
	}
}
