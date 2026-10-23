package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/db"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// A form read before a bulk change must not overwrite it: the patch with
// the old modified_at is refused with the current one, changes nothing and
// writes no audit entry. With the current value, and without a
// precondition, the patch goes through; another document is not affected.
func TestPatchDocumentPrecondition(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	conn := connect(t, dbURL)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	t1, t2, t3 := mustTag(t, st, alice, "T1", nil), mustTag(t, st, alice, "T2", nil), mustTag(t, st, alice, "T3", nil)
	in := newDoc("open in the form")
	in.TagIDs = []string{t1.ID}
	doc := mustDoc(t, st, alice, in)
	other := mustDoc(t, st, alice, newDoc("other"))
	read := getDoc(t, st, doc.ID) // the form's state
	otherRead := getDoc(t, st, other.ID)

	if _, err := bulk(t, st, alice, store.BulkSelection{IDs: []string{doc.ID}}, store.BulkChange{AddTags: []string{t2.ID}}); err != nil {
		t.Fatal(err)
	}
	afterBulk := getDoc(t, st, doc.ID)
	before := len(auditRows(t, conn))

	e := &store.AuditEntry{Actor: store.ActorSession, Action: "document.update"}
	e.SetTarget(store.TargetDocument, "", "")
	_, err := st.PatchDocument(store.WithAudit(ctx, e), alice, doc.ID, store.DocumentPatch{
		SetTags: true, TagIDs: []string{t1.ID, t3.ID}, ExpectedModifiedAt: &read.ModifiedAt,
	})
	var changed *store.DocumentChangedError
	if !errors.As(err, &changed) || !errors.Is(err, store.ErrDocumentChanged) {
		t.Fatalf("stale patch: %v, want a *DocumentChangedError", err)
	}
	if !changed.ModifiedAt.Equal(afterBulk.ModifiedAt) {
		t.Errorf("reported modified_at %v, want the current %v", changed.ModifiedAt, afterBulk.ModifiedAt)
	}
	if got := getDoc(t, st, doc.ID); !slices.Equal(sortedTags(got), ids(t1, t2)) || !got.ModifiedAt.Equal(afterBulk.ModifiedAt) {
		t.Errorf("a refused patch changed the document: tags %v, modified %v", got.TagIDs, got.ModifiedAt)
	}
	if n := len(auditRows(t, conn)); n != before {
		t.Errorf("a refused patch wrote %d audit entries", n-before)
	}

	// Outside the scope it is 404, whatever the precondition says.
	if _, err := st.PatchDocument(ctx, bob, doc.ID, store.DocumentPatch{Title: ptr("x"), ExpectedModifiedAt: &read.ModifiedAt}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's document: %v, want ErrNotFound", err)
	}

	// The current value goes through and keeps the bulk's tag only when
	// the patch does not replace it.
	got, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{
		SetTags: true, TagIDs: []string{t1.ID, t2.ID, t3.ID}, ExpectedModifiedAt: &afterBulk.ModifiedAt,
	})
	if err != nil || !slices.Equal(sortedTags(got), ids(t1, t2, t3)) || !got.ModifiedAt.After(afterBulk.ModifiedAt) {
		t.Fatalf("patch with the current value: %+v, %v", got, err)
	}
	// Without a precondition it goes through (API clients that set one
	// field).
	if _, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{IsFavorite: ptr(true)}); err != nil {
		t.Errorf("patch without precondition: %v", err)
	}
	// The other document was not touched by any of it.
	if _, err := st.PatchDocument(ctx, alice, other.ID, store.DocumentPatch{Title: ptr("renamed"), ExpectedModifiedAt: &otherRead.ModifiedAt}); err != nil {
		t.Errorf("other document: %v", err)
	}
}

