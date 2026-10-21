package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

func mustDeadline(t *testing.T, st *store.Store, sc store.Scope, docID string, in store.DeadlineInput) *store.Deadline {
	t.Helper()
	d, err := st.AddDeadline(context.Background(), sc, docID, in)
	if err != nil {
		t.Fatalf("add deadline: %v", err)
	}
	return d
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func TestDeadlineRoundTrip(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	doc := mustDoc(t, st, alice, newDoc("passport"))

	// Millisecond dates, as the app's archive folder format writes them.
	date := time.Date(2034, 12, 31, 11, 0, 0, 123_000_000, time.UTC)
	cal := time.Date(2026, 10, 21, 8, 15, 30, 456_000_000, time.UTC)
	rem := time.Date(2026, 10, 21, 8, 16, 0, 789_000_000, time.UTC)
	// Kind and origin of a newer app version survive unchanged.
	in := store.DeadlineInput{
		Kind: "renewalWindow", Date: date, Label: "Reisepass", Origin: "importedFromMail",
		IsDone: true, AddedToCalendarAt: &cal, AddedToRemindersAt: &rem,
	}
	d := mustDeadline(t, st, alice, doc.ID, in)
	got, err := st.ListDeadlines(ctx, alice, doc.ID)
	if err != nil || len(got) != 1 {
		t.Fatalf("list: %+v, %v", got, err)
	}
	g := got[0]
	if g.ID != d.ID || g.OwnerID != alice.UserID || g.DocumentID != doc.ID || g.Kind != "renewalWindow" ||
		!g.Date.Equal(date) || g.Label != "Reisepass" || g.Origin != "importedFromMail" || !g.IsDone ||
		!sameTime(g.AddedToCalendarAt, &cal) || !sameTime(g.AddedToRemindersAt, &rem) ||
		g.CreatedAt.IsZero() || g.ModifiedAt.IsZero() {
		t.Errorf("deadline = %+v", g)
	}

	// Empty kind and origin take the app's defaults.
	early := time.Date(2026, 11, 15, 11, 0, 0, 0, time.UTC)
	plain := mustDeadline(t, st, alice, doc.ID, store.DeadlineInput{Date: early})
	if plain.Kind != store.DefaultDeadlineKind || plain.Origin != store.DefaultDeadlineOrigin ||
		plain.IsDone || plain.AddedToCalendarAt != nil || plain.AddedToRemindersAt != nil {
		t.Errorf("defaults = %+v", plain)
	}
	// A document's deadlines come by date.
	if got, _ := st.ListDeadlines(ctx, alice, doc.ID); len(got) != 2 || got[0].ID != plain.ID || got[1].ID != d.ID {
		t.Errorf("order = %+v", got)
	}

	// Update replaces every field and keeps the document.
	later := date.Add(24 * time.Hour)
	up, err := st.UpdateDeadline(ctx, alice, d.ID, store.DeadlineInput{Kind: "due", Date: later, Origin: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if up.Kind != "due" || !up.Date.Equal(later) || up.Label != "" || up.Origin != "manual" || up.IsDone ||
		up.AddedToCalendarAt != nil || up.AddedToRemindersAt != nil || up.DocumentID != doc.ID ||
		up.ModifiedAt.Before(g.ModifiedAt) {
		t.Errorf("updated = %+v", up)
	}

	for name, err := range map[string]error{
		"add":    second(st.AddDeadline(ctx, alice, doc.ID, store.DeadlineInput{Kind: "due"})),
		"update": second(st.UpdateDeadline(ctx, alice, d.ID, store.DeadlineInput{Kind: "due"})),
	} {
		if !errors.Is(err, store.ErrNoDeadlineDate) {
			t.Errorf("%s without date: %v, want ErrNoDeadlineDate", name, err)
		}
	}

	if err := st.DeleteDeadline(ctx, alice, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteDeadline(ctx, alice, d.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if _, err := st.UpdateDeadline(ctx, alice, d.ID, store.DeadlineInput{Date: date}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("update after delete: %v", err)
	}
	if err := st.DeleteDeadline(ctx, alice, "not-a-uuid"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("malformed id: %v", err)
	}
	if got, _ := st.ListDeadlines(ctx, alice, doc.ID); len(got) != 1 || got[0].ID != plain.ID {
		t.Errorf("after delete = %+v", got)
	}
}

func TestDeadlineOwnerMismatch(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	admin := store.Scope{UserID: storetest.User(t, st, "admin", true).ID, All: true}
	bd := mustDoc(t, st, bob, newDoc("bob doc"))
	date := time.Date(2027, 6, 30, 10, 0, 0, 0, time.UTC)

	for name, id := range map[string]string{
		"other owner's document": bd.ID,
		"missing document":       "00000000-0000-4000-8000-000000000000",
		"malformed document id":  "nope",
	} {
		if _, err := st.AddDeadline(ctx, alice, id, store.DeadlineInput{Date: date}); !errors.Is(err, store.ErrOwnerMismatch) {
			t.Errorf("%s: %v, want ErrOwnerMismatch", name, err)
		}
	}
	if got, _ := st.ListDeadlines(ctx, admin, bd.ID); len(got) != 0 {
		t.Errorf("failed adds stored %+v", got)
	}

	// An admin's deadline belongs to the document's owner.
	d := mustDeadline(t, st, admin, bd.ID, store.DeadlineInput{Date: date})
	if d.OwnerID != bob.UserID {
		t.Errorf("admin's deadline owner = %d, want %d", d.OwnerID, bob.UserID)
	}

	// The database itself refuses a deadline whose owner differs from its
	// document's owner.
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, "INSERT INTO deadlines (owner_id, document_id, date) VALUES ($1, $2, now())", alice.UserID, bd.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != "deadlines_document_fkey" {
		t.Errorf("insert across owners: %v, want deadlines_document_fkey violation", err)
	}
	_, err = conn.Exec(ctx, "UPDATE deadlines SET owner_id = $1 WHERE id = $2", alice.UserID, d.ID)
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Errorf("move deadline to another owner: %v, want a foreign key violation", err)
	}
}

func TestDeadlineScope(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	admin := store.Scope{UserID: storetest.User(t, st, "admin", true).ID, All: true}
	ad := mustDoc(t, st, alice, newDoc("alice doc"))
	date := time.Date(2027, 1, 1, 11, 0, 0, 0, time.UTC)
	d := mustDeadline(t, st, alice, ad.ID, store.DeadlineInput{Date: date, Label: "alice"})

	if got, err := st.ListDeadlines(ctx, bob, ad.ID); err != nil || len(got) != 0 {
		t.Errorf("bob lists alice's deadlines: %+v, %v", got, err)
	}
	for name, err := range map[string]error{
		"update": second(st.UpdateDeadline(ctx, bob, d.ID, store.DeadlineInput{Date: date, Label: "bob"})),
		"delete": st.DeleteDeadline(ctx, bob, d.ID),
	} {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s across owners: %v, want ErrNotFound", name, err)
		}
	}
	if got, _ := st.ListDeadlines(ctx, alice, ad.ID); len(got) != 1 || got[0].Label != "alice" {
		t.Errorf("alice's deadline changed: %+v", got)
	}

	// The All scope sees and changes them.
	if got, _ := st.ListDeadlines(ctx, admin, ad.ID); len(got) != 1 {
		t.Errorf("admin sees %d deadlines, want 1", len(got))
	}
	up, err := st.UpdateDeadline(ctx, admin, d.ID, store.DeadlineInput{Date: date, Label: "by admin"})
	if err != nil || up.Label != "by admin" || up.OwnerID != alice.UserID {
		t.Errorf("admin update: %+v, %v", up, err)
	}
	if err := st.DeleteDeadline(ctx, admin, d.ID); err != nil {
		t.Errorf("admin delete: %v", err)
	}
}

func TestDeleteDocumentCascadesToDeadlines(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	gone := mustDoc(t, st, alice, newDoc("gone"))
	kept := mustDoc(t, st, alice, newDoc("kept"))
	date := time.Date(2027, 1, 1, 11, 0, 0, 0, time.UTC)
	mustDeadline(t, st, alice, gone.ID, store.DeadlineInput{Date: date})
	mustDeadline(t, st, alice, gone.ID, store.DeadlineInput{Date: date.Add(time.Hour)})
	k := mustDeadline(t, st, alice, kept.ID, store.DeadlineInput{Date: date})

	if err := st.DeleteDocument(ctx, alice, gone.ID); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM deadlines WHERE document_id = $1", gone.ID).Scan(&n); err != nil || n != 0 {
		t.Errorf("deadlines of the deleted document = %d (%v), want 0", n, err)
	}
	if got, _ := st.ListDeadlines(ctx, alice, kept.ID); len(got) != 1 || got[0].ID != k.ID {
		t.Errorf("other document's deadlines = %+v", got)
	}
}
