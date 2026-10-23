// Package sweep removes what the trash let go (#12;
// docs/development/architecture.md, "Trash"). Its row pass deletes the
// documents that are due for purge: their purge was asked for ("delete
// permanently", "empty trash") or the retention passed since they were
// deleted. Each purge queues the document's files for removal; the file
// passes that remove them come with #12, part 2.
package sweep

import (
	"context"
	"log/slog"
	"time"

	"github.com/pklnx/paperless-go/internal/store"
)

// Defaults.
const (
	// DefaultInterval is how often serve sweeps; a purge request in the
	// same process wakes the sweeper at once.
	DefaultInterval = time.Minute
	// BatchSize is the number of documents one row pass deletes per
	// round, at most one owner per transaction: with the vocabulary
	// trigger a delete costs about 12 ms, so an owner's other writers
	// wait at most about 1.5 s for one transaction.
	BatchSize = 100
)

// Store is the part of *store.Store the sweeper uses.
type Store interface {
	PurgeDueDocuments(ctx context.Context, now time.Time, retention time.Duration, limit int) (store.PurgeResult, error)
}

// Sweeper runs the sweeps. Retention and Now must be the store's
// (store.Store.TrashRetention and Now), so the sweeper and the API agree
// on what is due.
type Sweeper struct {
	Store Store
	Log   *slog.Logger
	// Now is the clock; time.Now if nil.
	Now func() time.Time
	// Retention is how long a trashed document stays.
	Retention time.Duration
}

// Stats is what one RunOnce did.
type Stats struct {
	// Purged is the number of documents deleted, Retention those of them
	// whose retention passed (the others' purge was asked for).
	Purged, Retention int
}

func (s *Sweeper) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Sweeper) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

// RunOnce runs the row pass until no document is due at the time it
// started: batches of BatchSize, so emptying a trash of 5 000 documents
// takes one call, not 50 rounds of the ticker. It stops with ctx (between
// owners' transactions) and on the first error; what was deleted until
// then stays deleted.
func (s *Sweeper) RunOnce(ctx context.Context) (Stats, error) {
	var st Stats
	now := s.now()
	for {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		r, err := s.Store.PurgeDueDocuments(ctx, now, s.Retention, BatchSize)
		st.Purged += r.Purged
		st.Retention += r.Retention
		if err != nil {
			return st, err
		}
		// Every scanned document was deleted or is no longer due (a
		// restore came first), so a scan below the limit was the last.
		if r.Scanned < BatchSize {
			return st, nil
		}
	}
}

// Run sweeps at once, then at every tick and whenever wake delivers, until
// ctx ends. An error is logged; the next round tries again.
func (s *Sweeper) Run(ctx context.Context, tick <-chan time.Time, wake <-chan struct{}) {
	log := s.log()
	for {
		st, err := s.RunOnce(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			log.Error("sweep: purging the trash failed; trying again at the next round", "purged", st.Purged, "err", err)
		case st.Purged > 0:
			log.Info("sweep: purged documents from the trash", "purged", st.Purged, "retention", st.Retention)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick:
		case <-wake:
		}
	}
}

// Wake returns a channel for Run and the function that wakes it without
// blocking (a wake while one is pending is merged into it).
func Wake() (<-chan struct{}, func()) {
	c := make(chan struct{}, 1)
	return c, func() {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}
