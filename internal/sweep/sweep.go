// Package sweep removes what the trash let go (#12;
// docs/development/architecture.md, "Trash" and "Blob removal"). Its row
// pass deletes the documents that are due for purge: their purge was
// asked for ("delete permanently", "empty trash") or the retention passed
// since they were deleted. Each purge queues the document's files in
// blob_removals. The queue pass is the only remover of files: it removes
// a queued file once it is due and no document names it, under the
// hash's blob lock (store.MarkBlobRemoval). The daily walk queues the
// files no document names (left by refused uploads, failed imports or
// crashes) and removes stale temporary files, but only in a data
// directory bound to this installation and behind a sanity stop.
package sweep

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pklnx/paperless-go/internal/blobstore"
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
	// QueueBatch is the most queued files one queue pass removes.
	QueueBatch = 1000
	// WalkInterval is how often serve walks the data directory; the first
	// walk runs at start.
	WalkInterval = 24 * time.Hour
	// WalkBatch is how many hashes the walk looks up at a time.
	WalkBatch = 1000
	// TempAge is the age from which the walk removes a temporary file of
	// Put: an upload's Put is bounded by its hour-long transfer timeout.
	TempAge = 24 * time.Hour
	// MinRefusal is the number of candidates above which the 10 % stop
	// applies: a small store may well lose a tenth of its files at once.
	MinRefusal = 100
)

// Store is the part of *store.Store the sweeper uses.
type Store interface {
	PurgeDueDocuments(ctx context.Context, now time.Time, retention time.Duration, limit int) (store.PurgeResult, error)
	BlobRemovalsDue(ctx context.Context, now time.Time, limit int) ([]string, error)
	// ExclusiveSweep runs fn under the sweep lock; the queue pass's steps
	// 1 and 3 run on the session it gets.
	ExclusiveSweep(ctx context.Context, fn func(*store.SweepSession) error) error
	UnreferencedBlobs(ctx context.Context, hashes []string) ([]string, error)
	WalkCandidates(ctx context.Context, hashes []string) ([]string, error)
	QueueUnreferencedBlobs(ctx context.Context, hashes []string, due time.Time) (int64, error)
	NewestDocumentChange(ctx context.Context) (time.Time, bool, error)
	Binder
}

// Blobs is the part of *blobstore.Store the sweeper uses.
type Blobs interface {
	Remove(hash string) error
	RemoveTemp(rel string) error
	Walk(fn func(blobstore.Entry) error) error
	// Root is the data directory, which holds the installation marker.
	Root() string
}

// Sweeper runs the sweeps. Retention and Now must be the store's
// (store.Store.TrashRetention and Now), so the sweeper and the API agree
// on what is due. Store and Blobs are required.
type Sweeper struct {
	Store Store
	Blobs Blobs
	Log   *slog.Logger
	// Now is the clock; time.Now if nil.
	Now func() time.Time
	// Retention is how long a trashed document stays.
	Retention time.Duration

	// Test seams (export_test.go): called after step 1 and step 2 of
	// the queue pass for a hash.
	afterMark, afterRemove func(hash string)
	// walkStarted is called when a walk starts (export_test.go).
	walkStarted func()
}

// Stats is what one RunOnce did.
type Stats struct {
	// Purged is the number of documents deleted, Retention those of them
	// whose retention passed (the others' purge was asked for).
	Purged, Retention int
	Queue             QueueStats
}

// QueueStats is what one queue pass did (or, in a dry run, would do).
type QueueStats struct {
	// Removed files; in a dry run the due files no document names.
	Removed int
	// InUse are the due hashes a document names again: their rows went
	// and the files stay.
	InUse int
	// Failed removals: the row is unmarked and due again after
	// store.BlobGrace, so uploads of the file are not refused meanwhile.
	Failed int
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
// started (batches of BatchSize, so emptying a trash of 5 000 documents
// takes one call, not 50 rounds of the ticker), then one queue pass. It
// stops with ctx (between owners' transactions and between files) and on
// the first error; what was done until then stays done.
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
			break
		}
	}
	q, err := s.QueuePass(ctx, false)
	st.Queue = q
	return st, err
}

