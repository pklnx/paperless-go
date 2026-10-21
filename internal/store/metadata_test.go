package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

func mustCorrespondent(t *testing.T, st *store.Store, sc store.Scope, name string) *store.Correspondent {
	t.Helper()
	c, err := st.CreateCorrespondent(context.Background(), sc, store.CorrespondentInput{Name: name})
	if err != nil {
		t.Fatalf("create correspondent %q: %v", name, err)
	}
	return c
}

func mustTag(t *testing.T, st *store.Store, sc store.Scope, name string, parent *store.Tag) *store.Tag {
	t.Helper()
	in := store.TagInput{Name: name}
	if parent != nil {
		in.ParentID = &parent.ID
	}
	tag, err := st.CreateTag(context.Background(), sc, in)
	if err != nil {
		t.Fatalf("create tag %q: %v", name, err)
	}
	return tag
}

func TestMetadataDefaultsAndUpdate(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}

	c := mustCorrespondent(t, st, alice, "Bank")
	if c.MatchMode != "any" || c.OwnerID != alice.UserID || c.CreatedAt.IsZero() {
		t.Errorf("correspondent = %+v", c)
	}
	c2, err := st.UpdateCorrespondent(ctx, alice, c.ID, store.CorrespondentInput{Name: "Sparkasse", MatchText: "IBAN", MatchMode: "exact"})
	if err != nil || c2.Name != "Sparkasse" || c2.MatchText != "IBAN" || c2.MatchMode != "exact" {
		t.Errorf("updated correspondent = %+v, %v", c2, err)
	}
	if _, err := st.UpdateCorrespondent(ctx, alice, c.ID, store.CorrespondentInput{Name: "x", MatchMode: "fuzzy"}); err == nil {
		t.Error("unknown match mode accepted")
	}

	typ, err := st.CreateDocumentType(ctx, alice, store.DocumentTypeInput{Name: "Invoice"})
	if err != nil || typ.Symbol != "doc.text" || typ.MatchMode != "any" {
		t.Fatalf("document type = %+v, %v", typ, err)
	}
	typ2, err := st.UpdateDocumentType(ctx, alice, typ.ID, store.DocumentTypeInput{Name: "Bill", Symbol: "eurosign"})
	if err != nil || typ2.Name != "Bill" || typ2.Symbol != "eurosign" {
		t.Errorf("updated type = %+v, %v", typ2, err)
	}

	tag := mustTag(t, st, alice, "Tax", nil)
	if tag.ColorHex != "#3B82F6" || tag.Symbol != "tag.fill" || tag.ParentID != nil {
		t.Errorf("tag = %+v", tag)
	}
	if _, err := st.CreateTag(ctx, alice, store.TagInput{Name: "Bad", ColorHex: "red"}); err == nil {
		t.Error("invalid color accepted")
	}
	if _, err := st.CreateTag(ctx, alice, store.TagInput{Name: "  "}); err == nil {
		t.Error("blank name accepted")
	}
	got, err := st.GetTag(ctx, alice, tag.ID)
	if err != nil || got.Name != "Tax" {
		t.Errorf("get tag = %+v, %v", got, err)
	}
}

func TestNamesCaseInsensitivePerOwner(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}

	mustCorrespondent(t, st, alice, "Stadtwerke")
	if _, err := st.CreateCorrespondent(ctx, alice, store.CorrespondentInput{Name: "STADTWERKE"}); !errors.Is(err, store.ErrNameTaken) {
		t.Errorf("correspondent in other case: %v, want ErrNameTaken", err)
	}
	mustCorrespondent(t, st, bob, "stadtwerke")

	if _, err := st.CreateDocumentType(ctx, alice, store.DocumentTypeInput{Name: "Invoice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateDocumentType(ctx, alice, store.DocumentTypeInput{Name: "invoice"}); !errors.Is(err, store.ErrNameTaken) {
		t.Errorf("type in other case: %v, want ErrNameTaken", err)
	}
	if _, err := st.CreateDocumentType(ctx, bob, store.DocumentTypeInput{Name: "INVOICE"}); err != nil {
		t.Errorf("same type name for bob: %v", err)
	}

	// Renaming into a taken name fails too.
	other := mustCorrespondent(t, st, alice, "Bank")
	if _, err := st.UpdateCorrespondent(ctx, alice, other.ID, store.CorrespondentInput{Name: "stadtWERKE"}); !errors.Is(err, store.ErrNameTaken) {
		t.Errorf("rename into taken name: %v, want ErrNameTaken", err)
	}

	mustTag(t, st, alice, "Tax", nil)
	if _, err := st.CreateTag(ctx, alice, store.TagInput{Name: "tax"}); !errors.Is(err, store.ErrNameTaken) {
		t.Errorf("root tag in other case: %v, want ErrNameTaken", err)
	}
	mustTag(t, st, bob, "TAX", nil)
}

