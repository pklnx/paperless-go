package store_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/paperless-go/internal/search"
	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// ownerKey is the owner lock's key of owner, as PostgreSQL computes it.
func ownerKey(t *testing.T, conn *pgx.Conn, owner int64) int64 {
	t.Helper()
	var k int64
	if err := conn.QueryRow(context.Background(), `SELECT hashtextextended($1::text || $2::bigint::text, 0)`,
		store.OwnerLockPrefix, owner).Scan(&k); err != nil {
		t.Fatal(err)
	}
	return k
}

// advisoryLock is one advisory lock in pg_locks.
type advisoryLock struct {
	key     int64
	granted bool
}

// advisoryLocks lists the advisory locks of the backends of application
// app (or of the backend pid when app is "").
func advisoryLocks(t *testing.T, conn *pgx.Conn, app string, pid int) []advisoryLock {
	t.Helper()
	rows, err := conn.Query(context.Background(), `SELECT l.classid::bigint, l.objid::bigint, l.objsubid, l.granted
		FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
		WHERE l.locktype = 'advisory' AND a.datname = current_database()
		  AND (a.application_name = $1 OR a.pid = $2)
		ORDER BY l.granted DESC`, app, pid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []advisoryLock
	for rows.Next() {
		var hi, lo int64
		var sub int16
		var l advisoryLock
		if err := rows.Scan(&hi, &lo, &sub, &l.granted); err != nil {
			t.Fatal(err)
		}
		if sub != 1 { // 1: a single bigint key
			t.Errorf("advisory lock with objsubid %d, want a bigint key", sub)
		}
		l.key = int64(uint64(hi)<<32 | uint64(lo))
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// The store's owner lock and the tags_no_cycle trigger's lock are the
// same lock: a transaction that takes the store's key and then moves a tag
// (the trigger takes its key) holds one advisory lock, the store's key; a
// store writer held after its locks holds exactly that one too.
func TestOwnerLockMatchesTrigger(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	a, b := mustTag(t, st, alice, "A", nil), mustTag(t, st, alice, "B", nil)
	doc := mustDoc(t, st, alice, newDoc("doc"))
	conn := connect(t, dbURL)
	watch := connect(t, dbURL)
	key := ownerKey(t, conn, alice.UserID)

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var pid int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text || $2::bigint::text, 0))`,
		store.OwnerLockPrefix, alice.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE tags SET parent_id = $1 WHERE id = $2`, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if got := advisoryLocks(t, watch, "-", pid); len(got) != 1 || got[0] != (advisoryLock{key, true}) {
		t.Errorf("advisory locks after the store's key and a tag move: %+v, want one, %d", got, key)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	held := openWith(t, dbURL, map[string]string{"application_name": "held"})
	h := holdAfterLock(ctx, t, "patch", func(ctx context.Context) error {
		_, err := held.PatchDocument(ctx, alice, doc.ID, store.DocumentPatch{Title: ptr("held")})
		return err
	})
	if got := advisoryLocks(t, watch, "held", 0); len(got) != 1 || got[0] != (advisoryLock{key, true}) {
		t.Errorf("advisory locks of a held patch: %+v, want one, %d", got, key)
	}
	close(h.release)
	if err := <-h.done; err != nil {
		t.Fatal(err)
	}
}

// The guard refuses a writer of an owner table without the owner lock, in
// a transaction and outside one, for sqlc queries and for unnamed SQL, and
// nothing is written; lockOwners runs once per transaction and never
// without an owner.
func TestUnlockedWriteIsRefused(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	doc := mustDoc(t, st, alice, newDoc("doc"))
	for _, inTx := range []bool{true, false} {
		if err := store.UnlockedPatch(ctx, st, doc.ID, inTx); !errors.Is(err, store.ErrUnlockedWrite) {
			t.Errorf("raw PatchDocument (in a transaction: %v): %v, want ErrUnlockedWrite", inTx, err)
		}
	}
	raw := `UPDATE documents SET title = 'raw' WHERE id = '` + doc.ID + `'`
	if err := store.GuardedExec(ctx, st, raw, nil); !errors.Is(err, store.ErrUnlockedWrite) {
		t.Errorf("raw UPDATE on the pool: %v, want ErrUnlockedWrite", err)
	}
	if err := store.GuardedExec(ctx, st, raw, [][]int64{}); !errors.Is(err, store.ErrUnlockedWrite) {
		t.Errorf("raw UPDATE in a transaction without the lock: %v, want ErrUnlockedWrite", err)
	}
	if g := getDoc(t, st, doc.ID); g.Title != "doc" {
		t.Errorf("a refused write changed the document: %+v", g)
	}
	if err := store.GuardedExec(ctx, st, `SELECT 1 FROM documents`, nil); err != nil {
		t.Errorf("a read on the pool: %v", err)
	}
	if err := store.GuardedExec(ctx, st, raw, [][]int64{{alice.UserID}}); err != nil {
		t.Errorf("raw UPDATE under the lock: %v", err)
	}
	if g := getDoc(t, st, doc.ID); g.Title != "raw" {
		t.Errorf("the write under the lock is missing: %+v", g)
	}
	if err := store.GuardedExec(ctx, st, `SELECT 1`, [][]int64{{alice.UserID}, {alice.UserID + 1}}); !errors.Is(err, store.ErrLockedTwice) {
		t.Errorf("lockOwners twice: %v, want ErrLockedTwice", err)
	}
	if err := store.GuardedExec(ctx, st, `SELECT 1`, [][]int64{{}}); !errors.Is(err, store.ErrNoOwners) {
		t.Errorf("lockOwners without owners: %v, want ErrNoOwners", err)
	}
}

// writerCase is one row of TestEveryWriterLocksItsOwner.
type writerCase struct {
	name   string
	method string  // the *store.Store method
	owners []int64 // the owner locks it must take, ascending; nil: none
	run    func(ctx context.Context) error
	err    error // the expected error, nil for success
}

// Every store writer of the five owner tables takes the owner locks of
// the rows it writes, ascending, before its first statement that needs
// them, also in an admin's scope (the row's owner, not the actor); an
// empty selection answers before it locks. Every *Store method is either
// in this table or on the list of non-writers, so a new method must be
// classified.
func TestEveryWriterLocksItsOwner(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	aliceU := storetest.User(t, st, "alice", false)
	bobU := storetest.User(t, st, "bob", false)
	rootU := storetest.User(t, st, "root", true)
	alice, bob := store.Scope{UserID: aliceU.ID}, store.Scope{UserID: bobU.ID}
	admin := store.Scope{UserID: rootU.ID, All: true}
	a, b, r := aliceU.ID, bobU.ID, rootU.ID
	if a >= b {
		t.Fatalf("user ids %d, %d not ascending", a, b)
	}
	job := mustDoc(t, st, alice, newDoc("job"))
	j := claim(t, st, time.Now())
	if j.DocumentID != job.ID {
		t.Fatalf("claimed %s, want %s", j.DocumentID, job.ID)
	}
	archived := func(name string) store.NewDocument {
		in := newDoc(name)
		in.ArchiveSHA256, in.ArchiveSize = ptr(hashOf("archive "+name)), ptr(int64(1))
		return in
	}
	ad := mustDoc(t, st, alice, archived("Rechnung alice"))
	ad2 := mustDoc(t, st, alice, archived("alice two"))
	bd := mustDoc(t, st, bob, archived("Rechnung bob"))
	bd2 := mustDoc(t, st, bob, archived("bob two"))
	ad3 := mustDoc(t, st, alice, archived("alice three"))
	ad4 := mustDoc(t, st, alice, archived("alice four"))
	bd3 := mustDoc(t, st, bob, archived("bob three"))
	atag := mustTag(t, st, alice, "AT", nil)
	btag := mustTag(t, st, bob, "BT", nil)
	acorr := mustCorrespondent(t, st, alice, "AC")
	bcorr := mustCorrespondent(t, st, bob, "BC")
	atype := mustDocType(t, st, alice, "AY")
	btype := mustDocType(t, st, bob, "BY")
	parse := func(s string) *search.Query {
		q, err := search.Parse(s, nil)
		if err != nil {
			t.Fatal(err)
		}
		return &q
	}
	bulk := func(sc store.Scope, sel store.BulkSelection) func(context.Context) error {
		return func(ctx context.Context) error {
			if sel.Match != nil {
				m, err := st.CountBulkMatches(ctx, sc, sel)
				if err != nil {
					return err
				}
				sel.Fingerprint = m.Fingerprint
			}
			_, err := st.BulkUpdateDocuments(ctx, sc, sel, store.BulkChange{IsFavorite: ptr(true)}, "document.update")
			return err
		}
	}
	two := func(f func(context.Context) (any, error)) func(context.Context) error {
		return func(ctx context.Context) error { _, err := f(ctx); return err }
	}
	cases := []writerCase{
		{"create document", "CreateDocument", []int64{a}, two(func(ctx context.Context) (any, error) {
			return st.CreateDocument(ctx, alice, newDoc("new"))
		}), nil},
		{"admin creates own document", "CreateDocument", []int64{r}, two(func(ctx context.Context) (any, error) {
			return st.CreateDocument(ctx, admin, newDoc("admin's"))
		}), nil},
		{"patch document", "PatchDocument", []int64{a}, two(func(ctx context.Context) (any, error) {
			return st.PatchDocument(ctx, alice, ad.ID, store.DocumentPatch{Notes: ptr("x"), SetTags: true, TagIDs: []string{atag.ID}})
		}), nil},
		{"admin patches bob's document", "PatchDocument", []int64{b}, two(func(ctx context.Context) (any, error) {
			return st.PatchDocument(ctx, admin, bd.ID, store.DocumentPatch{Notes: ptr("x")})
		}), nil},
		{"patch out of scope", "PatchDocument", nil, two(func(ctx context.Context) (any, error) {
			return st.PatchDocument(ctx, alice, bd.ID, store.DocumentPatch{Title: ptr("x")})
		}), store.ErrNotFound},
		{"bulk by ids", "BulkUpdateDocuments", []int64{a}, bulk(alice, store.BulkSelection{IDs: []string{ad.ID, ad2.ID}}), nil},
		{"admin bulk by ids over two owners", "BulkUpdateDocuments", []int64{a, b}, bulk(admin, store.BulkSelection{IDs: []string{bd.ID, ad.ID}}), nil},
		{"bulk by ids out of scope", "BulkUpdateDocuments", nil, bulk(alice, store.BulkSelection{IDs: []string{bd.ID}}), store.ErrNotFound},
		{"bulk by match", "BulkUpdateDocuments", []int64{a}, bulk(alice, store.BulkSelection{Match: parse("rechnung")}), nil},
		{"admin bulk by match over all owners", "BulkUpdateDocuments", []int64{a, b}, bulk(admin, store.BulkSelection{Match: parse("rechnung")}), nil},
		{"admin bulk by match, owner=bob", "BulkUpdateDocuments", []int64{b}, bulk(admin, store.BulkSelection{Match: parse("rechnung"), Owner: &b}), nil},
		{"admin bulk by match without matches", "BulkUpdateDocuments", nil, bulk(admin, store.BulkSelection{Match: parse("nichtda")}), nil},
		{"bulk by match without matches", "BulkUpdateDocuments", []int64{a}, bulk(alice, store.BulkSelection{Match: parse("nichtda")}), nil},
		{"create correspondent", "CreateCorrespondent", []int64{a}, two(func(ctx context.Context) (any, error) {
			return st.CreateCorrespondent(ctx, alice, store.CorrespondentInput{Name: "New C"})
		}), nil},
		{"admin creates a correspondent for bob", "CreateCorrespondent", []int64{b}, two(func(ctx context.Context) (any, error) {
			return st.CreateCorrespondent(ctx, admin, store.CorrespondentInput{OwnerID: b, Name: "For bob"})
		}), nil},
		{"update correspondent", "UpdateCorrespondent", []int64{a}, two(func(ctx context.Context) (any, error) {
			return st.UpdateCorrespondent(ctx, alice, acorr.ID, store.CorrespondentInput{Name: "AC2"})
		}), nil},
		{"admin patches bob's correspondent", "PatchCorrespondent", []int64{b}, two(func(ctx context.Context) (any, error) {
			return st.PatchCorrespondent(ctx, admin, bcorr.ID, store.CorrespondentPatch{Name: ptr("BC2")})
		}), nil},
		{"delete correspondent out of scope", "DeleteCorrespondent", nil, func(ctx context.Context) error {
			return st.DeleteCorrespondent(ctx, alice, bcorr.ID)
		}, store.ErrNotFound},
		{"admin deletes bob's correspondent", "DeleteCorrespondent", []int64{b}, func(ctx context.Context) error {
			return st.DeleteCorrespondent(ctx, admin, bcorr.ID)
		}, nil},
		{"create document type", "CreateDocumentType", []int64{a}, two(func(ctx context.Context) (any, error) {
			return st.CreateDocumentType(ctx, alice, store.DocumentTypeInput{Name: "New Y"})
		}), nil},
		{"update document type", "UpdateDocumentType", []int64{a}, two(func(ctx context.Context) (any, error) {
			return st.UpdateDocumentType(ctx, alice, atype.ID, store.DocumentTypeInput{Name: "AY2"})
		}), nil},
		{"patch document type", "PatchDocumentType", []int64{a}, two(func(ctx context.Context) (any, error) {
			return st.PatchDocumentType(ctx, alice, atype.ID, store.DocumentTypePatch{Name: ptr("AY3")})
		}), nil},
		{"admin deletes bob's document type", "DeleteDocumentType", []int64{b}, func(ctx context.Context) error {
			return st.DeleteDocumentType(ctx, admin, btype.ID)
		}, nil},
		{"create tag", "CreateTag", []int64{a}, two(func(ctx context.Context) (any, error) {
			return st.CreateTag(ctx, alice, store.TagInput{Name: "Sub", ParentID: &atag.ID})
		}), nil},
		{"admin creates a tag for bob", "CreateTag", []int64{b}, two(func(ctx context.Context) (any, error) {
			return st.CreateTag(ctx, admin, store.TagInput{OwnerID: b, Name: "For bob"})
		}), nil},
		{"update tag", "UpdateTag", []int64{a}, two(func(ctx context.Context) (any, error) {
			return st.UpdateTag(ctx, alice, atag.ID, store.TagInput{Name: "AT2"})
		}), nil},
		{"admin patches alice's tag", "PatchTag", []int64{a}, two(func(ctx context.Context) (any, error) {
			return st.PatchTag(ctx, admin, atag.ID, store.TagPatch{Name: ptr("AT3")})
		}), nil},
		{"admin deletes bob's tag", "DeleteTag", []int64{b}, func(ctx context.Context) error {
			return st.DeleteTag(ctx, admin, btag.ID)
		}, nil},
		{"complete job", "CompleteJob", []int64{a}, func(ctx context.Context) error {
			return st.CompleteJob(ctx, j, time.Now(), store.ProcessResult{PageCount: 1, Text: "t", TextSource: store.TextSourceTesseract,
				ArchiveSHA256: hashOf("job archive"), ArchiveSize: 1})
		}, nil},
		{"trash document", "TrashDocument", []int64{a}, func(ctx context.Context) error {
			return st.TrashDocument(ctx, alice, ad3.ID)
		}, nil},
		{"admin trashes bob's document", "TrashDocument", []int64{b}, func(ctx context.Context) error {
			return st.TrashDocument(ctx, admin, bd3.ID)
		}, nil},
		{"trash out of scope", "TrashDocument", nil, func(ctx context.Context) error {
			return st.TrashDocument(ctx, alice, bd2.ID)
		}, store.ErrNotFound},
		{"restore document", "RestoreDocument", []int64{a}, two(func(ctx context.Context) (any, error) {
			return st.RestoreDocument(ctx, alice, ad3.ID)
		}), nil},
		{"admin restores bob's document", "RestoreDocument", []int64{b}, two(func(ctx context.Context) (any, error) {
			return st.RestoreDocument(ctx, admin, bd3.ID)
		}), nil},
		{"bulk trash by ids", "BulkUpdateDocuments", []int64{a}, func(ctx context.Context) error {
			_, err := st.BulkUpdateDocuments(ctx, alice, store.BulkSelection{IDs: []string{ad3.ID, ad4.ID}, Action: store.BulkTrash},
				store.BulkChange{}, "document.delete")
			return err
		}, nil},
		{"purge document", "PurgeDocument", []int64{a}, func(ctx context.Context) error {
			return st.PurgeDocument(ctx, alice, ad3.ID)
		}, nil},
		{"purge due documents", "PurgeDueDocuments", []int64{a}, func(ctx context.Context) error {
			res, err := st.PurgeDueDocuments(ctx, time.Now(), store.DefaultTrashRetention, 100)
			if err == nil && res.Purged != 1 {
				err = fmt.Errorf("purged %d, want 1", res.Purged)
			}
			return err
		}, nil},
		{"delete document", "DeleteDocument", []int64{a}, func(ctx context.Context) error {
			return st.DeleteDocument(ctx, alice, ad2.ID)
		}, nil},
		{"admin deletes bob's document", "DeleteDocument", []int64{b}, func(ctx context.Context) error {
			return st.DeleteDocument(ctx, admin, bd2.ID)
		}, nil},
	}
	writers := map[string]bool{}
	for _, c := range cases {
		writers[c.method] = true
		var events []store.LockEvent
		err := c.run(store.WithLockRecorder(ctx, func(e store.LockEvent) { events = append(events, e) }))
		if !errors.Is(err, c.err) || (c.err == nil) != (err == nil) {
			t.Errorf("%s: %v, want %v", c.name, err, c.err)
			continue
		}
		var locks, gates [][]int64
		writes := 0
		for _, e := range events {
			if e.Gated != nil {
				gates = append(gates, e.Gated)
				continue
			}
			if e.Owners != nil {
				locks = append(locks, e.Owners)
				continue
			}
			if e.Writes {
				writes++
				if !e.Locked {
					t.Errorf("%s: %s ran without the owner lock", c.name, e.Query)
				}
			}
		}
		if !reflect.DeepEqual(gates, locks) {
			t.Errorf("%s: gated %v, locked %v; want the same owners", c.name, gates, locks)
		}
		switch {
		case c.owners == nil && len(locks) != 0:
			t.Errorf("%s: locked %v, want no lock", c.name, locks)
		case c.owners != nil && (len(locks) != 1 || !slices.Equal(locks[0], c.owners)):
			t.Errorf("%s: locked %v, want %v once", c.name, locks, c.owners)
		case c.owners != nil && c.err == nil && writes == 0 && !strings.HasSuffix(c.name, "without matches"):
			t.Errorf("%s: no write recorded", c.name)
		}
	}

	nonWriters := map[string]bool{}
	for _, m := range []string{
		"APIKeyCounts", "APIKeyLogin", "AddDeadline", "Audit", "BeginPasskeyLogin", "BeginPasskeyRegistration",
		"BeginTwoFactorSetup", "ChangeOwnPassword", "CheckNewAPIKey", "CheckServerVersion", "ClaimJob", "Close",
		"CollationNeedsRebuild", "CompletePasskeyLogin", "CompleteTwoFactorLogin", "ConfirmTwoFactorSetup",
		"CountBulkMatches", "CountDocuments", "CountUsers", "CreateAPIKey", "CreateSavedView", "CreateSession",
		"CreateTwoFactorChallenge", "CreateUser", "CreateUserWithGeneratedPassword", "DeleteDeadline",
		"DeleteDoneJobs", "DeleteExpiredSessions", "DeleteExpiredTwoFactorChallenges",
		"DeleteExpiredWebAuthnCeremonies", "DeleteOldAPIKeys", "DeleteSavedView", "DeleteSession", "DeleteUser",
		"DisableOwnTwoFactor", "DocumentJobs", "DropPasskeyLogins", "EndSession", "EnqueueDocument",
		"EnsureAuditPartitions", "ExportAudit", "ExtendJobLease", "FailJob", "FindDocumentByOriginal",
		"FindRefusedAPIKey", "FinishPasskeyRegistration", "GetCorrespondent", "GetDocument", "GetDocumentType",
		"GetJob", "GetSavedView", "GetSession", "GetTag", "GetTwoFactorState", "GetUserByID", "GetUserByName",
		"Installation", "JobCounts", "LatestJobs", "ListAPIKeys", "ListAllAPIKeys", "ListAudit",
		"ListCorrespondents", "ListCorrespondentsWithCounts", "ListDeadlines", "ListDocumentTypes",
		"ListDocumentTypesWithCounts", "ListDocuments", "ListPasskeys", "ListSavedViews", "ListTags",
		"ListTagsWithCounts", "ListUsers", "ListenJobs", "Migrate", "MigrateAndRebuild", "MigrateDown",
		"MigrateDownTo", "MigrationStatus", "OwnedRowCounts", "OwnerGateWaiters", "PasskeyCounts", "PatchSavedView", "Ping", "ReapJobs",
		"RehashUserPassword", "ReleaseJob", "RemoveOwnPasskey", "RemovePasskeys", "ReplaceRecoveryCodes",
		"Reprocess", "ResetTwoFactor", "ResetUserPassword", "RetryJob", "RevokeAPIKey", "RevokeUserAPIKeys",
		"SchemaVersions", "SearchDocuments", "SetUserAdmin", "SetUserLocked", "SetUserPassword", "StartSession",
		"TouchAPIKey", "TouchSession", "TwoFactorChallengeUser", "UpdateDeadline", "UpdateSavedView",
		"VerifyTwoFactorCode",
	} {
		nonWriters[m] = true
	}
	typ := reflect.TypeFor[*store.Store]()
	var unclassified []string
	for i := range typ.NumMethod() {
		m := typ.Method(i).Name
		if writers[m] == nonWriters[m] {
			unclassified = append(unclassified, m)
		}
	}
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Errorf("methods neither in the writer table nor on the list of non-writers (or on both): %v", unclassified)
	}
}

// startOp runs op in the background and returns its result channel.
func startOp(op func() error) chan error {
	done := make(chan error, 1)
	go func() { done <- op() }()
	return done
}

// waitForAdvisory returns once n statements of application app wait for
// an advisory lock, or fails when one of dones delivers first or ctx ends.
func waitForAdvisory(ctx context.Context, t *testing.T, conn *pgx.Conn, app string, n int, dones ...chan error) {
	t.Helper()
	for {
		var waiting int
		err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND application_name = $1
			  AND wait_event_type = 'Lock' AND wait_event = 'advisory'`, app).Scan(&waiting)
		if err == nil && waiting >= n {
			return
		}
		for i, d := range dones {
			select {
			case err := <-d:
				t.Fatalf("%s: operation %d finished without waiting for the owner lock: %v", app, i, err)
			default:
			}
		}
		if ctx.Err() != nil {
			t.Fatalf("%s: %d of %d never waited for the owner lock", app, waiting, n)
		}
	}
}

// waitForGate returns once n writers wait at st's owner gate, or fails
// when one of dones delivers first or ctx ends.
func waitForGate(ctx context.Context, t *testing.T, st *store.Store, n int, dones ...chan error) {
	t.Helper()
	for st.OwnerGateWaiters() < n {
		for i, d := range dones {
			select {
			case err := <-d:
				t.Fatalf("operation %d finished without waiting at the gate: %v", i, err)
			default:
			}
		}
		if ctx.Err() != nil {
			t.Fatalf("%d of %d never waited at the gate", st.OwnerGateWaiters(), n)
		}
		runtime.Gosched()
	}
}

// One owner's writers serialise: while alice's bulk change holds her lock,
// her patch, tag delete, tag create, upload and OCR completion wait for it
// (one of them at the advisory lock, the others at her store's owner
// gate, without a connection); bob's same calls go through without any
// lock wait. After the release everything succeeds.
func TestWritersOfOneOwnerSerialise(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := connect(t, dbURL)
	type user struct {
		sc    store.Scope
		job   *store.Job
		doc   *store.Document
		tag   *store.Tag
		store *store.Store
		app   string
	}
	var users []*user
	for _, name := range []string{"alice", "bob"} {
		u := &user{sc: store.Scope{UserID: storetest.User(t, st, name, false).ID}, app: name + "-ops"}
		jobDoc := mustDoc(t, st, u.sc, newDoc(name+" job"))
		u.job = claim(t, st, time.Now())
		if u.job.DocumentID != jobDoc.ID {
			t.Fatalf("claimed %s, want %s", u.job.DocumentID, jobDoc.ID)
		}
		in := newDoc(name + " doc")
		in.ArchiveSHA256, in.ArchiveSize = ptr(hashOf(name+" archive")), ptr(int64(1))
		u.doc = mustDoc(t, st, u.sc, in)
		u.tag = mustTag(t, st, u.sc, name+" tag", nil)
		u.store = openWith(t, dbURL, map[string]string{"application_name": u.app, "pool_max_conns": "10"})
		users = append(users, u)
	}
	alice, bob := users[0], users[1]
	ops := func(u *user) []func() error {
		return []func() error{
			func() error {
				_, err := u.store.PatchDocument(ctx, u.sc, u.doc.ID, store.DocumentPatch{Title: ptr("patched")})
				return err
			},
			func() error { return u.store.DeleteTag(ctx, u.sc, u.tag.ID) },
			func() error {
				_, err := u.store.CreateTag(ctx, u.sc, store.TagInput{Name: "new"})
				return err
			},
			func() error {
				_, err := u.store.CreateDocument(ctx, u.sc, newDoc(u.app+" upload"))
				return err
			},
			func() error {
				return u.store.CompleteJob(ctx, u.job, time.Now(), store.ProcessResult{PageCount: 1, Text: "t",
					TextSource: store.TextSourceTesseract, ArchiveSHA256: hashOf(u.app + " ocr"), ArchiveSize: 1})
			},
		}
	}

	h := holdAfterLock(ctx, t, "alice's bulk change", func(ctx context.Context) error {
		_, err := st.BulkUpdateDocuments(ctx, alice.sc, store.BulkSelection{IDs: []string{alice.doc.ID}},
			store.BulkChange{IsFavorite: ptr(true)}, "document.update")
		return err
	})
	var waiting []chan error
	for _, op := range ops(alice) {
		waiting = append(waiting, startOp(op))
	}
	waitForAdvisory(ctx, t, conn, alice.app, 1, waiting...)
	waitForGate(ctx, t, alice.store, len(waiting)-1, waiting...)
	for i, op := range ops(bob) {
		if err := finishesWithoutLockWait(ctx, t, conn, bob.app, startOp(op)); err != nil {
			t.Errorf("bob's operation %d: %v", i, err)
		}
	}
	close(h.release)
	if err := <-h.done; err != nil {
		t.Fatalf("alice's bulk change: %v", err)
	}
	for i, d := range waiting {
		if err := <-d; err != nil {
			t.Errorf("alice's operation %d: %v", i, err)
		}
	}
	if g := getDoc(t, st, alice.doc.ID); !g.IsFavorite || g.Title != "patched" {
		t.Errorf("alice's document: %+v", g)
	}
}

// An admin's change over two owners takes their locks in ascending id:
// while bob's bulk change holds bob's lock, the admin's bulk change over
// alice's and bob's documents holds alice's lock and waits for bob's, and
// alice's own patch, started meanwhile, waits for the admin's. After the
// release all three succeed.
func TestSeveralOwnersLockAscending(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := connect(t, dbURL)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	admin := store.Scope{UserID: storetest.User(t, st, "root", true).ID, All: true}
	if alice.UserID >= bob.UserID {
		t.Fatalf("user ids %d, %d not ascending", alice.UserID, bob.UserID)
	}
	ad := mustDoc(t, st, alice, newDoc("alice doc"))
	bd := mustDoc(t, st, bob, newDoc("bob doc"))
	adminStore := openWith(t, dbURL, map[string]string{"application_name": "admin"})
	aliceStore := openWith(t, dbURL, map[string]string{"application_name": "alice"})

	h := holdAfterLock(ctx, t, "bob's bulk change", func(ctx context.Context) error {
		_, err := st.BulkUpdateDocuments(ctx, bob, store.BulkSelection{IDs: []string{bd.ID}}, store.BulkChange{IsInbox: ptr(false)}, "document.update")
		return err
	})
	adminDone := startOp(func() error {
		_, err := adminStore.BulkUpdateDocuments(ctx, admin, store.BulkSelection{IDs: []string{bd.ID, ad.ID}},
			store.BulkChange{IsFavorite: ptr(true)}, "document.update")
		return err
	})
	waitForAdvisory(ctx, t, conn, "admin", 1, adminDone)
	want := []advisoryLock{{ownerKey(t, conn, alice.UserID), true}, {ownerKey(t, conn, bob.UserID), false}}
	if got := advisoryLocks(t, conn, "admin", 0); !slices.Equal(got, want) {
		t.Errorf("the admin's advisory locks: %+v, want alice's held and bob's waiting %+v", got, want)
	}
	aliceDone := startOp(func() error {
		_, err := aliceStore.PatchDocument(ctx, alice, ad.ID, store.DocumentPatch{Title: ptr("alice's")})
		return err
	})
	waitForAdvisory(ctx, t, conn, "alice", 1, aliceDone)
	close(h.release)
	for name, d := range map[string]chan error{"bob's bulk change": h.done, "admin's bulk change": adminDone, "alice's patch": aliceDone} {
		if err := <-d; err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if g := getDoc(t, st, ad.ID); !g.IsFavorite || g.Title != "alice's" {
		t.Errorf("alice's document: %+v", g)
	}
	if g := getDoc(t, st, bd.ID); !g.IsFavorite || g.IsInbox {
		t.Errorf("bob's document: %+v", g)
	}
}

// A document deleted between the bulk change's owner read and its row
// lock gives ErrNotFound and changes nothing, as before the lock.
func TestBulkUpdateDocumentDeletedAfterOwnerRead(t *testing.T) {
	st, dbURL := storetest.NewWithURL(t)
	ctx := context.Background()
	conn := connect(t, dbURL)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	gone := mustDoc(t, st, alice, newDoc("gone"))
	stays := mustDoc(t, st, alice, newDoc("stays"))
	var delErr error
	held := store.WithBeforeLock(ctx, func() {
		_, delErr = conn.Exec(ctx, `DELETE FROM documents WHERE id = $1`, gone.ID)
	})
	_, err := st.BulkUpdateDocuments(held, alice, store.BulkSelection{IDs: []string{gone.ID, stays.ID}},
		store.BulkChange{IsFavorite: ptr(true)}, "document.update")
	if delErr != nil {
		t.Fatal(delErr)
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("bulk change: %v, want ErrNotFound", err)
	}
	if g := getDoc(t, st, stays.ID); g.IsFavorite {
		t.Errorf("the refused change changed a document: %+v", g)
	}
}

// Review B of PR #103: a writer that changes a document's tags writes its
// row twice (the search trigger, then the change itself), so PostgreSQL
// checks its foreign keys again and takes FOR KEY SHARE on the document's
// correspondent or type after it locked the document; their delete,
// waiting for the document in its SET NULL, held that row. With the owner
// lock the delete waits before anything; without it the writer deadlocks
// (ErrRetry).
func TestLinkerNoDeadlockOnForeignKeyRecheck(t *testing.T) {
	for _, c := range []struct {
		name   string
		typ    bool // a document type, else a correspondent
		change func(st *store.Store, sc store.Scope, doc, tag string) func(context.Context) error
	}{
		{"bulk addTags vs delete correspondent", false, func(st *store.Store, sc store.Scope, doc, tag string) func(context.Context) error {
			return func(ctx context.Context) error {
				_, err := st.BulkUpdateDocuments(ctx, sc, store.BulkSelection{IDs: []string{doc}}, store.BulkChange{AddTags: []string{tag}}, "document.update")
				return err
			}
		}},
		{"bulk removeTags vs delete correspondent", false, func(st *store.Store, sc store.Scope, doc, tag string) func(context.Context) error {
			return func(ctx context.Context) error {
				_, err := st.BulkUpdateDocuments(ctx, sc, store.BulkSelection{IDs: []string{doc}}, store.BulkChange{RemoveTags: []string{tag}}, "document.update")
				return err
			}
		}},
		{"patch tagIds vs delete correspondent", false, func(st *store.Store, sc store.Scope, doc, tag string) func(context.Context) error {
			return func(ctx context.Context) error {
				_, err := st.PatchDocument(ctx, sc, doc, store.DocumentPatch{Title: ptr("patched"), SetTags: true})
				return err
			}
		}},
		{"bulk addTags vs delete document type", true, func(st *store.Store, sc store.Scope, doc, tag string) func(context.Context) error {
			return func(ctx context.Context) error {
				_, err := st.BulkUpdateDocuments(ctx, sc, store.BulkSelection{IDs: []string{doc}}, store.BulkChange{AddTags: []string{tag}}, "document.update")
				return err
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			withAndWithoutOwnerLock(t, func(t *testing.T, locked bool) {
				st, dbURL := storetest.NewWithURL(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
				existing := mustTag(t, st, alice, "Existing", nil)
				added := mustTag(t, st, alice, "Added", nil)
				in := newDoc("doc")
				in.TagIDs = []string{existing.ID}
				var linkID string
				if c.typ {
					linkID = mustDocType(t, st, alice, "Invoice").ID
					in.DocumentTypeID = &linkID
				} else {
					linkID = mustCorrespondent(t, st, alice, "Bank").ID
					in.CorrespondentID = &linkID
				}
				doc := mustDoc(t, st, alice, in)
				tag := added.ID
				if c.name == "bulk removeTags vs delete correspondent" {
					tag = existing.ID
				}
				deleter, linker := lockOrderStores(t, dbURL, "deleter", "linker")
				conn := connect(t, dbURL)

				h := holdAfterLock(ctx, t, "linker", c.change(linker, alice, doc.ID, tag))
				deleted := startOp(func() error {
					if c.typ {
						return deleter.DeleteDocumentType(ctx, alice, linkID)
					}
					return deleter.DeleteCorrespondent(ctx, alice, linkID)
				})
				waitForLock(ctx, t, conn, "deleter", deleted)
				close(h.release)
				wantDeadlockLoser(t, "linker", locked, <-h.done)
				if err := <-deleted; err != nil {
					t.Errorf("delete: %v", err)
				}
				if g := getDoc(t, st, doc.ID); g.CorrespondentID != nil || g.DocumentTypeID != nil {
					t.Errorf("after both: %+v", g)
				}
			})
		})
	}
}

// Review A of PR #103: Q is P's parent, C is P's child. PatchTag(C,
// parent=Q) holds C; DeleteTag(P) waits for C (its SET NULL); DeleteTag(Q)
// waits for P (its SET NULL); the patch's foreign-key check then waits for
// Q: a deadlock. The variant: CreateTag(X, parent=P) takes the
// tags_no_cycle trigger's lock and waits for P, and the patch's trigger
// then waits for that lock. With the owner lock every one of them waits
// for the patch before it touches a row; without it the patch deadlocks
// (ErrRetry).
func TestPatchTagNoDeadlockWithParentDelete(t *testing.T) {
	for _, variant := range []string{"delete parent and grandparent", "delete parent, create under it"} {
		t.Run(variant, func(t *testing.T) {
			withAndWithoutOwnerLock(t, func(t *testing.T, locked bool) {
				st, dbURL := storetest.NewWithURL(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
				q := mustTag(t, st, alice, "Q", nil)
				p := mustTag(t, st, alice, "P", q)
				c := mustTag(t, st, alice, "C", p)
				deleterP, patcher := lockOrderStores(t, dbURL, "deleter-p", "patcher")
				other := openWith(t, dbURL, map[string]string{"options": "-c deadlock_timeout=1h", "application_name": "other"})
				conn := connect(t, dbURL)

				h := holdAfterLock(ctx, t, "patch", func(ctx context.Context) error {
					_, err := patcher.PatchTag(ctx, alice, c.ID, store.TagPatch{SetParent: true, ParentID: &q.ID})
					return err
				})
				delP := startOp(func() error { return deleterP.DeleteTag(ctx, alice, p.ID) })
				waitForLock(ctx, t, conn, "deleter-p", delP)
				var created *store.Tag
				third := startOp(func() error {
					if variant == "delete parent and grandparent" {
						return other.DeleteTag(ctx, alice, q.ID)
					}
					var err error
					created, err = other.CreateTag(ctx, alice, store.TagInput{Name: "X", ParentID: &p.ID})
					return err
				})
				waitForLock(ctx, t, conn, "other", third)
				close(h.release)
				wantDeadlockLoser(t, "patch", locked, <-h.done)
				if err := <-delP; err != nil {
					t.Errorf("delete P: %v", err)
				}
				errThird := <-third
				gotC, err := st.GetTag(ctx, alice, c.ID)
				if err != nil {
					t.Fatalf("C after all: %v", err)
				}
				if variant == "delete parent and grandparent" {
					if errThird != nil {
						t.Errorf("delete Q: %v", errThird)
					}
					if gotC.ParentID != nil {
						t.Errorf("C after the deletes: %+v, want a root", gotC)
					}
					return
				}
				// The create comes before or after P's delete: under P
				// (then a root once P is gone) or refused.
				if errThird != nil && !errors.Is(errThird, store.ErrOwnerMismatch) {
					t.Errorf("create under P: %v, want success or ErrOwnerMismatch", errThird)
				}
				if errThird == nil {
					if x, err := st.GetTag(ctx, alice, created.ID); err != nil || x.ParentID != nil {
						t.Errorf("X after P's delete: %+v, %v; want a root", x, err)
					}
				}
				// With the lock the patch moved C under Q; without it the
				// patch failed and P's delete made C a root.
				if locked && (gotC.ParentID == nil || *gotC.ParentID != q.ID) || !locked && gotC.ParentID != nil {
					t.Errorf("C after all: parent %v (owner lock %v)", gotC.ParentID, locked)
				}
			})
		})
	}
}