// QueuePass removes up to QueueBatch queued files that are due and that
// no document names, per hash in three steps: mark the row under the
// hash's exclusive blob lock (store.MarkBlobRemoval; a hash in use loses
// its row instead), remove the file without a lock held, delete the row.
// It first checks the data directory's marker: when it names another
// installation (or cannot be read), the pass removes nothing and returns
// that error (ErrOtherInstallation); an unbound data directory is fine,
// since the queue holds only what this database queued. The whole pass
// holds the sweep lock (store.ExclusiveSweep): while
// another pass or walk runs, in this or another process, it does nothing
// and returns store.ErrSweepRunning. A crash after the first step leaves
// the row marked, and a pass after store.MarkStale takes it again; Remove
// of a file already gone is fine. A failed removal is logged at error
// level, its row unmarked and due again after store.BlobGrace, and the
// pass goes on with the next hash. With dryRun it only counts.
func (s *Sweeper) QueuePass(ctx context.Context, dryRun bool) (QueueStats, error) {
	var st QueueStats
	err := s.Store.ExclusiveSweep(ctx, func(ss *store.SweepSession) error {
		var err error
		st, err = s.queuePass(ctx, ss, dryRun)
		return err
	})
	return st, err
}

func (s *Sweeper) queuePass(ctx context.Context, ss *store.SweepSession, dryRun bool) (QueueStats, error) {
	var st QueueStats
	// The data directory must not belong to another installation: this
	// database's queue names files of its own documents only.
	inst, err := s.Store.Installation(ctx)
	if err != nil {
		return st, err
	}
	if err := CheckBinding(s.Blobs.Root(), inst.ID); err != nil && !errors.Is(err, ErrUnbound) {
		return st, err
	}
	now := s.now()
	hashes, err := s.Store.BlobRemovalsDue(ctx, now, QueueBatch)
	if err != nil || len(hashes) == 0 {
		return st, err
	}
	if dryRun {
		free, err := s.Store.UnreferencedBlobs(ctx, hashes)
		st.Removed, st.InUse = len(free), len(hashes)-len(free)
		return st, err
	}
	log := s.log()
	for _, h := range hashes {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		// Each mark carries the time it was set, so a long pass never
		// leaves a fresh mark that already looks stale.
		markedAt := s.now()
		mark, err := ss.MarkBlobRemoval(ctx, h, markedAt)
		if err != nil {
			return st, err
		}
		switch mark {
		case store.BlobInUse:
			st.InUse++
			continue
		case store.BlobKept:
			continue
		}
		if s.afterMark != nil {
			s.afterMark(h)
		}
		// The session must still hold the sweep lock and the row its
		// mark: a session that was terminated or timed out may have let
		// another pass take the mark over and finish, after which a
		// writer may have stored and named the file again.
		if err := ss.StillMarked(ctx, h, markedAt); err != nil {
			log.Error("sweep: the sweep lost its lock; the pass stops and removes nothing more", "sha256", h, "err", err)
			return st, err
		}
		if err := s.Blobs.Remove(h); err != nil {
			st.Failed++
			log.Error("sweep: removing a file failed; trying again later", "sha256", h, "retry_after", store.BlobGrace, "err", err)
			if err := ss.RequeueBlobRemoval(ctx, h, markedAt, s.now().Add(store.BlobGrace)); err != nil {
				return st, err
			}
			continue
		}
		if s.afterRemove != nil {
			s.afterRemove(h)
		}
		if err := ss.FinishBlobRemoval(ctx, h, markedAt); err != nil {
			return st, err
		}
		st.Removed++
	}
	return st, nil
}

// WalkOptions are the choices of one walk.
type WalkOptions struct {
	// DryRun only counts: nothing is queued or removed.
	DryRun bool
	// Force overrides the sanity stops (an empty database, more than 10 %
	// of the files) for this walk and queues the files newer than the
	// newest document change too. The binding is never overridden.
	Force bool
}

// WalkStats is what one walk saw and did.
type WalkStats struct {
	// Blobs is the number of files below documents/, Candidates those no
	// document names and that are not queued yet, Queued the rows added
	// to blob_removals (in a dry run: 0).
	Blobs, Candidates, Queued int
	// Newer is the number of candidates written after the newest document
	// change (store.NewestDocumentChange): unless Force, they are kept,
	// not queued, since they most likely belong to documents this
	// database does not have (an older dump restored with a newer data
	// directory).
	Newer int
	// Temp is the number of stale temporary files removed (in a dry run:
	// found).
	Temp int
	// Unexpected is the number of entries the walk left alone: symlinks,
	// other file types, wrong names or places.
	Unexpected int
}

