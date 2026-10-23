package store_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/search"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// The search vocabulary (#56, part 1, 00018_search_vocabulary.sql): per
// owner, the lexemes of 5 to 64 characters in search_text and how many of
// the owner's documents hold each, kept exact by a trigger on documents.

// vocabularyVersion is the migration that adds the vocabulary.
const vocabularyVersion = 18

// vocabRow is one row of search_vocabulary.
type vocabRow struct {
	owner  int64
	lexeme string
}

// vocabulary reads search_vocabulary: the count of each row.
func vocabulary(t *testing.T, conn *pgx.Conn) map[vocabRow]int {
	t.Helper()
	return vocabQuery(t, conn, `SELECT owner_id, lexeme, n FROM search_vocabulary`)
}

// recomputed is what the vocabulary must be, computed from the documents
// with the rule written out here rather than through the migration's
// function.
func recomputed(t *testing.T, conn *pgx.Conn) map[vocabRow]int {
	t.Helper()
	return vocabQuery(t, conn, `SELECT d.owner_id, l, count(*)::int FROM documents d, unnest(tsvector_to_array(d.search_text)) l
		WHERE char_length(l) BETWEEN 5 AND 64 GROUP BY 1, 2`)
}

func vocabQuery(t *testing.T, conn *pgx.Conn, sql string) map[vocabRow]int {
	t.Helper()
	rows, err := conn.Query(context.Background(), sql)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[vocabRow]int{}
	for rows.Next() {
		var r vocabRow
		var n int
		if err := rows.Scan(&r.owner, &r.lexeme, &n); err != nil {
			t.Fatal(err)
		}
		out[r] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// checkVocabulary fails unless the vocabulary equals the recomputed
// aggregate.
func checkVocabulary(t *testing.T, conn *pgx.Conn, step string) {
	t.Helper()
	got, want := vocabulary(t, conn), recomputed(t, conn)
	if maps.Equal(got, want) {
		return
	}
	var diff []string
	for r, n := range want {
		if got[r] != n {
			diff = append(diff, fmt.Sprintf("%d/%s: %d, want %d", r.owner, r.lexeme, got[r], n))
		}
	}
	for r, n := range got {
		if _, ok := want[r]; !ok {
			diff = append(diff, fmt.Sprintf("%d/%s: %d, want none", r.owner, r.lexeme, n))
		}
	}
	slices.Sort(diff)
	t.Errorf("%s: vocabulary differs from the documents: %v", step, diff)
}

// vocabXmins maps the vocabulary rows of owner to the transaction that
// wrote them: it changes whenever a row is updated.
func vocabXmins(t *testing.T, conn *pgx.Conn, owner int64) map[string]string {
	t.Helper()
	rows, err := conn.Query(context.Background(), `SELECT lexeme, xmin::text FROM search_vocabulary WHERE owner_id = $1`, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var l, x string
		if err := rows.Scan(&l, &x); err != nil {
			t.Fatal(err)
		}
		out[l] = x
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// vocabCount returns n of one vocabulary row, 0 when it is missing.
func vocabCount(t *testing.T, conn *pgx.Conn, owner int64, lexeme string) int {
	t.Helper()
	var n int
	err := conn.QueryRow(context.Background(), `SELECT n FROM search_vocabulary WHERE owner_id = $1 AND lexeme = $2`, owner, lexeme).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Every write path that changes search_text keeps the vocabulary equal to
// the aggregate over the documents: an upload, OCR text, a title, notes
// and ASN patch, a delete. Another owner's rows are never written.
func TestSearchVocabularyUpkeep(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn := connect(t, url)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	// An upload without text, then (below) its OCR result.
	in := newDoc("Gasrechnung")
	in.OriginalFileName = "gasrechnung_2026.pdf"
	upload := mustDoc(t, st, alice, in)
	checkVocabulary(t, conn, "upload")
	job := claim(t, st, time.Now())
	if job.DocumentID != upload.ID {
		t.Fatalf("claimed %s, want %s", job.DocumentID, upload.ID)
	}
	textDoc(t, st, bob, "Stromrechnung Juli", "Abschlag für die Wohnung, Zählerstand gemeldet.")
	bobRows := vocabXmins(t, conn, bob.UserID)
	if len(bobRows) == 0 {
		t.Fatal("bob has no vocabulary")
	}
	checkVocabulary(t, conn, "bob's document")

	if err := st.CompleteJob(ctx, job, time.Now(), store.ProcessResult{PageCount: 1, Text: "Stadtwerke Abschlag Gasverbrauch Zählerstand",
		TextSource: store.TextSourceTesseract, ArchiveSHA256: hashOf("gas archive"), ArchiveSize: 1}); err != nil {
		t.Fatal(err)
	}
	checkVocabulary(t, conn, "OCR text")
	if vocabCount(t, conn, alice.UserID, "gasverbrauch") != 1 {
		t.Error("the OCR text's word is not in the vocabulary")
	}

	// An import with text sharing words with the upload.
	other := textDoc(t, st, alice, "Wasserrechnung", "Stadtwerke Abschlag Wasserverbrauch")
	checkVocabulary(t, conn, "import")
	if n := vocabCount(t, conn, alice.UserID, "stadtwerke"); n != 2 {
		t.Errorf("stadtwerke in %d documents, want 2", n)
	}

	for _, p := range []store.DocumentPatch{
		{Title: ptr("Heizkostenabrechnung")},
		{Notes: ptr("Nachzahlung geprüft")},
		{SetASN: true, ArchiveSerialNumber: ptr(int32(12345))},
		{Title: ptr("Wasserrechnung"), Notes: ptr("")},
	} {
		if _, err := st.PatchDocument(ctx, alice, other.ID, p); err != nil {
			t.Fatal(err)
		}
		checkVocabulary(t, conn, fmt.Sprintf("patch %+v", p))
	}
	if vocabCount(t, conn, alice.UserID, "heizkostenabrechnung") != 0 {
		t.Error("the replaced title stays in the vocabulary")
	}

	if err := st.DeleteDocument(ctx, alice, other.ID); err != nil {
		t.Fatal(err)
	}
	checkVocabulary(t, conn, "delete one")
	if n := vocabCount(t, conn, alice.UserID, "stadtwerke"); n != 1 {
		t.Errorf("stadtwerke in %d documents after deleting one, want 1", n)
	}
	if vocabCount(t, conn, alice.UserID, "wasserverbrauch") != 0 {
		t.Error("the deleted document's own word stays")
	}
	if err := st.DeleteDocument(ctx, alice, upload.ID); err != nil {
		t.Fatal(err)
	}
	checkVocabulary(t, conn, "delete both")
	if rows := vocabXmins(t, conn, alice.UserID); len(rows) != 0 {
		t.Errorf("alice has no documents left but vocabulary %v", rows)
	}
	if got := vocabXmins(t, conn, bob.UserID); !maps.Equal(got, bobRows) {
		t.Error("alice's writes touched bob's vocabulary")
	}
}

// Updates that leave search_text as it is write no vocabulary row: an OCR
// run with the same text, a patch that repeats the title, a raw update of
// unrelated columns.
func TestSearchVocabularyUnchangedUpdate(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn := connect(t, url)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	doc := mustDoc(t, st, alice, newDoc("Kontoauszug"))
	result := store.ProcessResult{PageCount: 1, Text: "Überweisung Gutschrift Lastschrift",
		TextSource: store.TextSourceTesseract, ArchiveSHA256: hashOf("archive"), ArchiveSize: 1}
	if err := st.CompleteJob(ctx, claim(t, st, time.Now()), time.Now(), result); err != nil {
		t.Fatal(err)
	}
	before := vocabXmins(t, conn, alice.UserID)
	if before["gutschrift"] == "" {
		t.Fatalf("vocabulary %v", before)
	}
	if _, err := st.Reprocess(ctx, store.ReprocessSelection{IDs: []string{doc.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteJob(ctx, claim(t, st, time.Now()), time.Now(), result); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{Title: ptr("Kontoauszug"), IsFavorite: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE documents SET page_count = 7, title = title WHERE id = $1`, doc.ID); err != nil {
		t.Fatal(err)
	}
	if after := vocabXmins(t, conn, alice.UserID); !maps.Equal(after, before) {
		t.Errorf("unchanged updates rewrote vocabulary rows:\n%v\n%v", before, after)
	}
	checkVocabulary(t, conn, "unchanged updates")
}

// Only lexemes of 5 to 64 characters (not bytes) are kept.
func TestSearchVocabularyLengths(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	long64 := strings.Repeat("ü", 64)  // 64 characters, 128 bytes
	long65 := strings.Repeat("q", 65)  // too long
	exact64 := strings.Repeat("x", 64) // the longest kept
	textDoc(t, st, alice, "Haus", "Baum Blatt "+long64+" "+long65+" "+exact64)
	checkVocabulary(t, conn, "lengths")
	for lexeme, want := range map[string]int{
		"haus": 0, "baum": 0, "blatt": 1, long64: 1, long65: 0, exact64: 1,
		strings.Repeat("u", 64): 1, // the unaccented paperless_simple copy
	} {
		if got := vocabCount(t, conn, alice.UserID, lexeme); got != want {
			t.Errorf("%.12s… (%d characters): n = %d, want %d", lexeme, len([]rune(lexeme)), got, want)
		}
	}
	var bad int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM search_vocabulary
		WHERE char_length(lexeme) NOT BETWEEN 5 AND 64`).Scan(&bad); err != nil || bad != 0 {
		t.Errorf("%d rows outside 5 to 64 characters, %v", bad, err)
	}
}

// A count that drifted below the truth (here set by hand) never makes a
// document write fail: the decrement deletes a row at 1 instead of taking
// it to 0 against the CHECK.
func TestSearchVocabularyDriftedCount(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn := connect(t, url)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	a := textDoc(t, st, alice, "Erster Brief", "Mahnung Versicherung")
	b := textDoc(t, st, alice, "Zweiter Brief", "Mahnung Versicherung")
	if n := vocabCount(t, conn, alice.UserID, "mahnung"); n != 2 {
		t.Fatalf("mahnung in %d documents, want 2", n)
	}
	if _, err := conn.Exec(ctx, `UPDATE search_vocabulary SET n = 1 WHERE owner_id = $1 AND lexeme = 'mahnung'`, alice.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PatchDocument(ctx, alice, a.ID, store.DocumentPatch{Title: ptr("Erster Brief, gelesen")}); err != nil {
		t.Fatalf("patch with a drifted count: %v", err)
	}
	if err := st.DeleteDocument(ctx, alice, a.ID); err != nil {
		t.Fatalf("delete with a drifted count: %v", err)
	}
	if n := vocabCount(t, conn, alice.UserID, "mahnung"); n != 0 {
		t.Errorf("mahnung: n = %d after the delete, want the row gone", n)
	}
	if err := st.DeleteDocument(ctx, alice, b.ID); err != nil {
		t.Fatalf("delete of the second document: %v", err)
	}
	checkVocabulary(t, conn, "both deleted")
}

// A bulk change by match over many documents (flags, links, tags) leaves
// every vocabulary row as it was: the trigger does not even fire.
func TestSearchVocabularyBulkLeavesItAlone(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	for i := range 60 {
		textDoc(t, st, alice, fmt.Sprintf("Massenbrief %d", i), fmt.Sprintf("Rundschreiben Mitglieder Beitragserhöhung%d", i))
	}
	before := vocabXmins(t, conn, alice.UserID)
	if len(before) < 60 {
		t.Fatalf("%d vocabulary rows", len(before))
	}
	match, err := search.Parse("rundschreiben", nil)
	if err != nil {
		t.Fatal(err)
	}
	tag := mustTag(t, st, alice, "Verein", nil)
	corr := mustCorrespondent(t, st, alice, "Vorstand")
	typ := mustDocType(t, st, alice, "Brief")
	for _, c := range []store.BulkChange{
		{IsFavorite: ptr(true), IsInbox: ptr(false)},
		{AddTags: []string{tag.ID}},
		{SetCorrespondent: true, CorrespondentID: &corr.ID, SetDocumentType: true, DocumentTypeID: &typ.ID},
		{RemoveTags: []string{tag.ID}},
	} {
		ids, err := bulk(t, st, alice, store.BulkSelection{Match: &match}, c)
		if err != nil || len(ids) != 60 {
			t.Fatalf("bulk %+v: %d documents, %v", c, len(ids), err)
		}
	}
	if after := vocabXmins(t, conn, alice.UserID); !maps.Equal(after, before) {
		t.Error("a bulk change rewrote vocabulary rows")
	}
	checkVocabulary(t, conn, "after the bulk changes")
}

// The owner's write lock serialises the vocabulary upkeep: while alice's
// patch holds her lock, her second writer of the same lexemes waits at the
// advisory lock, bob's writer finishes without any lock wait, and after
// the release the counts are exact.
func TestSearchVocabularyConcurrentWriters(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := connect(t, dbURL)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	a1 := textDoc(t, st, alice, "Erste Abrechnung", "Nebenkosten Hausmeister")
	a2 := textDoc(t, st, alice, "Zweite Abrechnung", "Nebenkosten Hausmeister")
	b1 := textDoc(t, st, bob, "Bobs Abrechnung", "Nebenkosten Hausmeister")
	aliceStore := openWith(t, dbURL, map[string]string{"application_name": "alice-second"})
	bobStore := openWith(t, dbURL, map[string]string{"application_name": "bob-writer"})

	h := holdAfterLock(ctx, t, "alice's patch", func(ctx context.Context) error {
		_, err := st.PatchDocument(ctx, alice, a1.ID, store.DocumentPatch{Title: ptr("Nebenkostenabrechnung Treppenhaus")})
		return err
	})
	second := startOp(func() error {
		_, err := aliceStore.PatchDocument(ctx, alice, a2.ID, store.DocumentPatch{Title: ptr("Nebenkostenabrechnung Treppenhaus")})
		return err
	})
	waitForAdvisory(ctx, t, conn, "alice-second", 1, second)
	if err := finishesWithoutLockWait(ctx, t, conn, "bob-writer", startOp(func() error {
		_, err := bobStore.PatchDocument(ctx, bob, b1.ID, store.DocumentPatch{Title: ptr("Nebenkostenabrechnung Treppenhaus")})
		return err
	})); err != nil {
		t.Fatalf("bob's patch: %v", err)
	}
	close(h.release)
	if err := <-h.done; err != nil {
		t.Fatalf("alice's patch: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("alice's second patch: %v", err)
	}
	checkVocabulary(t, conn, "concurrent writers")
	for owner, want := range map[int64]int{alice.UserID: 2, bob.UserID: 1} {
		if n := vocabCount(t, conn, owner, "treppenhaus"); n != want {
			t.Errorf("owner %d: treppenhaus in %d documents, want %d", owner, n, want)
		}
	}
}

// The migration fills the vocabulary from existing documents, Down drops
// it and pg_trgm, and up again gives the same rows.
func TestSearchVocabularyMigration(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := storetest.User(t, st, "alice", false)
	bob := storetest.User(t, st, "bob", false)
	if err := st.MigrateDownTo(ctx, vocabularyVersion-1); err != nil {
		t.Fatal(err)
	}
	conn := connect(t, url)
	if _, err := conn.Exec(ctx, "SELECT 1 FROM search_vocabulary"); err == nil {
		t.Fatal("search_vocabulary still there after rolling back its migration")
	}
	for i, d := range []struct {
		owner       int64
		title, text string
	}{
		{alice.ID, "Stromrechnung", "Abschlag Zählerstand Stadtwerke"},
		{alice.ID, "Gasrechnung", "Abschlag Gasverbrauch Stadtwerke"},
		{bob.ID, "Stromrechnung", "Kündigung Stadtwerke"},
	} {
		if _, err := conn.Exec(ctx, `INSERT INTO documents (owner_id, title, extracted_text, source, original_sha256, original_size, original_mime)
			VALUES ($1, $2, $3, 'file', $4, 1, 'application/pdf')`, d.owner, d.title, d.text, hashOf(fmt.Sprint("old", i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	checkVocabulary(t, conn, "backfill")
	want := vocabulary(t, conn)
	if want[vocabRow{alice.ID, "stadtwerke"}] != 2 || want[vocabRow{bob.ID, "stadtwerke"}] != 1 {
		t.Errorf("backfill: %v", want)
	}

	if err := st.MigrateDownTo(ctx, vocabularyVersion-1); err != nil {
		t.Fatalf("down: %v", err)
	}
	for _, sql := range []string{
		`SELECT 1 FROM search_vocabulary`,
		`SELECT search_vocabulary_lexemes(''::tsvector)`,
		`SELECT 'search_vocabulary_trigger'::regproc`,
	} {
		if _, err := conn.Exec(ctx, sql); err == nil {
			t.Errorf("%s works after rolling back", sql)
		}
	}
	var ext int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_extension WHERE extname = 'pg_trgm'`).Scan(&ext); err != nil || ext != 0 {
		t.Errorf("pg_trgm left installed: %d, %v", ext, err)
	}
	// Writes without the vocabulary still work, and up again counts them.
	if _, err := conn.Exec(ctx, `DELETE FROM documents WHERE title = 'Gasrechnung'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	checkVocabulary(t, conn, "up again")
	if n := vocabCount(t, conn, alice.ID, "stadtwerke"); n != 1 {
		t.Errorf("stadtwerke after up again: %d, want 1", n)
	}

	// An object outside the extension that uses pg_trgm keeps it on Down.
	if _, err := conn.Exec(ctx, `CREATE TABLE other_trgm (t text);
		CREATE INDEX other_trgm_idx ON other_trgm USING GIN (t public.gin_trgm_ops)`); err != nil {
		t.Fatal(err)
	}
	if err := st.MigrateDownTo(ctx, vocabularyVersion-1); err != nil {
		t.Fatalf("down with another user of pg_trgm: %v", err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_extension WHERE extname = 'pg_trgm'`).Scan(&ext); err != nil || ext != 1 {
		t.Errorf("pg_trgm in use elsewhere after rolling back: %d, %v", ext, err)
	}
}

// A pg_trgm installed in another schema than public stops the migration
// with a message that says how to move it; after the move it runs.
func TestSearchVocabularyMigrationNeedsTrgmInPublic(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	if err := st.MigrateDownTo(ctx, vocabularyVersion-1); err != nil {
		t.Fatal(err)
	}
	conn := connect(t, url)
	if _, err := conn.Exec(ctx, `CREATE SCHEMA extensions; CREATE EXTENSION pg_trgm SCHEMA extensions`); err != nil {
		t.Fatal(err)
	}
	_, err := st.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "pg_trgm is installed in schema extensions, it must be in public") {
		t.Fatalf("migrate with pg_trgm elsewhere: %v", err)
	}
	for _, hint := range []string{"its owner or a superuser run ALTER EXTENSION pg_trgm SET SCHEMA public", "DROP EXTENSION pg_trgm"} {
		if !strings.Contains(err.Error(), hint) {
			t.Errorf("no %q in %q", hint, err)
		}
	}
	if _, err := conn.Exec(ctx, "SELECT 1 FROM search_vocabulary"); err == nil {
		t.Error("search_vocabulary created by the failed migration")
	}
	if _, err := conn.Exec(ctx, `ALTER EXTENSION pg_trgm SET SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate after the move: %v", err)
	}
}

// The backfill cannot miss a document committed while the migration runs:
// the migration locks documents before its backfill, so a writer still
// open when it starts makes it wait, and the writer's document is counted.
func TestSearchVocabularyMigrationWaitsForWriters(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alice := storetest.User(t, st, "alice", false)
	if err := st.MigrateDownTo(ctx, vocabularyVersion-1); err != nil {
		t.Fatal(err)
	}
	conn := connect(t, dbURL)
	writer := connect(t, dbURL)
	tx, err := writer.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO documents (owner_id, title, extracted_text, source, original_sha256, original_size, original_mime)
		VALUES ($1, 'Spätzünder', 'Nachzügler Rechnung', 'file', $2, 1, 'application/pdf')`, alice.ID, hashOf("late")); err != nil {
		t.Fatal(err)
	}
	migrator := openWith(t, dbURL, map[string]string{"application_name": "migrator"})
	done := startOp(func() error {
		_, err := migrator.Migrate(ctx)
		return err
	})
	for {
		var waiting bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND application_name = 'migrator'
			  AND wait_event_type = 'Lock' AND wait_event = 'relation')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("migration finished while a writer was open: %v", err)
		case <-ctx.Done():
			t.Fatal("the migration never waited for the writer")
		default:
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("migrate: %v", err)
	}
	checkVocabulary(t, conn, "writer during the migration")
	if n := vocabCount(t, conn, alice.ID, "nachzugler"); n != 1 {
		t.Errorf("the late writer's word: n = %d, want 1", n)
	}
}