// The precondition compares instants: the same modified_at in another
// zone matches, one microsecond off does not.
func TestPatchDocumentPreconditionCompareInstants(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	doc := mustDoc(t, st, alice, newDoc("zones"))
	read := getDoc(t, st, doc.ID)
	off := read.ModifiedAt.Add(-time.Microsecond)
	if _, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{Title: ptr("a"), ExpectedModifiedAt: &off}); !errors.Is(err, store.ErrDocumentChanged) {
		t.Errorf("one microsecond off: %v, want ErrDocumentChanged", err)
	}
	inZone := read.ModifiedAt.In(time.FixedZone("CEST", 2*3600))
	if _, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{Title: ptr("b"), ExpectedModifiedAt: &inZone}); err != nil {
		t.Errorf("same instant in another zone: %v", err)
	}
}

func uuidOf(t *testing.T, id string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(id); err != nil {
		t.Fatal(err)
	}
	return u
}

// Each document write query advances modified_at strictly, also twice in
// one transaction, where now() is the same: with plain now() the second
// value equals the first and a precondition read in between would hold
// although the document changed.
func TestModifiedAtStrictlyIncreasesInOneTransaction(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	conn := connect(t, dbURL)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	doc := mustDoc(t, st, alice, newDoc("twice"))
	u := uuidOf(t, doc.ID)

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	// As a store writer does: the owner's write lock first.
	if err := q.LockOwner(ctx, db.LockOwnerParams{Prefix: store.OwnerLockPrefix, OwnerID: alice.UserID}); err != nil {
		t.Fatal(err)
	}
	modified := func() time.Time {
		t.Helper()
		var m time.Time
		if err := tx.QueryRow(ctx, "SELECT modified_at FROM documents WHERE id = $1", u).Scan(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	writes := []struct {
		name string
		run  func() error
	}{
		{"PatchDocument", func() error {
			_, err := q.PatchDocument(ctx, db.PatchDocumentParams{ID: u, Title: ptr("one")})
			return err
		}},
		{"PatchDocument again", func() error {
			_, err := q.PatchDocument(ctx, db.PatchDocumentParams{ID: u, Title: ptr("two")})
			return err
		}},
		{"BulkUpdateDocuments", func() error {
			return q.BulkUpdateDocuments(ctx, db.BulkUpdateDocumentsParams{Ids: []pgtype.UUID{u}, IsFavorite: ptr(true)})
		}},
	}
	prev := modified()
	for _, w := range writes {
		if err := w.run(); err != nil {
			t.Fatalf("%s: %v", w.name, err)
		}
		now := modified()
		if !now.After(prev) {
			t.Errorf("%s: modified_at %v not after %v", w.name, now, prev)
		}
		prev = now
	}
}

// Every store function that writes a PATCH-able field or the tags moves
// modified_at forward, also when the stored value lies ahead of the clock
// (a clock stepped back): each path of PatchDocument and
// BulkUpdateDocuments.
func TestEveryDocumentWriteAdvancesModifiedAt(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	conn := connect(t, dbURL)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	tag := mustTag(t, st, alice, "T", nil)
	doc := mustDoc(t, st, alice, newDoc("writes"))
	future := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	if _, err := conn.Exec(ctx, "UPDATE documents SET modified_at = $2 WHERE id = $1", uuidOf(t, doc.ID), future); err != nil {
		t.Fatal(err)
	}
	writes := []struct {
		name string
		run  func() error
	}{
		{"PatchDocument fields", func() error {
			_, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{Title: ptr("new")})
			return err
		}},
		{"PatchDocument tags", func() error {
			_, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{SetTags: true, TagIDs: []string{tag.ID}})
			return err
		}},
		{"BulkUpdateDocuments tags", func() error {
			_, err := bulk(t, st, alice, store.BulkSelection{IDs: []string{doc.ID}}, store.BulkChange{RemoveTags: []string{tag.ID}})
			return err
		}},
		{"BulkUpdateDocuments fields", func() error {
			_, err := bulk(t, st, alice, store.BulkSelection{IDs: []string{doc.ID}}, store.BulkChange{IsInbox: ptr(false)})
			return err
		}},
	}
	prev := getDoc(t, st, doc.ID).ModifiedAt
	if !prev.Equal(future) {
		t.Fatalf("modified_at %v, want %v", prev, future)
	}
	for _, w := range writes {
		if err := w.run(); err != nil {
			t.Fatalf("%s: %v", w.name, err)
		}
		now := getDoc(t, st, doc.ID).ModifiedAt
		if !now.After(prev) {
			t.Errorf("%s: modified_at %v not after %v", w.name, now, prev)
		}
		prev = now
	}
}

