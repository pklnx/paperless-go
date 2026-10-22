package store

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// gateWaiting returns a context whose gate wait hook sends on the returned
// channel.
func gateWaiting(ctx context.Context) (context.Context, chan struct{}) {
	ch := make(chan struct{}, 16)
	return context.WithValue(ctx, gateWaitKey{}, func() { ch <- struct{}{} }), ch
}

// Two acquirers of overlapping owner sets given in opposite orders both
// finish: the gate sorts, so neither holds a token the other waits for
// while waiting for one the other holds.
func TestGateAscendingNoDeadlock(t *testing.T) {
	g := newOwnerGate()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := range 100 {
		owners := [][]int64{{1, 2}, {2, 1}, {2, 3, 1}, {3, 1}}[i%4]
		wg.Go(func() {
			// ownerTx sorts; acquire relies on it.
			sorted := slices.Sorted(slices.Values(owners))
			release, err := g.acquire(ctx, sorted, time.Time{})
			if err != nil {
				errs <- err
				return
			}
			release()
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("acquire: %v", err)
	}
	if n := g.size(); n != 0 {
		t.Errorf("%d entries left after all users left", n)
	}
}

// A waiter whose context ends returns ctx.Err(), gives back the tokens it
// already took and leaves no entry behind; one past its deadline returns
// ErrBusy the same way. release twice is harmless.
func TestGateWaitEnds(t *testing.T) {
	g := newOwnerGate()
	ctx := context.Background()
	hold, err := g.acquire(ctx, []int64{2}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name     string
		deadline time.Time
		cancel   bool
		want     error
	}{
		{"cancelled", time.Time{}, true, context.Canceled},
		{"deadline", time.Now().Add(time.Millisecond), false, ErrBusy},
		{"deadline passed", time.Now().Add(-time.Second), false, ErrBusy},
	} {
		t.Run(c.name, func(t *testing.T) {
			wctx, cancel := context.WithCancel(ctx)
			defer cancel()
			wctx, waiting := gateWaiting(wctx)
			done := make(chan error, 1)
			go func() {
				release, err := g.acquire(wctx, []int64{1, 2}, c.deadline)
				if err == nil {
					release()
				}
				done <- err
			}()
			<-waiting
			if c.cancel {
				cancel()
			}
			if err := <-done; !errors.Is(err, c.want) {
				t.Fatalf("acquire: %v, want %v", err, c.want)
			}
			// Owner 1's token was given back: it is free at once.
			r, err := g.acquire(ctx, []int64{1}, time.Now().Add(-time.Second))
			if err != nil {
				t.Fatalf("owner 1 after the waiter left: %v", err)
			}
			r()
			if n := g.size(); n != 1 {
				t.Errorf("%d entries, want only the holder's", n)
			}
			if n := g.waiting.Load(); n != 0 {
				t.Errorf("%d waiting", n)
			}
		})
	}
	hold()
	hold()
	if n := g.size(); n != 0 {
		t.Errorf("%d entries left after release", n)
	}
	r, err := g.acquire(ctx, []int64{2}, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatalf("owner 2 after a double release: %v", err)
	}
	r()
}

// The gate hands an owner's token on to a waiter, which then holds it
// alone; a writer of another owner never waits.
func TestGateSerialisesOneOwner(t *testing.T) {
	g := newOwnerGate()
	ctx := context.Background()
	hold, err := g.acquire(ctx, []int64{1}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := g.acquire(ctx, []int64{2}, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatalf("another owner waited: %v", err)
	}
	other()
	wctx, waiting := gateWaiting(ctx)
	got := make(chan func(), 1)
	go func() {
		r, err := g.acquire(wctx, []int64{1}, time.Time{})
		if err != nil {
			t.Error(err)
			r = func() {}
		}
		got <- r
	}()
	<-waiting
	if n := g.waiting.Load(); n != 1 {
		t.Errorf("%d waiting, want 1", n)
	}
	hold()
	r := <-got
	if _, err := g.acquire(ctx, []int64{1}, time.Now().Add(-time.Second)); !errors.Is(err, ErrBusy) {
		t.Errorf("owner 1 while the waiter holds it: %v, want ErrBusy", err)
	}
	r()
	if n := g.size(); n != 0 {
		t.Errorf("%d entries left", n)
	}
}