// maxUnexpectedLogged bounds the warnings about unexpected entries per
// walk; the count covers the rest.
const maxUnexpectedLogged = 20

// Walk walks the data directory once. It refuses, queues nothing and
// removes nothing when the data directory is not bound to the database's
// installation (ErrUnbound, ErrOtherInstallation; the binding is checked
// first, before any file is looked at), or, unless opts.Force, when the
// database has no documents but the data directory has files
// (ErrEmptyDatabase) or when more than 10 % of the files and more than
// MinRefusal would be queued (ErrTooMany): it counts all candidates (files
// no document names and not queued yet) before it queues any. Otherwise it
// queues the files no document names, due after store.BlobGrace (the queue
// pass removes them unless a writer names them by then), and removes the
// temporary files of Put older than TempAge. Unless opts.Force it keeps
// the candidates written after the newest document change and counts them
// in WalkStats.Newer: a file no document names that is newer than anything
// the database did most likely belongs to documents it does not have, as
// after a restore of an older dump with a newer data directory.
//
// The walk holds the sweep lock (store.ExclusiveSweep) like the queue
// pass; while another runs it returns store.ErrSweepRunning.
func (s *Sweeper) Walk(ctx context.Context, opts WalkOptions) (WalkStats, error) {
	var st WalkStats
	err := s.Store.ExclusiveSweep(ctx, func(*store.SweepSession) error {
		var err error
		st, err = s.walk(ctx, opts)
		return err
	})
	return st, err
}

func (s *Sweeper) walk(ctx context.Context, opts WalkOptions) (WalkStats, error) {
	var st WalkStats
	if s.walkStarted != nil {
		s.walkStarted()
	}
	inst, err := s.Store.Installation(ctx)
	if err != nil {
		return st, err
	}
	if err := CheckBinding(s.Blobs.Root(), inst.ID); err != nil {
		return st, err
	}
	now := s.now()
	log := s.log()
	var candidates, temps, batch []string
	// The last write of each file of the batch, for the candidates.
	modTimes := make(map[string]time.Time, WalkBatch)
	var candidateTimes []time.Time
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		free, err := s.Store.WalkCandidates(ctx, batch)
		for _, h := range free {
			candidates = append(candidates, h)
			candidateTimes = append(candidateTimes, modTimes[h])
		}
		batch = batch[:0]
		clear(modTimes)
		return err
	}
	err = s.Blobs.Walk(func(e blobstore.Entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch e.Kind {
		case blobstore.EntryBlob:
			st.Blobs++
			batch = append(batch, e.SHA256)
			modTimes[e.SHA256] = e.ModTime
			if len(batch) == WalkBatch {
				return flush()
			}
		case blobstore.EntryTemp:
			if strings.HasPrefix(e.Path, "tmp/put-") && now.Sub(e.ModTime) > TempAge {
				temps = append(temps, e.Path)
			}
		default:
			st.Unexpected++
			if st.Unexpected <= maxUnexpectedLogged {
				log.Warn("sweep: left an unexpected entry in the data directory alone", "path", e.Path, "reason", e.Reason)
			}
		}
		return nil
	})
	if err == nil {
		err = flush()
	}
	st.Candidates = len(candidates)
	if err != nil {
		return st, err
	}
	// The candidates written before the newest document change; the
	// others are counted in Newer (also for a refusal's report).
	older := candidates
	if len(candidates) > 0 {
		newest, found, err := s.Store.NewestDocumentChange(ctx)
		if err != nil {
			return st, err
		}
		older = make([]string, 0, len(candidates))
		for i, h := range candidates {
			// Without any document or job every candidate counts as
			// newer; only Force (over ErrEmptyDatabase) queues them then.
			if !found || candidateTimes[i].After(newest) {
				st.Newer++
				continue
			}
			older = append(older, h)
		}
	}
	if !opts.Force {
		has, err := s.Store.HasDocuments(ctx)
		if err != nil {
			return st, err
		}
		switch {
		case !has && st.Blobs > 0:
			return st, fmt.Errorf("%w (%d files); nothing was queued", ErrEmptyDatabase, st.Blobs)
		case st.Candidates > MinRefusal && st.Candidates*10 > st.Blobs:
			return st, fmt.Errorf("%w (%d of %d files); nothing was queued", ErrTooMany, st.Candidates, st.Blobs)
		}
	}
	if !opts.Force {
		candidates = older
	}
	if opts.DryRun {
		st.Temp = len(temps)
		return st, nil
	}
	due := now.Add(store.BlobGrace)
	for len(candidates) > 0 {
		n := min(len(candidates), WalkBatch)
		q, err := s.Store.QueueUnreferencedBlobs(ctx, candidates[:n], due)
		st.Queued += int(q)
		if err != nil {
			return st, err
		}
		candidates = candidates[n:]
	}
	for _, p := range temps {
		if err := s.Blobs.RemoveTemp(p); err != nil {
			log.Error("sweep: removing a stale temporary file failed", "path", p, "err", err)
			continue
		}
		st.Temp++
	}
	return st, nil
}

