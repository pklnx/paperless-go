package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

func sessionID(b byte) []byte {
	id := make([]byte, 32)
	id[0] = b
	return id
}

func TestUsers(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	now := time.Now()

	if n, err := st.CountUsers(ctx); err != nil || n != 0 {
		t.Fatalf("count: %d, %v", n, err)
	}
	admin, err := st.CreateUser(ctx, "admin", "h1", true)
	if err != nil {
		t.Fatal(err)
	}
	if admin.PasswordHash != "h1" || admin.MustChangePassword || admin.LockedAt != nil || admin.LastLoginAt != nil {
		t.Fatalf("created: %+v", admin)
	}
	if _, err := st.CreateUser(ctx, "admin", "h2", false); !errors.Is(err, store.ErrNameTaken) {
		t.Fatalf("duplicate: %v", err)
	}
	// The database only takes normalized names.
	for _, bad := range []string{"Admin", "a b", ""} {
		if _, err := st.CreateUser(ctx, bad, "h2", false); err == nil {
			t.Fatalf("name %q stored", bad)
		}
	}
	alice, err := st.CreateUser(ctx, "alice", "h3", false)
	if err != nil {
		t.Fatal(err)
	}

	// The last unlocked admin can be neither locked, demoted nor removed.
	if err := st.SetUserLocked(ctx, admin.ID, true); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("lock last admin: %v", err)
	}
	if err := st.SetUserAdmin(ctx, admin.ID, false); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("demote last admin: %v", err)
	}
	if err := st.DeleteUser(ctx, admin.ID); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("delete last admin: %v", err)
	}
	second, _ := st.CreateUser(ctx, "second", "h4", true)
	if err := st.SetUserLocked(ctx, admin.ID, true); err != nil {
		t.Fatalf("lock with a second admin: %v", err)
	}
	if err := st.DeleteUser(ctx, second.ID); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("delete the only unlocked admin: %v", err)
	}
	if err := st.SetUserLocked(ctx, admin.ID, false); err != nil {
		t.Fatal(err)
	}

	// A password set by an admin must be changed; a new password ends the
	// sessions.
	if err := st.CreateSession(ctx, sessionID(1), alice.ID, now, now.Add(time.Hour), "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserPassword(ctx, alice.ID, "h5", true); err != nil {
		t.Fatal(err)
	}
	if u, _ := st.GetUserByID(ctx, alice.ID); u.PasswordHash != "h5" || !u.MustChangePassword {
		t.Fatalf("after reset: %+v", u)
	}
	if _, err := st.GetSession(ctx, sessionID(1), now, now.Add(-time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session after a new password: %v", err)
	}
	// Changing one's own password keeps the session that did it.
	_ = st.CreateSession(ctx, sessionID(2), alice.ID, now, now.Add(time.Hour), "")
	_ = st.CreateSession(ctx, sessionID(3), alice.ID, now, now.Add(time.Hour), "")
	if err := st.ChangeOwnPassword(ctx, alice.ID, "h6", sessionID(2)); err != nil {
		t.Fatal(err)
	}
	if s, err := st.GetSession(ctx, sessionID(2), now, now.Add(-time.Hour)); err != nil || s.MustChangePassword || s.UserName != "alice" {
		t.Fatalf("own session: %+v, %v", s, err)
	}
	if _, err := st.GetSession(ctx, sessionID(3), now, now.Add(-time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other session: %v", err)
	}
	// Locking ends the sessions.
	if err := st.SetUserLocked(ctx, alice.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSession(ctx, sessionID(2), now, now.Add(-time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session of a locked user: %v", err)
	}
	if u, _ := st.GetUserByName(ctx, "alice"); u.LockedAt == nil {
		t.Fatal("not locked")
	}

	// A login records the time.
	if err := st.StartSession(ctx, sessionID(4), second.ID, now, now.Add(time.Hour), "ua"); err != nil {
		t.Fatal(err)
	}
	if u, _ := st.GetUserByID(ctx, second.ID); u.LastLoginAt == nil || !u.LastLoginAt.Equal(now.Truncate(time.Microsecond)) {
		t.Fatalf("last login: %v, want %v", u.LastLoginAt, now)
	}

	for _, err := range []error{
		st.SetUserAdmin(ctx, 99999, true),
		st.SetUserLocked(ctx, 99999, false),
		st.SetUserPassword(ctx, 99999, "h", false),
		st.DeleteUser(ctx, 99999),
		second2(st.GetUserByName(ctx, "nobody")),
	} {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("missing user: %v", err)
		}
	}

	users, err := st.ListUsers(ctx)
	if err != nil || len(users) != 3 || users[0].Name != "admin" || users[1].Name != "alice" || users[2].Name != "second" {
		t.Fatalf("list: %v, %v", users, err)
	}
}

func second2[T any](_ T, err error) error { return err }

// A user who owns nothing can be removed, also with sessions (which go
// with the user); one who owns rows cannot, and the error names the counts.
func TestDeleteUserOwnsRows(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	now := time.Now()
	storetest.User(t, st, "admin", true)
	kim := storetest.User(t, st, "kim", false)
	kimScope := store.Scope{UserID: kim.ID}
	if err := st.CreateSession(ctx, sessionID(1), kim.ID, now, now.Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}

	doc := mustDoc(t, st, kimScope, newDoc("a"))
	mustDoc(t, st, kimScope, newDoc("b"))
	tag := mustTag(t, st, kimScope, "Tax", nil)
	corr := mustCorrespondent(t, st, kimScope, "ACME")
	typ, err := st.CreateDocumentType(ctx, kimScope, store.DocumentTypeInput{Name: "Invoice"})
	if err != nil {
		t.Fatal(err)
	}
	view, err := st.CreateSavedView(ctx, kimScope, store.SavedViewInput{Name: "Inbox"})
	if err != nil {
		t.Fatal(err)
	}
	// A deadline hangs off a document: the documents count covers it.
	if _, err := st.AddDeadline(ctx, kimScope, doc.ID, store.DeadlineInput{Date: now}); err != nil {
		t.Fatal(err)
	}

	err = st.DeleteUser(ctx, kim.ID)
	var owns *store.OwnsRowsError
	if !errors.As(err, &owns) || !errors.Is(err, store.ErrOwnsRows) {
		t.Fatalf("delete an owner: %v", err)
	}
	if want := (store.OwnedRows{Documents: 2, Tags: 1, Correspondents: 1, DocumentTypes: 1, SavedViews: 1}); owns.Counts != want {
		t.Fatalf("counts %+v, want %+v", owns.Counts, want)
	}
	if got := owns.Error(); !strings.Contains(got, "2 documents, 1 tag, 1 correspondent, 1 document type, 1 saved view") {
		t.Fatalf("message %q", got)
	}
	counts, err := st.OwnedRowCounts(ctx)
	if err != nil || counts[kim.ID].Total() != 6 {
		t.Fatalf("OwnedRowCounts: %v, %v", counts, err)
	}

	for _, del := range []error{
		st.DeleteDocument(ctx, kimScope, doc.ID),
		st.DeleteTag(ctx, kimScope, tag.ID),
		st.DeleteCorrespondent(ctx, kimScope, corr.ID),
		st.DeleteDocumentType(ctx, kimScope, typ.ID),
		st.DeleteSavedView(ctx, kimScope, view.ID),
	} {
		if del != nil {
			t.Fatal(del)
		}
	}
	err = st.DeleteUser(ctx, kim.ID)
	if !errors.As(err, &owns) || owns.Counts != (store.OwnedRows{Documents: 1}) || owns.Error() != "store: the user still owns 1 document" {
		t.Fatalf("delete with one document: %v", err)
	}
	docs, err := st.ListDocuments(ctx, kimScope)
	if err != nil || len(docs) != 1 {
		t.Fatalf("documents: %v, %v", docs, err)
	}
	if err := st.DeleteDocument(ctx, kimScope, docs[0].ID); err != nil {
		t.Fatal(err)
	}

	// Owns nothing, has a session: removable, and the session is gone.
	if err := st.DeleteUser(ctx, kim.ID); err != nil {
		t.Fatalf("delete a user who owns nothing: %v", err)
	}
	if _, err := st.GetSession(ctx, sessionID(1), now, now.Add(-time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session of a removed user: %v", err)
	}
}

// A table the counts do not know yet still keeps the user (ON DELETE
// RESTRICT); the refusal of the database becomes ErrOwnsRows, for both
// codes PostgreSQL uses (23001 from version 18, 23503 before).
func TestDeleteUserRefusedByDatabase(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	kim := storetest.User(t, st, "kim", false)
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `CREATE TABLE later_owned (owner_id BIGINT NOT NULL REFERENCES users (id) ON DELETE RESTRICT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO later_owned VALUES ($1)`, kim.ID); err != nil {
		t.Fatal(err)
	}
	err = st.DeleteUser(ctx, kim.ID)
	if !errors.Is(err, store.ErrOwnsRows) {
		t.Fatalf("delete: %v, want ErrOwnsRows", err)
	}
	if err.Error() != store.ErrOwnsRows.Error() {
		t.Fatalf("message without counts: %q", err)
	}
	if _, err := st.GetUserByID(ctx, kim.ID); err != nil {
		t.Fatalf("user gone: %v", err)
	}
}

func TestSessionExpiry(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	u := storetest.User(t, st, "alice", false)
	// Times far from the database clock: only the passed time counts.
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	idle := 7 * 24 * time.Hour

	_ = st.CreateSession(ctx, sessionID(1), u.ID, now, now.Add(30*24*time.Hour), "")
	_ = st.CreateSession(ctx, sessionID(2), u.ID, now, now.Add(time.Hour), "")

	if _, err := st.GetSession(ctx, sessionID(1), now, now.Add(-idle)); err != nil {
		t.Fatalf("valid session: %v", err)
	}
	// Past the absolute expiry.
	later := now.Add(2 * time.Hour)
	if _, err := st.GetSession(ctx, sessionID(2), later, later.Add(-idle)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("past absolute expiry: %v", err)
	}
	// Idle: last used before the cutoff, unless touched.
	idleTime := now.Add(idle + time.Minute)
	if _, err := st.GetSession(ctx, sessionID(1), idleTime, idleTime.Add(-idle)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("idle session: %v", err)
	}
	if err := st.TouchSession(ctx, sessionID(1), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s, err := st.GetSession(ctx, sessionID(1), idleTime, idleTime.Add(-idle)); err != nil || !s.LastSeenAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("touched session: %+v, %v", s, err)
	}
	n, err := st.DeleteExpiredSessions(ctx, later, later.Add(-idle))
	if err != nil || n != 1 {
		t.Fatalf("cleanup: %d, %v", n, err)
	}
	if _, err := st.GetSession(ctx, sessionID(1), later, later.Add(-idle)); err != nil {
		t.Fatalf("valid session after cleanup: %v", err)
	}
}

func TestDemoteEachOtherConcurrently(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := storetest.User(t, st, "a", true)
	b := storetest.User(t, st, "b", true)
	for round := range 10 {
		var errA, errB error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); errA = st.SetUserAdmin(ctx, b.ID, false) }()
		go func() { defer wg.Done(); errB = st.SetUserAdmin(ctx, a.ID, false) }()
		wg.Wait()
		if (errA == nil) == (errB == nil) {
			t.Fatalf("round %d: %v / %v, want exactly one success", round, errA, errB)
		}
		// Make both admins again for the next round.
		_ = st.SetUserAdmin(ctx, a.ID, true)
		_ = st.SetUserAdmin(ctx, b.ID, true)
	}
}

// The login migration refuses a users table that has rows, in both
// directions: users from before it would have no password, and a rollback
// would drop every password.
func TestLoginMigrationRefusesUsers(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	storetest.User(t, st, "alice", false)

	// The audit log first, which has no entries.
	if rolledBack, err := st.MigrateDown(ctx); err != nil || rolledBack != "00005_audit_log.sql" {
		t.Fatalf("roll back the audit log: %q, %v", rolledBack, err)
	}
	if _, err := st.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "users exist") {
		t.Fatalf("roll back with users: %v", err)
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "DELETE FROM users"); err != nil {
		t.Fatal(err)
	}
	if rolledBack, err := st.MigrateDown(ctx); err != nil || rolledBack != "00004_users_login.sql" {
		t.Fatalf("roll back without users: %q, %v", rolledBack, err)
	}
	// A user created with the schema before login.
	if _, err := conn.Exec(ctx, "INSERT INTO users (name) VALUES ('early')"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("migrate with an early user: %v", err)
	}
	if _, err := conn.Exec(ctx, "DELETE FROM users"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}
