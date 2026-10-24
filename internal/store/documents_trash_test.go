package store_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/search"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// trashClock is a test clock that moves only when told.
type trashClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *trashClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *trashClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// trashStore is a store whose trash decisions use a test clock, and the
// clock.
func trashStore(t *testing.T) (*store.Store, string, *trashClock) {
	t.Helper()
	st, url := storetest.NewWithURL(t)
	c := &trashClock{now: time.Now().Truncate(time.Microsecond)}
	st.Now = c.Now
	return st, url, c
}

func searchTitles(t *testing.T, st *store.Store, sc store.Scope, q string, trashed bool) []string {
	t.Helper()
	var query search.Query
	if q != "" {
		query = parseQuery(t, q, nil)
	}
	query.Trashed = trashed
	titles := hitTitles(runSearch(t, st, sc, query))
	slices.Sort(titles)
	return titles
}

// A trashed document leaves the list, the search, the counts, the
// metadata counts and a bulk match, shows in the trash list and GET, and
// comes back with restore with everything as it was; modified_at stays.
func TestTrashRestoreRoundTrip(t *testing.T) {
	st, _, clock := trashStore(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	tag := mustTag(t, st, alice, "Steuer", nil)
	corr := mustCorrespondent(t, st, alice, "Finanzamt")
	typ := mustDocType(t, st, alice, "Bescheid")
	in := newDoc("Rechnung Strom")
	in.TagIDs, in.CorrespondentID, in.DocumentTypeID = []string{tag.ID}, &corr.ID, &typ.ID
	in.ArchiveSerialNumber, in.IsFavorite = ptr(int32(7)), true
	doc := mustDoc(t, st, alice, in)
	mustDoc(t, st, alice, newDoc("Rechnung Gas"))
	before := getDoc(t, st, doc.ID)
	if before.DeletedAt != nil || before.PurgeAt != nil {
		t.Fatalf("live document: deleted %v, purge %v", before.DeletedAt, before.PurgeAt)
	}

	clock.Add(time.Minute)
	trashedAt := clock.Now()
	if err := st.TrashDocument(ctx, alice, doc.ID); err != nil {
		t.Fatal(err)
	}
	got := getDoc(t, st, doc.ID)
	if got.DeletedAt == nil || !got.DeletedAt.Equal(trashedAt) || got.PurgeAt == nil ||
		!got.PurgeAt.Equal(trashedAt.Add(store.DefaultTrashRetention)) {
		t.Fatalf("trashed: deleted %v, purge %v", got.DeletedAt, got.PurgeAt)
	}
	if !got.ModifiedAt.Equal(before.ModifiedAt) {
		t.Errorf("modified_at %v after trash, was %v", got.ModifiedAt, before.ModifiedAt)
	}
	if got := searchTitles(t, st, alice, "", false); !slices.Equal(got, []string{"Rechnung Gas"}) {
		t.Errorf("list: %v", got)
	}
	if got := searchTitles(t, st, alice, "rechnung", false); !slices.Equal(got, []string{"Rechnung Gas"}) {
		t.Errorf("search: %v", got)
	}
	if got := searchTitles(t, st, alice, "", true); !slices.Equal(got, []string{"Rechnung Strom"}) {
		t.Errorf("trash: %v", got)
	}
	if got := searchTitles(t, st, alice, "strom", true); !slices.Equal(got, []string{"Rechnung Strom"}) {
		t.Errorf("trash search: %v", got)
	}
	if got := searchTitles(t, st, alice, "gas", true); len(got) != 0 {
		t.Errorf("trash search for a live document: %v", got)
	}
	if c, err := st.CountDocuments(ctx, alice); err != nil || c != (store.DocumentCounts{Inbox: 1, Total: 1, Trash: 1}) {
		t.Errorf("counts: %+v, %v", c, err)
	}
	tags, err := st.ListTagsWithCounts(ctx, alice)
	if err != nil || len(tags) != 1 || tags[0].DocumentCount != 0 || tags[0].SubtreeCount != 0 {
		t.Errorf("tag counts: %+v, %v", tags, err)
	}
	corrs, err := st.ListCorrespondentsWithCounts(ctx, alice)
	if err != nil || len(corrs) != 1 || corrs[0].DocumentCount != 0 {
		t.Errorf("correspondent counts: %+v, %v", corrs, err)
	}
	types, err := st.ListDocumentTypesWithCounts(ctx, alice)
	if err != nil || len(types) != 1 || types[0].DocumentCount != 0 {
		t.Errorf("type counts: %+v, %v", types, err)
	}
	q := parseQuery(t, "rechnung", nil)
	if m, err := st.CountBulkMatches(ctx, alice, store.BulkSelection{Match: &q}); err != nil || m.Count != 1 {
		t.Errorf("bulk match: %+v, %v", m, err)
	}
	if docs, err := st.ListDocuments(ctx, alice); err != nil || len(docs) != 1 {
		t.Errorf("ListDocuments: %d, %v", len(docs), err)
	}
	ref, err := st.FindDocumentByOriginal(ctx, alice.UserID, doc.OriginalSHA256)
	if err != nil || ref.ID != doc.ID || !ref.Trashed || ref.Due {
		t.Errorf("duplicate lookup: %+v, %v", ref, err)
	}

	// A trashed document cannot be trashed again or changed.
	if err := st.TrashDocument(ctx, alice, doc.ID); !errors.Is(err, store.ErrInTrash) {
		t.Errorf("trash twice: %v", err)
	}
	if _, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{Title: ptr("x")}); !errors.Is(err, store.ErrInTrash) {
		t.Errorf("patch in the trash: %v", err)
	}
	// Its ASN stays taken, and the error says where it is.
	other := mustDoc(t, st, alice, newDoc("other"))
	var taken *store.ASNTakenError
	if _, err := st.PatchDocument(ctx, alice, other.ID, store.DocumentPatch{SetASN: true, ArchiveSerialNumber: ptr(int32(7))}); !errors.As(err, &taken) ||
		taken.DocumentID != doc.ID || !taken.Trashed {
		t.Errorf("ASN of a trashed document: %v (%+v)", err, taken)
	}

	clock.Add(time.Minute)
	back, err := st.RestoreDocument(ctx, alice, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.DeletedAt != nil || back.PurgeAt != nil || !back.ModifiedAt.Equal(before.ModifiedAt) ||
		!slices.Equal(back.TagIDs, []string{tag.ID}) || *back.ArchiveSerialNumber != 7 || !back.IsFavorite ||
		!back.IsInbox || *back.CorrespondentID != corr.ID || *back.DocumentTypeID != typ.ID {
		t.Errorf("restored: %+v", back)
	}
	if got := searchTitles(t, st, alice, "rechnung", false); !slices.Equal(got, []string{"Rechnung Gas", "Rechnung Strom"}) {
		t.Errorf("search after restore: %v", got)
	}
	if c, err := st.CountDocuments(ctx, alice); err != nil || c.Trash != 0 || c.Total != 3 {
		t.Errorf("counts after restore: %+v, %v", c, err)
	}
	if _, err := st.RestoreDocument(ctx, alice, doc.ID); !errors.Is(err, store.ErrNotInTrash) {
		t.Errorf("restore a live document: %v", err)
	}
	if err := st.PurgeDocument(ctx, alice, doc.ID); !errors.Is(err, store.ErrNotInTrash) {
		t.Errorf("purge a live document: %v", err)
	}
}

// Another user's document is not found; an admin's change of it is
// audited with the owner.
func TestTrashScope(t *testing.T) {
	st, url, _ := trashStore(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	root := storetest.User(t, st, "root", true)
	admin := store.Scope{UserID: root.ID, All: true}
	doc := mustDoc(t, st, alice, newDoc("alice's"))
	for name, err := range map[string]error{
		"trash":   st.TrashDocument(ctx, bob, doc.ID),
		"purge":   st.PurgeDocument(ctx, bob, doc.ID),
		"invalid": st.TrashDocument(ctx, alice, "not-a-uuid"),
	} {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := st.RestoreDocument(ctx, bob, doc.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("restore by bob: %v", err)
	}
	for _, step := range []struct {
		action string
		run    func(context.Context) error
	}{
		{"document.delete", func(ctx context.Context) error { return st.TrashDocument(ctx, admin, doc.ID) }},
		{"document.restore", func(ctx context.Context) error { _, err := st.RestoreDocument(ctx, admin, doc.ID); return err }},
		{"document.delete", func(ctx context.Context) error { return st.TrashDocument(ctx, admin, doc.ID) }},
		{"document.purge", func(ctx context.Context) error { return st.PurgeDocument(ctx, admin, doc.ID) }},
	} {
		e := &store.AuditEntry{Actor: store.ActorCLI, Action: step.action}
		e.SetTarget(store.TargetDocument, "", "")
		if err := step.run(store.WithAudit(ctx, e)); err != nil {
			t.Fatalf("%s: %v", step.action, err)
		}
	}
	rows := auditRows(t, connect(t, url))
	if len(rows) != 4 {
		t.Fatalf("audit: %d rows", len(rows))
	}
	for i, want := range []string{"document.delete", "document.restore", "document.delete", "document.purge"} {
		r := rows[i]
		if r.Action != want || r.TargetID == nil || *r.TargetID != doc.ID || r.Details["ownerId"] != float64(alice.UserID) {
			t.Errorf("entry %d: %+v", i, r)
		}
	}
}

// "Due" uses the store's clock and retention: from deleted_at plus the
// retention on, or at once after a purge request, the document is hidden
// from GET, restore, purge, the trash and its count, and the duplicate
// lookup says so. A shorter retention applies to documents already in the
// trash; 0 makes a trashed document due at once.
func TestTrashDue(t *testing.T) {
	st, _, clock := trashStore(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	st.TrashRetention = time.Hour
	doc := mustDoc(t, st, alice, newDoc("doc"))
	if err := st.TrashDocument(ctx, alice, doc.ID); err != nil {
		t.Fatal(err)
	}
	clock.Add(time.Hour - time.Microsecond)
	if d := getDoc(t, st, doc.ID); d.PurgeAt == nil || !d.PurgeAt.Equal(clock.Now().Add(time.Microsecond)) {
		t.Fatalf("before the hour: %+v", d.PurgeAt)
	}
	clock.Add(time.Microsecond)
	hidden := func(stage string) {
		t.Helper()
		if _, err := st.GetDocument(ctx, alice, doc.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: GET %v", stage, err)
		}
		if _, err := st.RestoreDocument(ctx, alice, doc.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: restore %v", stage, err)
		}
		if err := st.PurgeDocument(ctx, alice, doc.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: purge %v", stage, err)
		}
		if err := st.TrashDocument(ctx, alice, doc.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: trash %v", stage, err)
		}
		if _, err := st.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{Title: ptr("x")}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: patch %v", stage, err)
		}
		if got := searchTitles(t, st, alice, "", true); len(got) != 0 {
			t.Errorf("%s: trash %v", stage, got)
		}
		if c, err := st.CountDocuments(ctx, alice); err != nil || c.Trash != 0 {
			t.Errorf("%s: counts %+v, %v", stage, c, err)
		}
		if ref, err := st.FindDocumentByOriginal(ctx, alice.UserID, doc.OriginalSHA256); err != nil || !ref.Trashed || !ref.Due {
			t.Errorf("%s: duplicate lookup %+v, %v", stage, ref, err)
		}
	}
	hidden("retention passed")

	// A longer retention brings it back (it is read at every decision),
	// a shorter one hides it again.
	st.TrashRetention = 2 * time.Hour
	if d := getDoc(t, st, doc.ID); d.DeletedAt == nil {
		t.Fatal("not in the trash")
	}
	st.TrashRetention = 0
	hidden("retention 0")

	// A purge request makes a document due at once.
	st.TrashRetention = store.DefaultTrashRetention
	two := mustDoc(t, st, alice, newDoc("two"))
	if err := st.TrashDocument(ctx, alice, two.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.PurgeDocument(ctx, alice, two.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetDocument(ctx, alice, two.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GET after purge: %v", err)
	}
	if _, err := st.RestoreDocument(ctx, alice, two.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("restore after purge: %v", err)
	}
}

// The tag counts leave trashed documents out also through a subtree: a
// parent counts the distinct live documents of its children.
func TestTagSubtreeCountWithoutTrash(t *testing.T) {
	st, _, _ := trashStore(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	parent := mustTag(t, st, alice, "Haus", nil)
	child := mustTag(t, st, alice, "Strom", parent)
	in := newDoc("child doc")
	in.TagIDs = []string{child.ID}
	trashed := mustDoc(t, st, alice, in)
	in = newDoc("child doc 2")
	in.TagIDs = []string{child.ID, parent.ID}
	mustDoc(t, st, alice, in)
	if err := st.TrashDocument(ctx, alice, trashed.ID); err != nil {
		t.Fatal(err)
	}
	tags, err := st.ListTagsWithCounts(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]int64{}
	for _, tc := range tags {
		got[tc.Name] = [2]int64{tc.DocumentCount, tc.SubtreeCount}
	}
	if got["Haus"] != [2]int64{1, 1} || got["Strom"] != [2]int64{1, 1} {
		t.Errorf("counts: %v", got)
	}
}

// PurgeDueDocuments deletes the documents whose retention passed and those
// whose purge was asked for, with their jobs and deadlines; queues their
// three hashes once each; audits only the retention purges, by the system;
// leaves trashed documents that are not due and live ones alone.
func TestPurgeDueDocuments(t *testing.T) {
	st, url, clock := trashStore(t)
	ctx := context.Background()
	conn := connect(t, url)
	aliceU := storetest.User(t, st, "alice", false)
	alice := store.Scope{UserID: aliceU.ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	in := newDoc("old")
	in.ArchiveSHA256, in.ArchiveSize = ptr(hashOf("old archive")), ptr(int64(1))
	old := mustDoc(t, st, alice, in)
	if _, err := conn.Exec(ctx, `UPDATE documents SET thumbnail_sha256 = $2 WHERE id = $1`, old.ID, hashOf("old thumb")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddDeadline(ctx, alice, old.ID, store.DeadlineInput{Kind: "due", Date: clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO jobs (document_id) VALUES ($1)`, old.ID); err != nil {
		t.Fatal(err)
	}
	asked := mustDoc(t, st, alice, newDoc("asked")) // has a queued job from its upload
	recent := mustDoc(t, st, bob, newDoc("recent"))
	live := mustDoc(t, st, bob, newDoc("live"))
	if err := st.TrashDocument(ctx, alice, old.ID); err != nil {
		t.Fatal(err)
	}
	clock.Add(store.DefaultTrashRetention)
	for _, d := range []*store.Document{asked, recent} {
		if err := st.TrashDocument(ctx, store.Scope{UserID: d.OwnerID}, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PurgeDocument(ctx, alice, asked.ID); err != nil {
		t.Fatal(err)
	}
	// Another document already queued the archive's hash.
	if _, err := conn.Exec(ctx, `INSERT INTO blob_removals (sha256, due_at) VALUES ($1, $2)`, hashOf("old archive"), clock.Now()); err != nil {
		t.Fatal(err)
	}

	now := clock.Now()
	res, err := st.PurgeDueDocuments(ctx, now, store.DefaultTrashRetention, 100)
	if err != nil || res != (store.PurgeResult{Scanned: 2, Purged: 2, Retention: 1}) {
		t.Fatalf("purge: %+v, %v", res, err)
	}
	for _, d := range []*store.Document{old, asked} {
		var n int
		if err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM documents WHERE id = $1) + (SELECT count(*) FROM jobs WHERE document_id = $1)
			+ (SELECT count(*) FROM deadlines WHERE document_id = $1) + (SELECT count(*) FROM document_tags WHERE document_id = $1)`, d.ID).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d rows left, %v", d.Title, n, err)
		}
	}
	for _, d := range []*store.Document{recent, live} {
		if _, err := st.GetDocument(ctx, store.Scope{All: true}, d.ID); err != nil {
			t.Errorf("%s: %v", d.Title, err)
		}
	}
	rows, err := conn.Query(ctx, `SELECT sha256, due_at FROM blob_removals ORDER BY sha256`)
	if err != nil {
		t.Fatal(err)
	}
	queued := map[string]time.Time{}
	for rows.Next() {
		var h string
		var due time.Time
		if err := rows.Scan(&h, &due); err != nil {
			t.Fatal(err)
		}
		queued[h] = due
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	want := map[string]time.Time{
		hashOf("old"): later, hashOf("old archive"): now, hashOf("old thumb"): later, hashOf("asked"): later,
	}
	if len(queued) != len(want) {
		t.Errorf("queued: %v", queued)
	}
	for h, due := range want {
		if got, ok := queued[h]; !ok || !got.Equal(due) {
			t.Errorf("queued %s: %v, %v; want %v", h, got, ok, due)
		}
	}
	var purges []auditRow
	for _, r := range auditRows(t, conn) {
		if r.Action == "document.purge" {
			purges = append(purges, r)
		}
	}
	if len(purges) != 1 || purges[0].Actor != store.ActorSystem || purges[0].UserID != nil || *purges[0].TargetID != old.ID ||
		purges[0].Details["reason"] != "retention" || purges[0].Details["ownerId"] != float64(aliceU.ID) {
		t.Errorf("purge entries: %+v", purges)
	}
	if res, err := st.PurgeDueDocuments(ctx, now, store.DefaultTrashRetention, 100); err != nil || res != (store.PurgeResult{}) {
		t.Errorf("second run: %+v, %v", res, err)
	}
	// The limit caps one call; the rest follows in the next.
	for i := range 3 {
		d := mustDoc(t, st, bob, newDoc(fmt.Sprint("batch ", i)))
		if err := st.TrashDocument(ctx, bob, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	if res, err := st.PurgeDueDocuments(ctx, now, 0, 2); err != nil || res.Scanned != 2 || res.Purged != 2 {
		t.Errorf("limit 2: %+v, %v", res, err)
	}
	// Retention 0: recent and the third batch document are due at once.
	if res, err := st.PurgeDueDocuments(ctx, now, 0, 2); err != nil || res.Scanned != 2 || res.Purged != 2 {
		t.Errorf("rest: %+v, %v", res, err)
	}
}

// A document restored between the sweeper's scan and its lock stays, and so
// does one restored and trashed again: the sweeper checks again under the
// lock.
func TestPurgeDueSkipsRestoredDocument(t *testing.T) {
	st, url, clock := trashStore(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	doc := mustDoc(t, st, alice, newDoc("doc"))
	if err := st.TrashDocument(ctx, alice, doc.ID); err != nil {
		t.Fatal(err)
	}
	clock.Add(store.DefaultTrashRetention)
	// The restore runs at a time before the retention passed (another
	// process with a clock a little behind).
	restore := store.WithBeforeLock(ctx, func() {
		st.Now = func() time.Time { return clock.Now().Add(-time.Second) }
		defer func() { st.Now = clock.Now }()
		if _, err := st.RestoreDocument(ctx, alice, doc.ID); err != nil {
			t.Errorf("restore: %v", err)
		}
	})
	res, err := st.PurgeDueDocuments(restore, clock.Now(), store.DefaultTrashRetention, 100)
	if err != nil || res != (store.PurgeResult{Scanned: 1}) {
		t.Fatalf("purge: %+v, %v", res, err)
	}
	if d := getDoc(t, st, doc.ID); d.DeletedAt != nil {
		t.Errorf("restored document: %+v", d.DeletedAt)
	}
	var n int
	if err := connect(t, url).QueryRow(ctx, `SELECT count(*) FROM blob_removals`).Scan(&n); err != nil || n != 0 {
		t.Errorf("queued: %d, %v", n, err)
	}

	// Restored and trashed again between the scan and the lock: in the
	// trash, but with a new deleted_at that is not due. Only the recheck
	// under the lock keeps it (the delete's own condition would not).
	two := mustDoc(t, st, alice, newDoc("two"))
	if err := st.TrashDocument(ctx, alice, two.ID); err != nil {
		t.Fatal(err)
	}
	clock.Add(store.DefaultTrashRetention)
	again := store.WithBeforeLock(ctx, func() {
		st.Now = func() time.Time { return clock.Now().Add(-time.Second) }
		_, err := st.RestoreDocument(ctx, alice, two.ID)
		st.Now = clock.Now
		if err != nil {
			t.Errorf("restore: %v", err)
		}
		if err := st.TrashDocument(ctx, alice, two.ID); err != nil {
			t.Errorf("trash again: %v", err)
		}
	})
	res, err = st.PurgeDueDocuments(again, clock.Now(), store.DefaultTrashRetention, 100)
	if err != nil || res != (store.PurgeResult{Scanned: 1}) {
		t.Fatalf("purge after a new trash: %+v, %v", res, err)
	}
	if d := getDoc(t, st, two.ID); d.DeletedAt == nil || !d.DeletedAt.Equal(clock.Now()) {
		t.Errorf("trashed again: %+v", d.DeletedAt)
	}
}

// Bulk trash, restore and purge by IDs and by search: the state of every
// document is checked, the match must fit the action, the fingerprint is
// bound to the action, a purge by search needs an owner, and each
// document gets its own audit entry.
func TestBulkTrashActions(t *testing.T) {
	st, url, _ := trashStore(t)
	ctx := context.Background()
	aliceU := storetest.User(t, st, "alice", false)
	alice := store.Scope{UserID: aliceU.ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	admin := store.Scope{UserID: storetest.User(t, st, "root", true).ID, All: true}
	a1 := mustDoc(t, st, alice, newDoc("Rechnung eins"))
	a2 := mustDoc(t, st, alice, newDoc("Rechnung zwei"))
	a3 := mustDoc(t, st, alice, newDoc("Brief"))
	b1 := mustDoc(t, st, bob, newDoc("Rechnung bob"))
	bulk := func(sc store.Scope, sel store.BulkSelection, c store.BulkChange) ([]string, error) {
		e := &store.AuditEntry{Actor: store.ActorCLI, Action: "document.bulk_" + string(sel.Action)}
		e.Detail("bulkRun", "run")
		return st.BulkUpdateDocuments(store.WithAudit(ctx, e), sc, sel, c, "document."+string(sel.Action))
	}
	match := func(q string, trashed bool) *search.Query {
		p := parseQuery(t, q, nil)
		p.Trashed = trashed
		return &p
	}

	// By IDs: trash two.
	if got, err := bulk(alice, store.BulkSelection{IDs: []string{a1.ID, a2.ID}, Action: store.BulkTrash}, store.BulkChange{}); err != nil || len(got) != 2 {
		t.Fatalf("trash: %v, %v", got, err)
	}
	// Fields with an action, a trashed document in an update or a
	// trash, a live one in a restore or purge: nothing changes.
	for name, c := range map[string]struct {
		sel  store.BulkSelection
		ch   store.BulkChange
		want error
	}{
		"fields with trash":     {store.BulkSelection{IDs: []string{a3.ID}, Action: store.BulkTrash}, store.BulkChange{IsFavorite: ptr(true)}, store.ErrInvalid},
		"no fields for update":  {store.BulkSelection{IDs: []string{a3.ID}}, store.BulkChange{}, store.ErrInvalid},
		"unknown action":        {store.BulkSelection{IDs: []string{a3.ID}, Action: "shred"}, store.BulkChange{}, store.ErrInvalid},
		"update a trashed one":  {store.BulkSelection{IDs: []string{a3.ID, a1.ID}}, store.BulkChange{IsFavorite: ptr(true)}, store.ErrInTrash},
		"trash a trashed one":   {store.BulkSelection{IDs: []string{a3.ID, a1.ID}, Action: store.BulkTrash}, store.BulkChange{}, store.ErrInTrash},
		"restore a live one":    {store.BulkSelection{IDs: []string{a1.ID, a3.ID}, Action: store.BulkRestore}, store.BulkChange{}, store.ErrNotInTrash},
		"purge a live one":      {store.BulkSelection{IDs: []string{a1.ID, a3.ID}, Action: store.BulkPurge}, store.BulkChange{}, store.ErrNotInTrash},
		"restore out of scope":  {store.BulkSelection{IDs: []string{b1.ID}, Action: store.BulkRestore}, store.BulkChange{}, store.ErrNotFound},
		"trash match for purge": {store.BulkSelection{Match: match("rechnung", false), Owner: &alice.UserID, Fingerprint: "v2.x", Action: store.BulkPurge}, store.BulkChange{}, store.ErrInvalid},
		"live match for trash":  {store.BulkSelection{Match: match("rechnung", true), Fingerprint: "v2.x", Action: store.BulkTrash}, store.BulkChange{}, store.ErrInvalid},
		"purge without owner":   {store.BulkSelection{Match: match("rechnung", true), Fingerprint: "v2.x", Action: store.BulkPurge}, store.BulkChange{}, store.ErrInvalid},
	} {
		if _, err := bulk(alice, c.sel, c.ch); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if d := getDoc(t, st, a3.ID); d.DeletedAt != nil || d.IsFavorite {
		t.Errorf("a3 changed: %+v", d)
	}
	if _, err := st.CountBulkMatches(ctx, alice, store.BulkSelection{Match: match("rechnung", true), Action: store.BulkPurge}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("dry run of a purge without owner: %v", err)
	}

	// By search over the trash: a dry run for restore cannot confirm a
	// purge of the same set, nor the other way round.
	restoreRun, err := st.CountBulkMatches(ctx, alice, store.BulkSelection{Match: match("rechnung", true), Action: store.BulkRestore})
	if err != nil || restoreRun.Count != 2 || !strings.HasPrefix(restoreRun.Fingerprint, "v2.") {
		t.Fatalf("restore dry run: %+v, %v", restoreRun, err)
	}
	purgeSel := store.BulkSelection{Match: match("rechnung", true), Owner: &alice.UserID, Action: store.BulkPurge}
	purgeRun, err := st.CountBulkMatches(ctx, alice, purgeSel)
	if err != nil || purgeRun.Count != 2 || purgeRun.Fingerprint == restoreRun.Fingerprint {
		t.Fatalf("purge dry run: %+v, %v", purgeRun, err)
	}
	purgeSel.Fingerprint = restoreRun.Fingerprint
	if _, err := bulk(alice, purgeSel, store.BulkChange{}); !errors.Is(err, store.ErrMatchChanged) {
		t.Errorf("purge with a restore fingerprint: %v", err)
	}
	// A v1 fingerprint (no action) is refused the same way.
	purgeSel.Fingerprint = "v1." + strings.TrimPrefix(purgeRun.Fingerprint, "v2.")
	if _, err := bulk(alice, purgeSel, store.BulkChange{}); !errors.Is(err, store.ErrMatchChanged) {
		t.Errorf("purge with a v1 fingerprint: %v", err)
	}
	restoreSel := store.BulkSelection{Match: match("rechnung", true), Fingerprint: restoreRun.Fingerprint, Action: store.BulkRestore}
	if got, err := bulk(alice, restoreSel, store.BulkChange{}); err != nil || len(got) != 2 {
		t.Fatalf("restore by search: %v, %v", got, err)
	}
	if got := searchTitles(t, st, alice, "", true); len(got) != 0 {
		t.Errorf("trash after restore: %v", got)
	}

	// An admin's empty trash is their own trash: owner names the user.
	// By explicit IDs an admin purges across owners.
	if _, err := bulk(admin, store.BulkSelection{IDs: []string{a1.ID, b1.ID}, Action: store.BulkTrash}, store.BulkChange{}); err != nil {
		t.Fatal(err)
	}
	adminSel := store.BulkSelection{Match: match("", true), Owner: &admin.UserID, Action: store.BulkPurge}
	run, err := st.CountBulkMatches(ctx, admin, adminSel)
	if err != nil || run.Count != 0 {
		t.Fatalf("admin's own trash: %+v, %v", run, err)
	}
	adminSel.Fingerprint = run.Fingerprint
	if got, err := bulk(admin, adminSel, store.BulkChange{}); err != nil || len(got) != 0 {
		t.Errorf("empty admin trash: %v, %v", got, err)
	}
	if got, err := bulk(admin, store.BulkSelection{IDs: []string{a1.ID, b1.ID}, Action: store.BulkPurge}, store.BulkChange{}); err != nil || len(got) != 2 {
		t.Fatalf("admin purge by ids: %v, %v", got, err)
	}
	for _, d := range []*store.Document{a1, b1} {
		if _, err := st.GetDocument(ctx, admin, d.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s after purge: %v", d.Title, err)
		}
	}

	// The audit: a summary per change and an entry per document, with
	// ownerId for another user's document.
	perDoc := map[string][]string{}
	for _, r := range auditRows(t, connect(t, url)) {
		if r.TargetType != nil && *r.TargetType == store.TargetDocument {
			key := r.Action
			if r.Details["ownerId"] != nil {
				key += fmt.Sprint(" owner ", r.Details["ownerId"])
			}
			perDoc[key] = append(perDoc[key], *r.TargetID)
		}
	}
	want := map[string]int{
		"document.trash": 2, "document.restore": 2,
		fmt.Sprint("document.trash owner ", alice.UserID): 1, fmt.Sprint("document.trash owner ", bob.UserID): 1,
		fmt.Sprint("document.purge owner ", alice.UserID): 1, fmt.Sprint("document.purge owner ", bob.UserID): 1,
	}
	for k, n := range want {
		if len(perDoc[k]) != n {
			t.Errorf("%s: %v, want %d", k, perDoc[k], n)
		}
	}
	if len(perDoc) != len(want) {
		t.Errorf("entries: %v", perDoc)
	}
}

// Reprocessing skips or refuses trashed documents; a job of a trashed
// document still completes.
func TestReprocessTrash(t *testing.T) {
	st, _, clock := trashStore(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	doc := mustDoc(t, st, alice, newDoc("doc"))
	j := claim(t, st, clock.Now())
	if err := st.TrashDocument(ctx, alice, doc.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteJob(ctx, j, clock.Now(), store.ProcessResult{PageCount: 2, Text: "text", TextSource: store.TextSourceTesseract}); err != nil {
		t.Fatalf("complete the job of a trashed document: %v", err)
	}
	if d := getDoc(t, st, doc.ID); d.PageCount != 2 || d.DeletedAt == nil {
		t.Errorf("after the job: %+v", d)
	}
	if _, err := st.EnqueueDocument(ctx, doc.ID); !errors.Is(err, store.ErrInTrash) {
		t.Errorf("enqueue: %v", err)
	}
	if _, err := st.Reprocess(ctx, store.ReprocessSelection{IDs: []string{doc.ID}}); !errors.Is(err, store.ErrInTrash) {
		t.Errorf("reprocess by id: %v", err)
	}
	if n, err := st.Reprocess(ctx, store.ReprocessSelection{All: true}); err != nil || n != 0 {
		t.Errorf("reprocess --all: %d, %v", n, err)
	}
	j2, err := st.ClaimJob(ctx, clock.Now(), time.Minute)
	if err != nil || j2 != nil {
		t.Fatalf("claim: %+v, %v", j2, err)
	}
	// A failed job: --failed leaves it alone while the document is
	// trashed.
	if _, err := st.RestoreDocument(ctx, alice, doc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueDocument(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}
	j3 := claim(t, st, clock.Now())
	if err := st.FailJob(ctx, j3, clock.Now(), "broken"); err != nil {
		t.Fatal(err)
	}
	if err := st.TrashDocument(ctx, alice, doc.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := st.Reprocess(ctx, store.ReprocessSelection{Failed: true}); err != nil || n != 0 {
		t.Errorf("reprocess --failed: %d, %v", n, err)
	}
}

// The search vocabulary stays exact through trash, restore and purge:
// trashed documents keep their words until the row is deleted.
func TestTrashKeepsVocabularyExact(t *testing.T) {
	st, url, clock := trashStore(t)
	ctx := context.Background()
	conn := connect(t, url)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	in := newDoc("Stromrechnung Stadtwerke")
	in.ExtractedText = "Abschlag Zählerstand Stadtwerke"
	doc := mustDoc(t, st, alice, in)
	mustDoc(t, st, alice, newDoc("Stadtwerke Kündigung"))
	checkVocabulary(t, conn, "created")
	if err := st.TrashDocument(ctx, alice, doc.ID); err != nil {
		t.Fatal(err)
	}
	checkVocabulary(t, conn, "trashed")
	if n := vocabCount(t, conn, alice.UserID, "stadtwerk"); n != 2 {
		t.Errorf("stadtwerk in the trash: %d, want 2", n)
	}
	if _, err := st.RestoreDocument(ctx, alice, doc.ID); err != nil {
		t.Fatal(err)
	}
	checkVocabulary(t, conn, "restored")
	if err := st.TrashDocument(ctx, alice, doc.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.PurgeDocument(ctx, alice, doc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PurgeDueDocuments(ctx, clock.Now(), store.DefaultTrashRetention, 100); err != nil {
		t.Fatal(err)
	}
	checkVocabulary(t, conn, "purged")
	if n := vocabCount(t, conn, alice.UserID, "abschlag"); n != 0 {
		t.Errorf("abschlag after the purge: %d, want 0", n)
	}
}

// The trash list goes through documents_trash_idx, sorted by deletion,
// and the live lists keep their indexes: the constant trashed folds one
// branch of the filter away.
func TestTrashFilterUsesIndexes(t *testing.T) {
	st, url, clock := trashStore(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	if _, err := conn.Exec(ctx, `
		INSERT INTO documents (owner_id, title, source, original_sha256, original_size, original_mime, extracted_text, created_at,
		                       document_date, deleted_at)
		SELECT $1, 'doc ' || i, 'file', encode(sha256(i::text::bytea), 'hex'), 1, 'application/pdf',
		       CASE WHEN i % 500 = 0 THEN 'Eine Zahlungserinnerung ' ELSE 'Ein Brief ' END || repeat('Wort' || (i % 97) || ' ', 30),
		       $2::timestamptz - i * interval '1 minute', $2::timestamptz - i * interval '1 day',
		       CASE WHEN i % 100 = 50 THEN $2::timestamptz - i * interval '1 second' END
		FROM generate_series(1, 5000) i`, alice.UserID, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `ANALYZE documents`); err != nil {
		t.Fatal(err)
	}
	var recorded []struct {
		sql  string
		args []any
	}
	store.RecordSearchSQL(st, func(sql string, args []any) {
		recorded = append(recorded, struct {
			sql  string
			args []any
		}{sql, args})
	})
	for _, tc := range []struct {
		name    string
		q       string
		sort    search.Sort
		trashed bool
		index   string
		hits    int
	}{
		{"trash", "", "", true, "documents_trash_idx", 50},
		{"trash by deletion", "", search.SortDeleted, true, "documents_trash_idx", 50},
		{"live, added", "", search.SortAdded, false, "documents_owner_created_idx", 100},
		{"live, date", "", search.SortDate, false, "documents_owner_date_idx", 100},
		{"live, term", "Zahlungserinnerung", search.SortAdded, false, "documents_search_idx", 10},
	} {
		recorded = nil
		var q search.Query
		if tc.q != "" {
			q = parseQuery(t, tc.q, nil)
		}
		q.Sort, q.Trashed = tc.sort, tc.trashed
		page := runSearch(t, st, alice, q)
		if len(page.Hits) != tc.hits {
			t.Errorf("%s: %d hits, want %d", tc.name, len(page.Hits), tc.hits)
		}
		if tc.trashed {
			for i := 1; i < len(page.Hits); i++ {
				if page.Hits[i-1].DeletedAt.Before(*page.Hits[i].DeletedAt) {
					t.Fatalf("%s: not by deletion, newest first", tc.name)
				}
			}
		}
		var plans []string
		for _, r := range recorded {
			if strings.Contains(r.sql, "name: SearchTsquery") {
				continue
			}
			rows, err := conn.Query(ctx, "EXPLAIN "+r.sql, append([]any{pgx.QueryExecModeCacheDescribe}, r.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				t.Fatal(err)
			}
			plans = append(plans, strings.Join(lines, "\n"))
		}
		if len(plans) != 1 || !strings.Contains(plans[0], tc.index) {
			t.Errorf("%s: want %s in the plan:\n%s", tc.name, tc.index, strings.Join(plans, "\n---\n"))
		}
		if !tc.trashed && strings.Contains(strings.Join(plans, ""), "documents_trash_idx") {
			t.Errorf("%s: the live list uses the trash index", tc.name)
		}
	}
	// The deleted sort is the trash's only; cursors page through it.
	for _, q := range []search.Query{{Sort: search.SortDeleted}, {Sort: search.SortDeleted, Trashed: true, Ascending: true}} {
		if _, err := st.SearchDocuments(ctx, alice, q); !errors.Is(err, store.ErrInvalidSort) {
			t.Errorf("%+v: %v", q, err)
		}
	}
	first := runSearch(t, st, alice, search.Query{Trashed: true, Limit: 30})
	if first.NextCursor == "" || len(first.Hits) != 30 {
		t.Fatalf("first page: %d, %q", len(first.Hits), first.NextCursor)
	}
	second := runSearch(t, st, alice, search.Query{Trashed: true, Limit: 30, Cursor: first.NextCursor})
	if len(second.Hits) != 20 || second.NextCursor != "" || second.Hits[0].ID == first.Hits[29].ID {
		t.Errorf("second page: %d, %q", len(second.Hits), second.NextCursor)
	}
}

// The migration: up, down and up again on a database with trashed
// documents and queued removals; Down refuses after a saved-view entry
// (the log is append-only) and then works once more without one.
func TestTrashMigration(t *testing.T) {
	st, url, _ := trashStore(t)
	ctx := context.Background()
	conn := connect(t, url)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	// 00020 is not the latest migration any more: start right after it.
	if err := st.MigrateDownTo(ctx, 20); err != nil {
		t.Fatal(err)
	}
	doc := mustDoc(t, st, alice, newDoc("doc"))
	mustDoc(t, st, alice, newDoc("live"))
	if err := st.TrashDocument(ctx, alice, doc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO blob_removals (sha256, due_at) VALUES ($1, now())`, hashOf("x")); err != nil {
		t.Fatal(err)
	}
	const uuid = "0b9c6a4e-6d1f-4c39-9f6e-2b8f0e7c1a55"
	if err := insertTarget(conn, "audit_log", store.TargetSavedView, "not-a-uuid"); !checkViolation(err) {
		t.Errorf("saved view without a UUID: %v", err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || m != "00020_trash.sql" {
		t.Fatalf("down: %q, %v", m, err)
	}
	for _, sql := range []string{`SELECT deleted_at FROM documents`, `SELECT purge_at FROM documents`, `SELECT 1 FROM blob_removals`} {
		if _, err := conn.Exec(ctx, sql); err == nil {
			t.Errorf("%s works after down", sql)
		}
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM documents`).Scan(&n); err != nil || n != 2 {
		t.Errorf("documents after down: %d, %v", n, err)
	}
	if err := insertTarget(conn, "audit_log", store.TargetSavedView, uuid); !checkViolation(err) {
		t.Errorf("saved view target after down: %v", err)
	}
	if err := insertTarget(conn, "audit_log", store.TargetTag, uuid); err != nil {
		t.Errorf("tag target after down: %v", err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if c, err := st.CountDocuments(ctx, alice); err != nil || c.Total != 2 || c.Trash != 0 {
		t.Errorf("after up again: %+v, %v", c, err)
	}
	if err := insertTarget(conn, "audit_log", store.TargetSavedView, uuid); err != nil {
		t.Fatalf("saved view target: %v", err)
	}
	if err := st.MigrateDownTo(ctx, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "saved views") {
		t.Fatalf("down with a saved-view entry: %v", err)
	}
	if _, err := conn.Exec(ctx, `SELECT deleted_at FROM documents`); err != nil {
		t.Errorf("the refused down changed the schema: %v", err)
	}
}
