package store

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// The owner gate (docs/development/architecture.md, "Write locking"): a
// writer of the owner tables takes, in this process, a token per owner
// before it takes a pool connection, and gives it back after its
// transaction has ended. So at most one writer per owner and process holds
// a connection while it waits for or holds the owner lock; the others wait
// in Go, without a connection, and a burst of one owner's writes cannot
// take the pool from every other request. The advisory lock (locking.go)
// stays the guarantee across processes; the gate only decides who in this
// process may wait for it with a connection.
//
// Tokens are taken in ascending owner id, as the advisory locks are. A
// writer waiting at the gate holds only tokens of lower ids, no
// connection and no advisory lock; a transaction never waits for a token.
// So gate waits form ascending chains that end at a writer which holds or
// waits for advisory locks, whose waits are acyclic (locking.go), and no
// cycle closes.

// errNotGated means lockOwners locked owners other than those ownerTx
// gated: a writer that bypasses the gate, a bug in the store.
var errNotGated = errors.New("store: owner lock without the owner gate")

// errNestedOwnerTx means a writer started another writer inside its
// transaction. The gate is not reentrant, so this would wait for itself.
var errNestedOwnerTx = errors.New("store: owner transaction inside another")

// ownerGate holds one token per owner while a writer of that owner is in
// its transaction. Entries exist only while someone holds or waits for
// them, so the map does not grow with the owners ever seen.
type ownerGate struct {
	mu      sync.Mutex
	entries map[int64]*gateEntry
	waiting atomic.Int64 // writers waiting at the gate now
}

// gateEntry is one owner's token: a one-slot channel, full while a writer
// holds it. refs counts the writers holding or waiting for it.
type gateEntry struct {
	token chan struct{}
	refs  int
}

func newOwnerGate() *ownerGate { return &ownerGate{entries: map[int64]*gateEntry{}} }

// ownerGateDisabled makes acquire a no-op: a test-only switch
// (export_test.go) that shows a test fails without the gate.
var ownerGateDisabled atomic.Bool

// enter returns owner's entry, counted as used before the caller waits.
func (g *ownerGate) enter(owner int64) *gateEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.entries[owner]
	if e == nil {
		e = &gateEntry{token: make(chan struct{}, 1)}
		g.entries[owner] = e
	}
	e.refs++
	return e
}

// leave drops one use of owner's entry and removes it after the last.
func (g *ownerGate) leave(owner int64, e *gateEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e.refs--; e.refs == 0 {
		delete(g.entries, owner)
	}
}

// size is the number of entries, for the tests.
func (g *ownerGate) size() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.entries)
}

// acquire takes the tokens of owners (deduplicated, ascending) and returns
// the function that gives them back; it is idempotent. Each wait ends
// with ctx (ctx.Err()) or at deadline (ErrBusy; a zero deadline: none);
// then the tokens already taken are given back.
func (g *ownerGate) acquire(ctx context.Context, owners []int64, deadline time.Time) (func(), error) {
	if ownerGateDisabled.Load() {
		return func() {}, nil
	}
	type held struct {
		owner int64
		e     *gateEntry
	}
	var got []held
	var once sync.Once
	release := func() {
		once.Do(func() {
			for i := len(got) - 1; i >= 0; i-- {
				<-got[i].e.token
				g.leave(got[i].owner, got[i].e)
			}
		})
	}
	var timeout <-chan time.Time
	for _, o := range owners {
		e := g.enter(o)
		select {
		case e.token <- struct{}{}:
			got = append(got, held{o, e})
			continue
		default:
		}
		if timeout == nil && !deadline.IsZero() {
			t := time.NewTimer(time.Until(deadline))
			defer t.Stop()
			timeout = t.C
		}
		if f, ok := ctx.Value(gateWaitKey{}).(func()); ok {
			f()
		}
		g.waiting.Add(1)
		var err error
		select {
		case e.token <- struct{}{}:
		case <-ctx.Done():
			err = ctx.Err()
		case <-timeout:
			err = ErrBusy
		}
		g.waiting.Add(-1)
		if err != nil {
			g.leave(o, e)
			release()
			return nil, err
		}
		got = append(got, held{o, e})
	}
	return release, nil
}

// gateWaitKey carries a test hook (export_test.go): a function a writer
// calls when it starts to wait at the gate.
type gateWaitKey struct{}

// gatedKey marks the context of an owner transaction, so a nested one
// fails instead of waiting for itself.
type gatedKey struct{}

// ownerWaitKey carries the bound of WithOwnerWait.
type ownerWaitKey struct{}

// WithOwnerWait bounds how long a writer with ctx waits for other changes
// of the same owner, at the gate and at the owner lock together: after d
// it gives up with ErrBusy and writes nothing. Without it a writer waits
// as long as ctx lives. The web server sets it on every request; the
// worker, the consume folder and the import do not.
func WithOwnerWait(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, ownerWaitKey{}, d)
}

// OwnerGateWaiters returns the number of writers waiting at this store's
// owner gate now: a signal for tests in other packages and for
// monitoring.
func (s *Store) OwnerGateWaiters() int { return int(s.gate.waiting.Load()) }

// ownerTx is the only way to write the owner tables: it takes the gate for
// owners (ascending, deduplicated), runs a transaction (inTxWith) whose
// first statements take the owner locks of the same owners, then calls
// fn, and gives the gate back after the transaction has ended (commit or
// rollback), also on an error or a panic. fn gets the context it must use;
// the gate is not reentrant, so a writer called with it inside fn fails
// with errNestedOwnerTx instead of waiting for itself.
//
// The owners must be known before: a writer that does not know them reads
// them first, outside the transaction (owners never change; the row lock
// and t.holds inside catch a row that went away or a wrong owner). An
// empty list is a bug (errNoOwners). With WithOwnerWait the bound counts
// from here, for the gate and the lock together.
func (s *Store) ownerTx(ctx context.Context, owners []int64, fn func(context.Context, *writeTx) error) error {
	if ctx.Value(gatedKey{}) != nil {
		return errNestedOwnerTx
	}
	if len(owners) == 0 {
		return errNoOwners
	}
	sorted := slices.Clone(owners)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	var deadline time.Time
	if d, ok := ctx.Value(ownerWaitKey{}).(time.Duration); ok {
		deadline = time.Now().Add(d)
	}
	release, err := s.gate.acquire(ctx, sorted, deadline)
	if err != nil {
		return err
	}
	defer release()
	if r, ok := ctx.Value(lockRecorderKey{}).(func(lockEvent)); ok {
		r(lockEvent{Gated: slices.Clone(sorted)})
	}
	ctx = context.WithValue(ctx, gatedKey{}, true)
	return s.inTxWith(ctx, func(t *writeTx) error {
		t.gated, t.deadline = sorted, deadline
		if err := t.lockOwners(ctx, sorted...); err != nil {
			return err
		}
		if f, ok := ctx.Value(afterOwnerLockKey{}).(func(*writeTx)); ok {
			f(t)
		}
		return fn(ctx, t)
	})
}

// afterOwnerLockKey carries a test hook (export_test.go) that runs in the
// transaction right after lockOwners.
type afterOwnerLockKey struct{}