// Processing does not move modified_at: a precondition read before OCR
// finished still holds after it, so no false conflict.
func TestProcessingKeepsPrecondition(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	doc := mustDoc(t, st, alice, newDoc("scan-002")) // queued for processing
	read := getDoc(t, st, doc.ID)
	now := time.Now()
	j := claim(t, st, now)
	if err := st.CompleteJob(ctx, j, now, store.ProcessResult{
		PageCount: 1, Text: "Text", TextSource: "Tesseract", ArchiveSHA256: hashOf("archive"), ArchiveSize: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if got := getDoc(t, st, doc.ID); got.PageCount != 1 || !got.ModifiedAt.Equal(read.ModifiedAt) {
		t.Fatalf("after processing: pages %d, modified %v (was %v)", got.PageCount, got.ModifiedAt, read.ModifiedAt)
	}
	if _, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{Title: ptr("Bescheid"), ExpectedModifiedAt: &read.ModifiedAt}); err != nil {
		t.Errorf("patch with the value read before processing: %v", err)
	}
}

// The precondition relies on every write of a document advancing
// modified_at with the strictly increasing form (architecture.md,
// "modified_at"). Every UPDATE of documents in the queries must use it;
// SetDocumentProcessed is an exception (processing writes no field a PATCH
// sends, so it must not cause false conflicts), and so are the trash's
// writes (#12): they change no field of the document, and a PATCH of a
// trashed document is refused anyway.
func TestDocumentUpdatesAdvanceModifiedAt(t *testing.T) {
	const greatest = "modified_at = GREATEST(now(), modified_at + interval '1 microsecond')"
	exceptions := map[string]bool{"SetDocumentProcessed": true, "TrashDocuments": true, "RestoreDocuments": true, "PurgeDocuments": true}
	files, err := filepath.Glob(filepath.Join("queries", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no query files: %v", err)
	}
	name := regexp.MustCompile(`(?m)^-- name: (\w+)`)
	update := regexp.MustCompile(`(?i)\bUPDATE\s+documents\b`)
	seen := map[string]bool{}
	checked := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		starts := name.FindAllStringSubmatchIndex(src, -1)
		if update.MatchString(src) && len(starts) == 0 {
			t.Errorf("%s: UPDATE documents outside a named query", f)
		}
		if len(starts) > 0 && update.MatchString(src[:starts[0][0]]) {
			t.Errorf("%s: UPDATE documents before the first named query", f)
		}
		for i, s := range starts {
			end := len(src)
			if i+1 < len(starts) {
				end = starts[i+1][0]
			}
			q, body := src[s[2]:s[3]], src[s[0]:end]
			if !update.MatchString(stripComments(body)) {
				continue
			}
			if exceptions[q] {
				seen[q] = true
				if strings.Contains(body, "modified_at =") {
					t.Errorf("%s: %s sets modified_at", f, q)
				}
				continue
			}
			checked++
			if n := strings.Count(stripComments(body), greatest); n != 1 {
				t.Errorf("%s: %s updates documents without %q", f, q, greatest)
			}
		}
	}
	if checked == 0 {
		t.Error("no UPDATE of documents found")
	}
	for q := range exceptions {
		if !seen[q] {
			t.Errorf("exception %s not found; remove it", q)
		}
	}
}

// stripComments removes SQL line comments.
func stripComments(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if j := strings.Index(l, "--"); j >= 0 {
			lines[i] = l[:j]
		}
	}
	return strings.Join(lines, "\n")
}