func TestTagSiblings(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	work := mustTag(t, st, alice, "Work", nil)
	home := mustTag(t, st, alice, "Home", nil)

	mustTag(t, st, alice, "Tax", work)
	if _, err := st.CreateTag(ctx, alice, store.TagInput{Name: "TAX", ParentID: &work.ID}); !errors.Is(err, store.ErrNameTaken) {
		t.Errorf("sibling with same name: %v, want ErrNameTaken", err)
	}
	// The same name under another parent, or as a root, is fine.
	homeTax := mustTag(t, st, alice, "Tax", home)
	mustTag(t, st, alice, "Tax", nil)
	// Moving a tag next to a sibling of the same name fails.
	if _, err := st.UpdateTag(ctx, alice, homeTax.ID, store.TagInput{Name: "Tax", ParentID: &work.ID}); !errors.Is(err, store.ErrNameTaken) {
		t.Errorf("move next to same-named sibling: %v, want ErrNameTaken", err)
	}
}

func TestTagCycles(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	a := mustTag(t, st, alice, "A", nil)
	b := mustTag(t, st, alice, "B", a)
	c := mustTag(t, st, alice, "C", b)

	for name, parent := range map[string]string{"self": a.ID, "child": b.ID, "grandchild": c.ID} {
		if _, err := st.UpdateTag(ctx, alice, a.ID, store.TagInput{Name: "A", ParentID: &parent}); !errors.Is(err, store.ErrTagCycle) {
			t.Errorf("A under %s: %v, want ErrTagCycle", name, err)
		}
	}
	if got, _ := st.GetTag(ctx, alice, a.ID); got.ParentID != nil {
		t.Errorf("A's parent changed to %v", *got.ParentID)
	}
	// Moving within the tree without a cycle works.
	if moved, err := st.UpdateTag(ctx, alice, c.ID, store.TagInput{Name: "C", ParentID: &a.ID}); err != nil || *moved.ParentID != a.ID {
		t.Errorf("move C under A: %+v, %v", moved, err)
	}
}

// TestConcurrentTagCycle runs A→B and B→A at the same time: the advisory
// lock in tags_no_cycle lets exactly one of them commit.
func TestConcurrentTagCycle(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	a := mustTag(t, st, alice, "A", nil)
	b := mustTag(t, st, alice, "B", nil)

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// The first change: A under B, not yet committed.
	if _, err := tx.Exec(ctx, "UPDATE tags SET parent_id = $1 WHERE id = $2", b.ID, a.ID); err != nil {
		t.Fatal(err)
	}

	// The second change: B under A, through the store.
	done := make(chan error, 1)
	go func() {
		_, err := st.UpdateTag(ctx, alice, b.ID, store.TagInput{Name: "B", ParentID: &a.ID})
		done <- err
	}()

	// Wait until the second change waits for the advisory lock.
	for {
		var waiting int
		err := conn.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND wait_event = 'advisory'`).Scan(&waiting)
		if err == nil && waiting > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("second change finished without waiting: %v", err)
		case <-ctx.Done():
			t.Fatal("second change never waited for the lock")
		default:
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("first change: %v", err)
	}
	if err := <-done; !errors.Is(err, store.ErrTagCycle) {
		t.Fatalf("second change: %v, want ErrTagCycle", err)
	}
	gotA, _ := st.GetTag(ctx, alice, a.ID)
	gotB, _ := st.GetTag(ctx, alice, b.ID)
	if gotA.ParentID == nil || *gotA.ParentID != b.ID || gotB.ParentID != nil {
		t.Errorf("A.parent = %v, B.parent = %v; want A under B only", gotA.ParentID, gotB.ParentID)
	}
}

func TestDeleteParentTagMakesRoots(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	work := mustTag(t, st, alice, "Work", nil)
	child := mustTag(t, st, alice, "Projects", work)
	grandchild := mustTag(t, st, alice, "Old", child)

	if err := st.DeleteTag(ctx, alice, work.ID); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetTag(ctx, alice, child.ID)
	if err != nil || got.ParentID != nil {
		t.Errorf("child after deleting parent = %+v, %v; want a root", got, err)
	}
	if got, _ := st.GetTag(ctx, alice, grandchild.ID); got.ParentID == nil || *got.ParentID != child.ID {
		t.Errorf("grandchild moved: %+v", got)
	}
}

func TestDeleteParentTagNameConflict(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	rootTax := mustTag(t, st, alice, "Tax", nil)
	work := mustTag(t, st, alice, "Work", nil)
	workTax := mustTag(t, st, alice, "tax", work)

	err := st.DeleteTag(ctx, alice, work.ID)
	var conflict *store.TagNameConflictError
	if !errors.Is(err, store.ErrTagNameConflict) || !errors.As(err, &conflict) ||
		conflict.ChildID != workTax.ID || conflict.ChildName != "tax" {
		t.Fatalf("delete Work: %v, want TagNameConflictError for %s", err, workTax.ID)
	}
	tags, err := st.ListTags(ctx, alice)
	if err != nil || len(tags) != 3 {
		t.Fatalf("tags after refused delete = %d (%v), want 3", len(tags), err)
	}
	for _, id := range []string{rootTax.ID, work.ID} {
		if got, err := st.GetTag(ctx, alice, id); err != nil || got.ParentID != nil {
			t.Errorf("tag %s changed: %+v, %v", id, got, err)
		}
	}
	if got, _ := st.GetTag(ctx, alice, workTax.ID); got.ParentID == nil || *got.ParentID != work.ID {
		t.Errorf("Work/tax moved: %+v", got)
	}
}
