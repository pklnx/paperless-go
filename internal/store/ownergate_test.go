package store_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/pklnx/paperless-go/internal/store"
	"github.com/pklnx/paperless-go/internal/store/storetest"
)

// poolSize is the pool of the gate tests' stores, so the number of
// writers that would exhaust it is known.
const poolSize = 4

// envWithoutGate runs TestOtherOwnerNotDelayedByWaitingWriters with the
// owner gate disabled (store.DisableOwnerGate), to show that it fails
// without it: PAPERLESS_GO_TEST_WITHOUT_GATE=1 go test -run
// TestOtherOwnerNotDelayedByWaitingWriters ./internal/store/ is expected
// to FAIL.
const envWithoutGate = "PAPERLESS_GO_TEST_WITHOUT_GATE"

// #117: while alice's patch holds her owner lock, pool size + 2 more of
// her writers (patch, upload, tag and correspondent create, bulk change
// by IDs, OCR completion) wait for it at the owner gate without a pool
// connection, so bob's patch, read and tag create and a ping still get
// one. Afterwards all of alice's writers succeed. Without the gate the
// waiters hold every connection at the advisory lock and bob's calls run
// into their failure deadline.
func TestOtherOwnerNotDelayedByWaitingWriters(t *testing.T) {
	gated := os.Getenv(envWithoutGate) == ""
	if !gated {
		store.DisableOwnerGate(t)
	}
	st, dbURL := storetest.NewWithParams(t, map[string]string{
		"pool_max_conns": strconv.Itoa(poolSize), "application_name": "gate-test",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn := connect(t, dbURL)
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	archived := func(name string) store.NewDocument {
		in := newDoc(name)
		in.ArchiveSHA256, in.ArchiveSize = ptr(hashOf("archive "+name)), ptr(int64(1))
		return in
	}
	jobDoc := mustDoc(t, st, alice, newDoc("alice job"))
	job := claim(t, st, time.Now())
	if job.DocumentID != jobDoc.ID {
		t.Fatalf("claimed %s, want %s", job.DocumentID, jobDoc.ID)
	}
	held := mustDoc(t, st, alice, archived("held"))
	patched := mustDoc(t, st, alice, archived("patched"))
	bulked := mustDoc(t, st, alice, archived("bulked"))
	bobDoc := mustDoc(t, st, bob, archived("bob's"))

	h := holdAfterLock(ctx, t, "alice's held patch", func(ctx context.Context) error {
		_, err := st.PatchDocument(ctx, alice, held.ID, store.DocumentPatch{Title: ptr("held")})
		return err
	})
	released := false
	defer func() {
		if !released {
			close(h.release)
		}
	}()
	writers := []func(context.Context) error{
		func(ctx context.Context) error {
			_, err := st.PatchDocument(ctx, alice, patched.ID, store.DocumentPatch{Title: ptr("waited")})
			return err
		},
		func(ctx context.Context) error {
			_, err := st.CreateDocument(ctx, alice, archived("upload"))
			return err
		},
		func(ctx context.Context) error {
			_, err := st.CreateTag(ctx, alice, store.TagInput{Name: "waited"})
			return err
		},
		func(ctx context.Context) error {
			_, err := st.CreateCorrespondent(ctx, alice, store.CorrespondentInput{Name: "waited"})
			return err
		},
		func(ctx context.Context) error {
			_, err := st.BulkUpdateDocuments(ctx, alice, store.BulkSelection{IDs: []string{bulked.ID}},
				store.BulkChange{IsFavorite: ptr(true)}, "document.update")
			return err
		},
		func(ctx context.Context) error {
			return st.CompleteJob(ctx, job, time.Now(), store.ProcessResult{PageCount: 1, Text: "t",
				TextSource: store.TextSourceTesseract, ArchiveSHA256: hashOf("job archive"), ArchiveSize: 1})
		},
	}
	if len(writers) < poolSize+2 {
		t.Fatalf("%d waiting writers, want at least the pool size + 2", len(writers))
	}
	gateWaits := make(chan struct{}, len(writers))
	wctx := store.WithGateWait(ctx, func() { gateWaits <- struct{}{} })
	var dones []chan error
	for _, w := range writers {
		dones = append(dones, startOp(func() error { return w(wctx) }))
	}
	if gated {
		for range writers {
			select {
			case <-gateWaits:
			case <-ctx.Done():
				t.Fatalf("alice's writers never all waited at the gate")
			}
		}
	} else {
		// Without the gate as many as get a connection wait at the lock.
		waitForAdvisory(ctx, t, conn, "gate-test", poolSize-1, dones...)
	}
	for i, d := range dones {
		select {
		case err := <-d:
			t.Fatalf("alice's writer %d finished while her patch held the lock: %v", i, err)
		default:
		}
	}
	n := store.AcquiredConns(st)
	t.Logf("%d of %d connections in use while alice's writers wait (gate: %v)", n, poolSize, gated)
	if gated && n > 1 {
		t.Errorf("%d connections in use while alice's writers wait, want only her held patch's", n)
	}

	// The failure bound of bob's calls: with the gate they need no
	// waiting at all.
	bctx, bcancel := context.WithTimeout(ctx, 5*time.Second)
	defer bcancel()
	if _, err := st.PatchDocument(bctx, bob, bobDoc.ID, store.DocumentPatch{Title: ptr("bob's patch")}); err != nil {
		t.Errorf("bob's patch: %v", err)
	}
	if _, err := st.GetDocument(bctx, bob, bobDoc.ID); err != nil {
		t.Errorf("bob's read: %v", err)
	}
	if _, err := st.CreateTag(bctx, bob, store.TagInput{Name: "bob's"}); err != nil {
		t.Errorf("bob's tag create: %v", err)
	}
	if err := st.Ping(bctx); err != nil {
		t.Errorf("ping: %v", err)
	}
	if n := store.AcquiredConns(st); gated && n > 1 {
		t.Errorf("%d connections in use after bob's calls, want only alice's held patch's", n)
	}

	released = true
	close(h.release)
	if err := <-h.done; err != nil {
		t.Errorf("alice's held patch: %v", err)
	}
	for i, d := range dones {
		if err := <-d; err != nil {
			t.Errorf("alice's writer %d: %v", i, err)
		}
	}
	if g := getDoc(t, st, patched.ID); g.Title != "waited" {
		t.Errorf("alice's waiting patch is missing: %+v", g)
	}
	if g := getDoc(t, st, bulked.ID); !g.IsFavorite {
		t.Errorf("alice's waiting bulk change is missing: %+v", g)
	}
	if n := store.GateEntries(st); n != 0 {
		t.Errorf("%d gate entries left", n)
	}
}

// With WithOwnerWait a writer gives up with ErrBusy and writes nothing:
// at the gate, behind a writer of the same store, and at the owner lock,
// behind a holder outside the store (another process), also at the second
// owner's lock of an admin's change over two owners. Inside the
// transaction the bound is gone again right after the owner locks, and a
// connection reused after ErrBusy has its default lock_timeout. Without a
// bound the writer waits until the holder ends.
func TestOwnerWaitBound(t *testing.T) {
	st, dbURL := storetest.NewWithParams(t, map[string]string{"pool_max_conns": "1", "application_name": "bound"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alice := store.Scope{UserID: storetest.User(t, st, "alice", false).ID}
	bob := store.Scope{UserID: storetest.User(t, st, "bob", false).ID}
	admin := store.Scope{UserID: storetest.User(t, st, "root", true).ID, All: true}
	if alice.UserID >= bob.UserID {
		t.Fatalf("user ids %d, %d not ascending", alice.UserID, bob.UserID)
	}
	doc := mustDoc(t, st, alice, newDoc("doc"))
	other := mustDoc(t, st, alice, newDoc("other"))
	bobDoc := mustDoc(t, st, bob, newDoc("bob's"))
	defaultTimeout, err := store.PoolSetting(ctx, st, "lock_timeout")
	if err != nil {
		t.Fatal(err)
	}
	bounded := store.WithOwnerWait(ctx, 50*time.Millisecond)
	patch := func(ctx context.Context, id string) error {
		_, err := st.PatchDocument(ctx, alice, id, store.DocumentPatch{Title: ptr("changed")})
		return err
	}

	t.Run("lock_timeout reset inside the transaction", func(t *testing.T) {
		var inTx string
		var qerr error
		in := store.WithAfterOwnerLock(store.WithOwnerWait(ctx, 10*time.Second), func(query func(string) (string, error)) {
			inTx, qerr = query(`SHOW lock_timeout`)
		})
		if _, err := st.PatchDocument(in, alice, other.ID, store.DocumentPatch{Notes: ptr("bounded")}); err != nil {
			t.Fatal(err)
		}
		if qerr != nil || inTx != defaultTimeout {
			t.Errorf("lock_timeout after the owner locks: %q, %v; want the default %q", inTx, qerr, defaultTimeout)
		}
	})

	t.Run("at the gate", func(t *testing.T) {
		// A store with a pool of its own size: the waiter's owner read
		// needs a connection besides the holder's.
		gs := openWith(t, dbURL, nil)
		h := holdAfterLock(ctx, t, "held patch", func(ctx context.Context) error {
			_, err := gs.PatchDocument(ctx, alice, other.ID, store.DocumentPatch{Notes: ptr("held")})
			return err
		})
		waited := false
		_, err := gs.PatchDocument(store.WithGateWait(bounded, func() { waited = true }), alice, doc.ID,
			store.DocumentPatch{Title: ptr("changed")})
		close(h.release)
		if herr := <-h.done; herr != nil {
			t.Errorf("held patch: %v", herr)
		}
		if !errors.Is(err, store.ErrBusy) || !waited {
			t.Errorf("bounded patch behind a held one: %v (waited at the gate: %v), want ErrBusy", err, waited)
		}
		if g := getDoc(t, st, doc.ID); g.Title != "doc" {
			t.Errorf("the refused patch wrote: %+v", g)
		}
	})

	// holdOutside holds owner's lock in an open transaction of a
	// connection outside the store, until the returned function ends it.
	holdOutside := func(t *testing.T, owner int64) func() {
		t.Helper()
		c := connect(t, dbURL)
		tx, err := c.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text || $2::bigint::text, 0))`,
			store.OwnerLockPrefix, owner); err != nil {
			t.Fatal(err)
		}
		return func() { _ = tx.Rollback(ctx) }
	}

	t.Run("at the lock, another process", func(t *testing.T) {
		end := holdOutside(t, alice.UserID)
		err := patch(bounded, doc.ID)
		end()
		if !errors.Is(err, store.ErrBusy) {
			t.Errorf("bounded patch behind another process: %v, want ErrBusy", err)
		}
		if g := getDoc(t, st, doc.ID); g.Title != "doc" {
			t.Errorf("the refused patch wrote: %+v", g)
		}
		// The pool has one connection: this is the one that got ErrBusy.
		if v, err := store.PoolSetting(ctx, st, "lock_timeout"); err != nil || v != defaultTimeout {
			t.Errorf("lock_timeout on the connection after ErrBusy: %q, %v; want %q", v, err, defaultTimeout)
		}
	})

	t.Run("at the second owner's lock", func(t *testing.T) {
		end := holdOutside(t, bob.UserID)
		_, err := st.BulkUpdateDocuments(bounded, admin, store.BulkSelection{IDs: []string{doc.ID, bobDoc.ID}},
			store.BulkChange{IsFavorite: ptr(true)}, "document.update")
		end()
		if !errors.Is(err, store.ErrBusy) {
			t.Errorf("bounded bulk change behind bob's lock: %v, want ErrBusy", err)
		}
		if g := getDoc(t, st, doc.ID); g.IsFavorite {
			t.Errorf("the refused bulk change wrote: %+v", g)
		}
	})

	t.Run("without a bound", func(t *testing.T) {
		end := holdOutside(t, alice.UserID)
		watch := connect(t, dbURL)
		done := startOp(func() error { return patch(ctx, doc.ID) })
		waitForAdvisory(ctx, t, watch, "bound", 1, done)
		end()
		if err := <-done; err != nil {
			t.Errorf("unbounded patch: %v", err)
		}
		if g := getDoc(t, st, doc.ID); g.Title != "changed" {
			t.Errorf("the unbounded patch is missing: %+v", g)
		}
	})
}

// Only ownerTx may take the owner lock (errNotGated otherwise), and a
// writer started inside an owner transaction with its context fails
// instead of waiting for its own gate.
func TestOwnerTxGuards(t *testing.T) {
	st := storetest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	alice := storetest.User(t, st, "alice", false)
	if err := store.LockWithoutGate(ctx, st, alice.ID); !errors.Is(err, store.ErrNotGated) {
		t.Errorf("lockOwners outside ownerTx: %v, want ErrNotGated", err)
	}
	if err := store.NestedCreateTag(ctx, st, alice.ID); !errors.Is(err, store.ErrNestedOwnerTx) {
		t.Errorf("a writer inside an owner transaction: %v, want ErrNestedOwnerTx", err)
	}
	if n := store.GateEntries(st); n != 0 {
		t.Errorf("%d gate entries left", n)
	}
}
