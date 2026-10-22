package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// A patch with only a name keeps every other stored field, also those the
// web cannot set (match text and mode, symbol).
func TestPatchKeepsOtherFields(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	parent := mustTag(t, st, alice, "Parent", nil)
	tag, err := st.CreateTag(ctx, alice, store.TagInput{
		Name: "Tax", ColorHex: "#112233", Symbol: "star", ParentID: &parent.ID, MatchText: "finanzamt", MatchMode: "exact",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.PatchTag(ctx, alice, tag.ID, store.TagPatch{Name: ptr("Steuer")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Steuer" || got.ColorHex != "#112233" || got.Symbol != "star" || got.ParentID == nil ||
		*got.ParentID != parent.ID || got.MatchText != "finanzamt" || got.MatchMode != "exact" || got.OwnerID != alice.UserID {
		t.Errorf("patched tag: %+v", got)
	}
	// Colour only, then the parent cleared.
	if got, err = st.PatchTag(ctx, alice, tag.ID, store.TagPatch{ColorHex: ptr("#AABBCC")}); err != nil ||
		got.Name != "Steuer" || got.ColorHex != "#AABBCC" || got.ParentID == nil {
		t.Errorf("colour patch: %+v, %v", got, err)
	}
	if got, err = st.PatchTag(ctx, alice, tag.ID, store.TagPatch{SetParent: true}); err != nil ||
		got.ParentID != nil || got.ColorHex != "#AABBCC" || got.MatchText != "finanzamt" {
		t.Errorf("parent patch: %+v, %v", got, err)
	}

	c, err := st.CreateCorrespondent(ctx, alice, store.CorrespondentInput{Name: "Stadtwerke", MatchText: "sw", MatchMode: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.PatchCorrespondent(ctx, alice, c.ID, store.CorrespondentPatch{Name: ptr("SWM")}); err != nil ||
		got.Name != "SWM" || got.MatchText != "sw" || got.MatchMode != "all" {
		t.Errorf("patched correspondent: %+v, %v", got, err)
	}
	dt, err := st.CreateDocumentType(ctx, alice, store.DocumentTypeInput{Name: "Invoice", Symbol: "doc", MatchText: "rechnung", MatchMode: "regex"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.PatchDocumentType(ctx, alice, dt.ID, store.DocumentTypePatch{Name: ptr("Rechnung")}); err != nil ||
		got.Name != "Rechnung" || got.Symbol != "doc" || got.MatchText != "rechnung" || got.MatchMode != "regex" {
		t.Errorf("patched document type: %+v, %v", got, err)
	}
}

func TestPatchErrors(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	admin := store.Scope{UserID: storetest.User(t, st, "root", true).ID, All: true}
	a := mustTag(t, st, alice, "A", nil)
	child := mustTag(t, st, alice, "Child", a)
	mustTag(t, st, alice, "B", nil)
	bobs := mustTag(t, st, bob, "Bob's", nil)

	cases := []struct {
		name string
		sc   store.Scope
		id   string
		p    store.TagPatch
		want error
	}{
		{"sibling name, other case", alice, a.ID, store.TagPatch{Name: ptr("b")}, store.ErrNameTaken},
		{"under its own child", alice, a.ID, store.TagPatch{SetParent: true, ParentID: &child.ID}, store.ErrTagCycle},
		{"under itself", alice, a.ID, store.TagPatch{SetParent: true, ParentID: &a.ID}, store.ErrTagCycle},
		{"parent of another owner", alice, a.ID, store.TagPatch{SetParent: true, ParentID: &bobs.ID}, store.ErrOwnerMismatch},
		{"malformed parent", alice, a.ID, store.TagPatch{SetParent: true, ParentID: ptr("x")}, store.ErrOwnerMismatch},
		{"another user's tag", bob, a.ID, store.TagPatch{Name: ptr("Mine")}, store.ErrNotFound},
		{"malformed id", alice, "x", store.TagPatch{Name: ptr("Mine")}, store.ErrNotFound},
		{"admin: parent of another owner", admin, a.ID, store.TagPatch{SetParent: true, ParentID: &bobs.ID}, store.ErrOwnerMismatch},
	}
	for _, c := range cases {
		if _, err := st.PatchTag(ctx, c.sc, c.id, c.p); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	if got, _ := st.GetTag(ctx, alice, a.ID); got.Name != "A" || got.ParentID != nil {
		t.Errorf("a refused patch changed the tag: %+v", got)
	}
	// Under a different parent the same name is fine.
	if _, err := st.PatchTag(ctx, alice, child.ID, store.TagPatch{Name: ptr("B")}); err != nil {
		t.Errorf("same name under another parent: %v", err)
	}
	// An admin renames another user's tag; the owner stays.
	got, err := st.PatchTag(ctx, admin, a.ID, store.TagPatch{Name: ptr("Renamed")})
	if err != nil || got.OwnerID != alice.UserID || got.Name != "Renamed" {
		t.Errorf("admin rename: %+v, %v", got, err)
	}
	c := mustCorrespondent(t, st, alice, "Bank")
	mustCorrespondent(t, st, alice, "Insurance")
	if _, err := st.PatchCorrespondent(ctx, alice, c.ID, store.CorrespondentPatch{Name: ptr("INSURANCE")}); !errors.Is(err, store.ErrNameTaken) {
		t.Errorf("taken correspondent name: %v", err)
	}
	if _, err := st.PatchCorrespondent(ctx, bob, c.ID, store.CorrespondentPatch{Name: ptr("x")}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's correspondent: %v", err)
	}
}

// Two patches of different fields of one tag both survive: the second one
// waits for the first one's row lock and then reads its result. Without
// the lock it would read the old row and write the old name back.
func TestConcurrentPatchesBothSurvive(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	tag := mustTag(t, st, alice, "Old", nil)
	watch := connect(t, url)

	locked, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		held := store.WithAfterLock(ctx, func() {
			close(locked)
			select { // a failing test cancels ctx, so the pool can close
			case <-release:
			case <-ctx.Done():
			}
		})
		_, err := st.PatchTag(held, alice, tag.ID, store.TagPatch{Name: ptr("New")})
		first <- err
	}()
	select {
	case <-locked:
	case err := <-first:
		t.Fatalf("first patch finished before it locked: %v", err)
	case <-ctx.Done():
		t.Fatal("first patch never locked the row")
	}

	second := make(chan error, 1)
	go func() {
		_, err := st.PatchTag(ctx, alice, tag.ID, store.TagPatch{ColorHex: ptr("#00FF00")})
		second <- err
	}()
	// Wait until the second patch waits for the row lock.
	for {
		var waiting bool
		err := watch.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%-- name: LockTag%')`).Scan(&waiting)
		if err == nil && waiting {
			break
		}
		select {
		case err := <-second:
			t.Fatalf("second patch finished without waiting for the lock: %v", err)
		case <-ctx.Done():
			t.Fatal("second patch never waited for the lock")
		default:
		}
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first patch: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second patch: %v", err)
	}
	got, err := st.GetTag(ctx, alice, tag.ID)
	if err != nil || got.Name != "New" || got.ColorHex != "#00FF00" {
		t.Fatalf("after both patches: %+v, %v; want name New and colour #00FF00", got, err)
	}
}

// Opposite moves (X under Y while Y goes under X) give one success and one
// tag_cycle, not a deadlock. With FOR UPDATE in LockTag, A's foreign-key
// check on Y (FOR KEY SHARE) waited for B's row lock while A held the
// owner's cycle-check advisory lock that B then waited for: PostgreSQL
// aborted one of them (ErrRetry, 503).
func TestOppositeTagMovesGiveCycle(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	x := mustTag(t, st, alice, "X", nil)
	y := mustTag(t, st, alice, "Y", nil)
	watch := connect(t, url)

	move := func(id, parent string, locked, release chan struct{}, done chan<- error) {
		e := &store.AuditEntry{Actor: store.ActorCLI, Action: "tag.update"}
		e.SetTarget(store.TargetTag, "", "")
		held := store.WithAfterLock(store.WithAudit(ctx, e), func() {
			close(locked)
			select { // a failing test cancels ctx, so the pool can close
			case <-release:
			case <-ctx.Done():
			}
		})
		_, err := st.PatchTag(held, alice, id, store.TagPatch{SetParent: true, ParentID: &parent})
		done <- err
	}
	lockedA, releaseA, doneA := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	lockedB, releaseB, doneB := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go move(x.ID, y.ID, lockedA, releaseA, doneA)
	go move(y.ID, x.ID, lockedB, releaseB, doneB)
	for _, l := range []struct {
		name   string
		locked chan struct{}
		done   chan error
	}{{"A", lockedA, doneA}, {"B", lockedB, doneB}} {
		select {
		case <-l.locked:
		case err := <-l.done:
			t.Fatalf("move %s finished before it locked: %v", l.name, err)
		case <-ctx.Done():
			t.Fatalf("move %s never locked its row", l.name)
		}
	}

	// Release A; it either finishes or waits for a lock in UpdateTag (Y's
	// row, the old lock mode).
	close(releaseA)
	var errA error
	finishedA := false
	for !finishedA {
		select {
		case errA = <-doneA:
			finishedA = true
			continue
		case err := <-doneB:
			t.Fatalf("move B finished before it was released: %v", err)
		case <-ctx.Done():
			t.Fatal("move A neither finished nor waited for a lock")
		default:
		}
		var waiting bool
		err := watch.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%-- name: UpdateTag%')`).Scan(&waiting)
		if err == nil && waiting {
			break
		}
	}
	close(releaseB)
	if !finishedA {
		errA = <-doneA
	}
	errB := <-doneB
	if errA != nil {
		t.Errorf("move A: %v, want success", errA)
	}
	if !errors.Is(errB, store.ErrTagCycle) {
		t.Errorf("move B: %v, want ErrTagCycle", errB)
	}
	if gx, err := st.GetTag(ctx, alice, x.ID); err != nil || gx.ParentID == nil || *gx.ParentID != y.ID {
		t.Errorf("X after the moves: %+v, %v; want parent Y", gx, err)
	}
	if gy, err := st.GetTag(ctx, alice, y.ID); err != nil || gy.ParentID != nil {
		t.Errorf("Y after the moves: %+v, %v; want a root", gy, err)
	}
	var entries []string
	rows, err := watch.Query(ctx, `SELECT target_id FROM audit_log WHERE action = 'tag.update'`)
	if err == nil {
		entries, err = pgx.CollectRows(rows, pgx.RowTo[string])
	}
	if err != nil || len(entries) != 1 || entries[0] != x.ID {
		t.Errorf("audit entries of the moves: %v, %v; want only X's", entries, err)
	}
}

// An admin may create a row for another user; nobody else may, and an
// unknown owner is refused.
func TestCreateForOwner(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := storetest.User(t, st, "bob", false)
	admin := store.Scope{UserID: storetest.User(t, st, "root", true).ID, All: true}

	tag, err := st.CreateTag(ctx, admin, store.TagInput{OwnerID: bob.ID, Name: "For Bob"})
	if err != nil || tag.OwnerID != bob.ID {
		t.Fatalf("admin creates for bob: %+v, %v", tag, err)
	}
	if c, err := st.CreateCorrespondent(ctx, admin, store.CorrespondentInput{OwnerID: bob.ID, Name: "C"}); err != nil || c.OwnerID != bob.ID {
		t.Errorf("admin creates a correspondent for bob: %+v, %v", c, err)
	}
	if d, err := st.CreateDocumentType(ctx, admin, store.DocumentTypeInput{OwnerID: bob.ID, Name: "D"}); err != nil || d.OwnerID != bob.ID {
		t.Errorf("admin creates a document type for bob: %+v, %v", d, err)
	}
	if _, err := st.CreateTag(ctx, alice, store.TagInput{OwnerID: bob.ID, Name: "Sneaky"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("non-admin creates for bob: %v", err)
	}
	if own, err := st.CreateTag(ctx, alice, store.TagInput{OwnerID: alice.UserID, Name: "Own"}); err != nil || own.OwnerID != alice.UserID {
		t.Errorf("non-admin names herself: %+v, %v", own, err)
	}
	if _, err := st.CreateTag(ctx, admin, store.TagInput{OwnerID: 999999, Name: "Nobody"}); !errors.Is(err, store.ErrUnknownUser) {
		t.Errorf("unknown owner: %v", err)
	}
	// A parent must be the new owner's.
	adminTag := mustTag(t, st, admin, "Admin's", nil)
	if _, err := st.CreateTag(ctx, admin, store.TagInput{OwnerID: bob.ID, Name: "X", ParentID: &adminTag.ID}); !errors.Is(err, store.ErrOwnerMismatch) {
		t.Errorf("parent of another owner: %v", err)
	}
}

// Counts per list: a tag's own documents and the distinct documents of its
// subtree, per scope.
func TestMetadataCounts(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	admin := store.Scope{UserID: storetest.User(t, st, "root", true).ID, All: true}
	parent := mustTag(t, st, alice, "Parent", nil)
	child := mustTag(t, st, alice, "Child", parent)
	grandchild := mustTag(t, st, alice, "Grandchild", child)
	unused := mustTag(t, st, alice, "Unused", nil)
	corr := mustCorrespondent(t, st, alice, "Bank")
	typ, err := st.CreateDocumentType(ctx, alice, store.DocumentTypeInput{Name: "Invoice"})
	if err != nil {
		t.Fatal(err)
	}
	bobTag := mustTag(t, st, bob, "Bob", nil)

	d1 := newDoc("one") // parent and child: counted once in the subtree
	d1.TagIDs = []string{parent.ID, child.ID}
	d1.CorrespondentID, d1.DocumentTypeID, d1.IsFavorite = &corr.ID, &typ.ID, true
	mustDoc(t, st, alice, d1)
	d2 := newDoc("two")
	d2.TagIDs = []string{grandchild.ID}
	d2.CorrespondentID, d2.IsInbox = &corr.ID, false
	mustDoc(t, st, alice, d2)
	mustDoc(t, st, alice, newDoc("three"))
	b1 := newDoc("bob")
	b1.TagIDs = []string{bobTag.ID}
	mustDoc(t, st, bob, b1)

	tags, err := st.ListTagsWithCounts(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]int64{parent.ID: {1, 2}, child.ID: {1, 2}, grandchild.ID: {1, 1}, unused.ID: {0, 0}}
	if len(tags) != len(want) {
		t.Fatalf("alice's tags: %d, want %d", len(tags), len(want))
	}
	for _, tc := range tags {
		if w := want[tc.ID]; tc.DocumentCount != w[0] || tc.SubtreeCount != w[1] {
			t.Errorf("tag %s: own %d, subtree %d; want %v", tc.Name, tc.DocumentCount, tc.SubtreeCount, w)
		}
	}
	if all, err := st.ListTagsWithCounts(ctx, admin); err != nil || len(all) != 5 {
		t.Errorf("admin's tags: %d, %v", len(all), err)
	}
	cs, err := st.ListCorrespondentsWithCounts(ctx, alice)
	if err != nil || len(cs) != 1 || cs[0].DocumentCount != 2 {
		t.Errorf("correspondents: %+v, %v", cs, err)
	}
	ts, err := st.ListDocumentTypesWithCounts(ctx, alice)
	if err != nil || len(ts) != 1 || ts[0].DocumentCount != 1 {
		t.Errorf("document types: %+v, %v", ts, err)
	}
	if got, err := st.CountDocuments(ctx, alice); err != nil || got != (store.DocumentCounts{Inbox: 2, Favorites: 1, Total: 3}) {
		t.Errorf("alice's counts: %+v, %v", got, err)
	}
	if got, err := st.CountDocuments(ctx, admin); err != nil || got != (store.DocumentCounts{Inbox: 3, Favorites: 1, Total: 4}) {
		t.Errorf("admin's counts: %+v, %v", got, err)
	}
}

// A metadata delete that PostgreSQL aborts as a deadlock victim is tried
// again; after deadlockAttempts aborted attempts the error is ErrRetry and
// nothing changed. A test trigger raises the deadlock on the first n
// attempts; the sequence it counts with is not rolled back.
func TestMetadataDeleteRetriesDeadlock(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	conn := connect(t, url)
	setup := func(failures int) {
		t.Helper()
		if _, err := conn.Exec(ctx, `DROP TRIGGER IF EXISTS deadlock ON correspondents;
			DROP TRIGGER IF EXISTS deadlock ON tags;
			DROP SEQUENCE IF EXISTS deadlock_attempts;
			CREATE SEQUENCE deadlock_attempts;`); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `CREATE OR REPLACE FUNCTION deadlock() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				IF nextval('deadlock_attempts') <= `+strconv.Itoa(failures)+` THEN
					RAISE EXCEPTION 'test deadlock' USING ERRCODE = 'deadlock_detected';
				END IF;
				RETURN OLD;
			END $$;
			CREATE TRIGGER deadlock BEFORE DELETE ON correspondents FOR EACH ROW EXECUTE FUNCTION deadlock();
			CREATE TRIGGER deadlock BEFORE DELETE ON tags FOR EACH ROW EXECUTE FUNCTION deadlock();`); err != nil {
			t.Fatal(err)
		}
	}
	attempts := func() int64 {
		t.Helper()
		var n int64
		if err := conn.QueryRow(ctx, `SELECT last_value FROM deadlock_attempts`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	setup(2)
	c := mustCorrespondent(t, st, alice, "Bank")
	e := &store.AuditEntry{Actor: store.ActorCLI, Action: "correspondent.delete"}
	e.SetTarget(store.TargetCorrespondent, "", "")
	if err := st.DeleteCorrespondent(store.WithAudit(ctx, e), alice, c.ID); err != nil {
		t.Fatalf("delete after two deadlocks: %v", err)
	}
	if n := attempts(); n != 3 {
		t.Errorf("%d attempts, want 3", n)
	}
	var entries int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'correspondent.delete' AND target_id = $1`, c.ID).Scan(&entries); err != nil || entries != 1 {
		t.Errorf("audit entries of the delete: %d, %v", entries, err)
	}

	setup(3)
	tag := mustTag(t, st, alice, "Tax", nil)
	if err := st.DeleteTag(ctx, alice, tag.ID); !errors.Is(err, store.ErrRetry) {
		t.Fatalf("delete with deadlocks on every attempt: %v, want ErrRetry", err)
	}
	if n := attempts(); n != 3 {
		t.Errorf("%d attempts, want 3", n)
	}
	if _, err := st.GetTag(ctx, alice, tag.ID); err != nil {
		t.Errorf("tag gone after ErrRetry: %v", err)
	}
}

// A metadata delete locks the documents linked to its row before it
// deletes: held there (WithAfterLock), a bulk change of those documents
// that links nothing waits for it. After the release both succeed with
// one attempt each. Before #99 the delete had no such point, and its
// cascade or SET NULL locked the documents in the plan's order.
func TestMetadataDeleteLocksDocumentsFirst(t *testing.T) {
	for _, kind := range []string{"tag", "correspondent", "documentType"} {
		t.Run(kind, func(t *testing.T) {
			st, dbURL := storetest.NewWithURL(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
			var id string
			var del func(context.Context, *store.Store) error
			var link func(in *store.NewDocument)
			switch kind {
			case "tag":
				id = mustTag(t, st, alice, "T", nil).ID
				link = func(in *store.NewDocument) { in.TagIDs = []string{id} }
				del = func(ctx context.Context, s *store.Store) error { return s.DeleteTag(ctx, alice, id) }
			case "correspondent":
				id = mustCorrespondent(t, st, alice, "C").ID
				link = func(in *store.NewDocument) { in.CorrespondentID = &id }
				del = func(ctx context.Context, s *store.Store) error { return s.DeleteCorrespondent(ctx, alice, id) }
			default:
				id = mustDocType(t, st, alice, "Y").ID
				link = func(in *store.NewDocument) { in.DocumentTypeID = &id }
				del = func(ctx context.Context, s *store.Store) error { return s.DeleteDocumentType(ctx, alice, id) }
			}
			var docs []string
			for _, name := range []string{"d1", "d2"} {
				in := newDoc(name)
				link(&in)
				docs = append(docs, mustDoc(t, st, alice, in).ID)
			}
			deleter, bulkStore := lockOrderStores(t, dbURL, "deleter", "bulk")
			conn := connect(t, dbURL)

			h := holdAfterLock(ctx, t, "delete", func(ctx context.Context) error { return del(ctx, deleter) })
			n, changed := countAfterLock(ctx, func(ctx context.Context) error {
				_, err := bulkStore.BulkUpdateDocuments(ctx, alice, store.BulkSelection{IDs: docs}, store.BulkChange{IsFavorite: ptr(true)}, "document.update")
				return err
			})
			waitForLock(ctx, t, conn, "bulk", changed)
			close(h.release)
			if err := <-h.done; err != nil || h.attempts.Load() != 1 {
				t.Errorf("delete: %v after %d attempts, want success after 1", err, h.attempts.Load())
			}
			if err := <-changed; err != nil || n.Load() != 1 {
				t.Errorf("bulk change: %v after %d attempts, want success after 1", err, n.Load())
			}
			for _, d := range docs {
				if g := getDoc(t, st, d); !g.IsFavorite || len(g.TagIDs) != 0 || g.CorrespondentID != nil || g.DocumentTypeID != nil {
					t.Errorf("after both: %+v", g)
				}
			}
		})
	}
}

// Deletes of a tag P and of its child C on a document with both do not
// deadlock: P's delete locks its direct children before its documents, so
// C's delete, holding C, never waits for documents P's delete holds. Held
// after its locks, P's delete makes C's wait for C; after the release both
// succeed with one attempt each. Without the children's lock C's delete
// held C and waited for the document, and P's SET NULL (parent_id) waited
// for C: a deadlock, and a second attempt of P's delete (10 ms).
func TestDeleteParentAndChildTagNoDeadlock(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	p := mustTag(t, st, alice, "P", nil)
	c := mustTag(t, st, alice, "C", p)
	in := newDoc("doc")
	in.TagIDs = []string{p.ID, c.ID}
	doc := mustDoc(t, st, alice, in)
	childStore, parentStore := lockOrderStores(t, dbURL, "child", "parent")
	conn := connect(t, dbURL)

	h := holdAfterLock(ctx, t, "parent's delete", func(ctx context.Context) error { return parentStore.DeleteTag(ctx, alice, p.ID) })
	n, deleted := countAfterLock(ctx, func(ctx context.Context) error { return childStore.DeleteTag(ctx, alice, c.ID) })
	waitForLock(ctx, t, conn, "child", deleted)
	close(h.release)
	if err := <-h.done; err != nil || h.attempts.Load() != 1 {
		t.Errorf("parent's delete: %v after %d attempts, want success after 1", err, h.attempts.Load())
	}
	if err := <-deleted; err != nil || n.Load() != 1 {
		t.Errorf("child's delete: %v after %d attempts, want success after 1", err, n.Load())
	}
	for _, tag := range []*store.Tag{p, c} {
		if _, err := st.GetTag(ctx, alice, tag.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("tag %s after the deletes: %v", tag.Name, err)
		}
	}
	if g := getDoc(t, st, doc.ID); len(g.TagIDs) != 0 {
		t.Errorf("document after the deletes: %+v", g)
	}
}

// The metadata audit entries name the row and its owner when that is not
// the acting user, and record a rename's old name.
func TestMetadataAuditDetails(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	admin := store.Scope{UserID: storetest.User(t, st, "root", true).ID, All: true}
	conn := connect(t, url)
	entry := func(action string) *store.AuditEntry {
		e := &store.AuditEntry{Actor: store.ActorCLI, Action: action}
		e.SetTarget(store.TargetTag, "", "")
		return e
	}
	tag, err := st.CreateTag(store.WithAudit(ctx, entry("tag.create")), alice, store.TagInput{Name: "Old"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PatchTag(store.WithAudit(ctx, entry("tag.update")), admin, tag.ID, store.TagPatch{Name: ptr("New")}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteTag(store.WithAudit(ctx, entry("tag.delete")), admin, tag.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, `SELECT action, target_type, target_id, target_name, details FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	type rec struct {
		Action, Type, ID, Name string
		Details                []byte
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (rec, error) {
		var x rec
		return x, r.Scan(&x.Action, &x.Type, &x.ID, &x.Name, &x.Details)
	})
	if err != nil || len(got) != 3 {
		t.Fatalf("entries: %+v, %v", got, err)
	}
	want := []struct {
		action, name string
		details      map[string]any
	}{
		{"tag.create", "Old", map[string]any{"name": "Old"}},
		{"tag.update", "New", map[string]any{"name": "New", "oldName": "Old", "ownerId": float64(alice.UserID)}},
		{"tag.delete", "New", map[string]any{"name": "New", "ownerId": float64(alice.UserID)}},
	}
	for i, w := range want {
		g := got[i]
		var d map[string]any
		_ = json.Unmarshal(g.Details, &d)
		if g.Action != w.action || g.Type != store.TargetTag || g.ID != tag.ID || g.Name != w.name || len(d) != len(w.details) {
			t.Errorf("entry %d: %+v %v", i, g, d)
			continue
		}
		for k, v := range w.details {
			if d[k] != v {
				t.Errorf("entry %d: %s = %v, want %v", i, k, d[k], v)
			}
		}
	}
}

const metadataTargetsVersion = 16

// insertTarget writes an audit row with the given target straight into
// table (audit_log or one partition).
func insertTarget(conn *pgx.Conn, table, typ, id string) error {
	_, err := conn.Exec(context.Background(), `INSERT INTO `+pgx.Identifier{table}.Sanitize()+
		` (at, actor, action, target_type, target_id) VALUES (now(), 'cli', 'test.target', $1, $2)`, typ, id)
	return err
}

func checkViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

// Migration 00016 allows the metadata targets, as UUIDs only, on every
// partition, also on those created later; Down refuses while such entries
// exist and otherwise restores the old checks; Up works again after it.
func TestAuditMetadataTargetsMigration(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn := connect(t, url)
	const uuid = "0b9c6a4e-6d1f-4c39-9f6e-2b8f0e7c1a55"

	checkUp := func(stage string) {
		t.Helper()
		for _, typ := range []string{store.TargetTag, store.TargetCorrespondent, store.TargetDocumentType, store.TargetDocument} {
			if err := insertTarget(conn, "audit_log", typ, uuid); err != nil {
				t.Errorf("%s: %s with a UUID: %v", stage, typ, err)
			}
			if err := insertTarget(conn, "audit_log", typ, "not-a-uuid"); !checkViolation(err) {
				t.Errorf("%s: %s without a UUID: %v", stage, typ, err)
			}
		}
		if err := insertTarget(conn, "audit_log", "folder", uuid); !checkViolation(err) {
			t.Errorf("%s: unknown target type: %v", stage, err)
		}
		if err := insertTarget(conn, "audit_log", store.TargetUser, "42"); err != nil {
			t.Errorf("%s: user target: %v", stage, err)
		}
	}
	checkUp("after up")

	// A partition created after the migration has the checks too.
	if _, err := st.EnsureAuditPartitions(ctx, 6); err != nil {
		t.Fatal(err)
	}
	parts := partitions(t, conn)
	newest := parts[len(parts)-1]
	var at time.Time
	if err := conn.QueryRow(ctx, `SELECT (date_trunc('month', now() AT TIME ZONE 'UTC') + interval '6 months') AT TIME ZONE 'UTC'`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	ins := func(typ, id string) error {
		_, err := conn.Exec(ctx, `INSERT INTO `+pgx.Identifier{newest}.Sanitize()+
			` (at, actor, action, target_type, target_id) VALUES ($1, 'cli', 'test.target', $2, $3)`, at, typ, id)
		return err
	}
	if err := ins(store.TargetTag, "not-a-uuid"); !checkViolation(err) {
		t.Errorf("new partition %s: tag without a UUID: %v", newest, err)
	}
	if err := ins(store.TargetTag, uuid); err != nil {
		t.Errorf("new partition %s: tag with a UUID: %v", newest, err)
	}

	// Down refuses: the log has metadata entries.
	if err := st.MigrateDownTo(ctx, metadataTargetsVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "tags, correspondents or document types") {
		t.Fatalf("down with metadata entries: %v", err)
	}

	// Without such entries: Down restores the old checks, Up works again.
	st2, url2 := storetest.NewWithURL(t)
	conn2 := connect(t, url2)
	if err := insertTarget(conn2, "audit_log", store.TargetDocument, uuid); err != nil {
		t.Fatal(err)
	}
	if err := st2.MigrateDownTo(ctx, metadataTargetsVersion); err != nil {
		t.Fatal(err)
	}
	if m, err := st2.MigrateDown(ctx); err != nil || m != "00016_audit_metadata_targets.sql" {
		t.Fatalf("down: %q, %v", m, err)
	}
	if err := insertTarget(conn2, "audit_log", store.TargetTag, uuid); !checkViolation(err) {
		t.Errorf("after down: tag target: %v", err)
	}
	if err := insertTarget(conn2, "audit_log", store.TargetDocument, "not-a-uuid"); !checkViolation(err) {
		t.Errorf("after down: document without a UUID: %v", err)
	}
	if err := insertTarget(conn2, "audit_log", store.TargetDocument, uuid); err != nil {
		t.Errorf("after down: document with a UUID: %v", err)
	}
	if _, err := st2.Migrate(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	conn = conn2
	checkUp("after down and up")
}

// The migration stops when the checks it replaces cannot be found exactly
// once, instead of dropping the wrong one.
func TestAuditMetadataTargetsMigrationNeedsTheChecks(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn := connect(t, url)
	if err := st.MigrateDownTo(ctx, metadataTargetsVersion-1); err != nil {
		t.Fatal(err)
	}
	// A second UUID check on document targets makes the lookup ambiguous.
	if _, err := conn.Exec(ctx, `ALTER TABLE audit_log ADD CONSTRAINT extra_document_check
		CHECK (target_type <> 'document' OR length(target_id) = 36)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "expected one UUID check on document targets") {
		t.Fatalf("up with two document checks: %v", err)
	}
	if _, err := conn.Exec(ctx, `ALTER TABLE audit_log DROP CONSTRAINT extra_document_check`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
}