// Refused reports whether err is one of the walk's refusals.
func Refused(err error) bool {
	return errors.Is(err, ErrUnbound) || errors.Is(err, ErrOtherInstallation) ||
		errors.Is(err, ErrEmptyDatabase) || errors.Is(err, ErrTooMany)
}

// Run sweeps at once, then at every tick and whenever wake delivers, until
// ctx ends: each round runs RunOnce, and the walk at the first round and
// then once WalkInterval passed on the sweeper's clock. An error is
// logged; the next round tries again (the walk the next day).
func (s *Sweeper) Run(ctx context.Context, tick <-chan time.Time, wake <-chan struct{}) {
	log := s.log()
	var nextWalk time.Time
	for {
		st, err := s.RunOnce(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, store.ErrSweepRunning):
			log.Debug("sweep: another sweep is running; the queue pass waits for the next round")
		case errors.Is(err, ErrOtherInstallation):
			log.Error("sweep: the data directory belongs to another installation; no file is removed", "dir", s.Blobs.Root(),
				"err", err)
		case err != nil:
			log.Error("sweep: the round failed; trying again at the next round", "purged", st.Purged,
				"removed", st.Queue.Removed, "err", err)
		}
		if st.Purged > 0 {
			log.Info("sweep: purged documents from the trash", "purged", st.Purged, "retention", st.Retention)
		}
		if q := st.Queue; q.Removed+q.InUse+q.Failed > 0 {
			log.Info("sweep: removed files", "removed", q.Removed, "in_use", q.InUse, "failed", q.Failed)
		}
		if now := s.now(); !now.Before(nextWalk) && s.logWalk(ctx, log) {
			nextWalk = now.Add(WalkInterval)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick:
		case <-wake:
		}
	}
}

// logWalk runs the daily walk and logs what it did. It reports false when
// another sweep ran, so the walk is tried again at the next round.
func (s *Sweeper) logWalk(ctx context.Context, log *slog.Logger) bool {
	w, err := s.Walk(ctx, WalkOptions{})
	switch {
	case errors.Is(err, store.ErrSweepRunning):
		log.Debug("sweep: another sweep is running; the walk waits for the next round")
		return false
	case ctx.Err() != nil:
	case errors.Is(err, ErrUnbound):
		log.Warn("sweep: the data directory is not bound to this installation, so files no document names are not removed; "+
			"run sweep --bind once", "dir", s.Blobs.Root())
	case Refused(err):
		log.Error("sweep: the walk of the data directory refused to queue files", "dir", s.Blobs.Root(),
			"files", w.Blobs, "unreferenced", w.Candidates, "newer", w.Newer, "err", err)
	case err != nil:
		log.Error("sweep: the walk of the data directory failed; trying again tomorrow", "err", err)
	default:
		log.Info("sweep: walked the data directory", "files", w.Blobs, "unreferenced", w.Candidates, "queued", w.Queued,
			"temporary_removed", w.Temp, "unexpected", w.Unexpected)
		if w.Newer > 0 {
			log.Warn("sweep: kept files no document names that are newer than the newest document change; "+
				"was an older database restored with a newer data directory? Put back the data directory of the same backup, "+
				"or, if these files are not needed, queue them once with sweep --force",
				"dir", s.Blobs.Root(), "kept", w.Newer)
		}
	}
	return true
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
