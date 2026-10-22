package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pklnx/paperless-go/internal/blobstore"
	"github.com/pklnx/paperless-go/internal/store"
)

// Queue timing.
const (
	// DefaultLease is how long a claimed job stays the worker's without a
	// heartbeat; the heartbeat renews it every third of it.
	DefaultLease = 5 * time.Minute
	// pollInterval is the longest an idle worker waits without a
	// notification, for retries that became due and missed notifications.
	pollInterval = time.Minute
	// housekeepingInterval is how often the reaper runs.
	housekeepingInterval = time.Minute
	// keepDone is how long done jobs are kept.
	keepDone = 7 * 24 * time.Hour
	// listenRetry is the wait before listening again after the listening
	// connection failed.
	listenRetry = 10 * time.Second
	// releaseTimeout bounds giving a job back at shutdown.
	releaseTimeout = 10 * time.Second
)

// retryBackoff is the wait before the second and the third attempt.
var retryBackoff = []time.Duration{time.Minute, 5 * time.Minute}

// Backoff is the wait after the given number of failed attempts.
func Backoff(attempts int32) time.Duration {
	i := int(attempts) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(retryBackoff) {
		i = len(retryBackoff) - 1
	}
	return retryBackoff[i]
}

// msgLeaseExpired is the error of an attempt the reaper ended.
const msgLeaseExpired = "Processing stopped before it finished (the server was restarted or the worker stopped responding)."

// Outcome is what became of one claimed job.
type Outcome int

// Outcomes of RunOnce.
const (
	NoJob    Outcome = iota // nothing was due
	Done                    // the result is stored
	Retry                   // the attempt failed; the job runs again later
	Failed                  // the job failed for good
	Released                // shutdown: the job was given back
	Dropped                 // the document was deleted or the lease lost; nothing stored
)

// Worker runs the processing queue.
type Worker struct {
	Store    *store.Store
	Blobs    *blobstore.Store
	Pipeline *Pipeline
	WorkDir  *WorkDir
	Log      *slog.Logger
	// Workers is how many jobs Run processes at once (at least 1).
	Workers int
	// Timeout limits each attempt.
	Timeout time.Duration
	// Lease is DefaultLease unless set.
	Lease time.Duration
	// Now is the clock of claims, leases, retries and the reaper; nil means
	// time.Now.
	Now func() time.Time

	// Test hooks. after returns a channel that fires after d (heartbeat,
	// idle wait); nil means time.After. listening is called each time the
	// listener is ready, heartbeat after each lease renewal, completing
	// before the result is stored.
	after      func(d time.Duration) <-chan time.Time
	listening  func()
	heartbeat  func(error)
	completing func()
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Worker) lease() time.Duration {
	if w.Lease > 0 {
		return w.Lease
	}
	return DefaultLease
}

func (w *Worker) wait(d time.Duration) <-chan time.Time {
	if w.after != nil {
		return w.after(d)
	}
	return time.After(d)
}

// errLeaseLost cancels an attempt whose lease another worker or the reaper
// took.
var errLeaseLost = errors.New("lease lost")

// RunOnce claims the next due job and processes it. It returns NoJob when
// nothing is due. An error means the queue could not be read or updated;
// the job's state then is the reaper's to settle.
func (w *Worker) RunOnce(ctx context.Context) (Outcome, error) {
	job, err := w.Store.ClaimJob(ctx, w.now(), w.lease())
	if err != nil || job == nil {
		return NoJob, err
	}
	log := w.Log.With("job", job.ID, "document", job.DocumentID, "attempt", job.Attempts)
	log.Info("processing document")

	jobCtx, cancelJob := context.WithCancelCause(ctx)
	defer cancelJob(nil)
	stop := make(chan struct{})
	var hb sync.WaitGroup
	hb.Go(func() { w.renewLease(jobCtx, job, cancelJob, stop, log) })
	runCtx, cancelRun := context.WithTimeout(jobCtx, w.Timeout)
	res, err := w.attempt(runCtx, job, log)
	timedOut := err != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) && jobCtx.Err() == nil
	cancelRun()
	close(stop)
	hb.Wait()

	switch {
	case ctx.Err() != nil:
		return w.release(ctx, job, log)
	case errors.Is(context.Cause(jobCtx), errLeaseLost):
		log.Warn("lease lost; result dropped")
		return Dropped, nil
	case errors.Is(err, errDocumentGone):
		log.Info("document deleted; result dropped")
		return Dropped, nil
	case timedOut:
		return w.failAttempt(ctx, job, fmt.Errorf("%w: %w", &timeoutError{limit: w.Timeout}, err), log)
	case err != nil:
		return w.failAttempt(ctx, job, err, log)
	}

	if w.completing != nil {
		w.completing()
	}
	err = w.Store.CompleteJob(ctx, job, w.now(), res)
	switch {
	case err != nil && ctx.Err() != nil:
		// Shutdown while storing: nothing or everything was committed. A
		// job that is done is no longer ours to give back.
		return w.release(ctx, job, log)
	case errors.Is(err, store.ErrNotFound):
		log.Info("document deleted; result dropped")
		return Dropped, nil
	case errors.Is(err, store.ErrLeaseLost):
		log.Warn("lease lost; result dropped")
		return Dropped, nil
	case err != nil:
		return w.failAttempt(ctx, job, fmt.Errorf("store result: %w", err), log)
	}
	log.Info("document processed", "pages", res.PageCount, "text_source", res.TextSource)
	return Done, nil
}

// release gives job back at shutdown, without counting the attempt, on a
// context that outlives ctx.
func (w *Worker) release(ctx context.Context, job *store.Job, log *slog.Logger) (Outcome, error) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	if err := w.Store.ReleaseJob(rctx, job, w.now()); err != nil && !errors.Is(err, store.ErrLeaseLost) {
		return Released, fmt.Errorf("give job %d back: %w", job.ID, err)
	}
	log.Info("shutdown: job given back")
	return Released, nil
}

// timeoutError is an attempt that ran out of time. It is retried: a busy
// machine may be faster next time.
type timeoutError struct{ limit time.Duration }

func (e *timeoutError) Error() string {
	return fmt.Sprintf("Processing took longer than the limit of %s.", e.limit)
}

// failAttempt ends a failed attempt: a permanent error or the last attempt
// fails the job, any other error queues it again after the backoff.
func (w *Worker) failAttempt(ctx context.Context, job *store.Job, err error, log *slog.Logger) (Outcome, error) {
	msg := errorMessage(err)
	var te *timeoutError
	if errors.As(err, &te) {
		msg = te.Error()
	}
	attrs := []any{"err", err}
	var tool *ToolError
	if errors.As(err, &tool) && tool.Stderr != "" {
		attrs = append(attrs, "stderr", tool.Stderr)
	}
	var pe *PermanentError
	if errors.As(err, &pe) || job.Attempts >= job.MaxAttempts {
		log.Warn("processing failed", attrs...)
		if err := w.Store.FailJob(ctx, job, w.now(), msg); err != nil {
			return Failed, w.leaseErr(job, err)
		}
		return Failed, nil
	}
	log.Warn("processing attempt failed; retrying later", attrs...)
	if err := w.Store.RetryJob(ctx, job, w.now().Add(Backoff(job.Attempts)), msg); err != nil {
		return Retry, w.leaseErr(job, err)
	}
	return Retry, nil
}

// leaseErr reports a failure to record an attempt's end. A lost lease is
// no error: the reaper or another worker has the job.
func (w *Worker) leaseErr(job *store.Job, err error) error {
	if errors.Is(err, store.ErrLeaseLost) {
		return nil
	}
	return fmt.Errorf("record the end of job %d: %w", job.ID, err)
}

// renewLease renews job's lease every third of it until stop closes. A
// lease that is no longer the job's cancels the attempt.
func (w *Worker) renewLease(ctx context.Context, job *store.Job, cancel context.CancelCauseFunc, stop <-chan struct{}, log *slog.Logger) {
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-w.wait(w.lease() / 3):
		}
		err := w.Store.ExtendJobLease(ctx, job, w.now().Add(w.lease()))
		if w.heartbeat != nil {
			w.heartbeat(err)
		}
		switch {
		case errors.Is(err, store.ErrLeaseLost):
			cancel(errLeaseLost)
			return
		case err != nil && ctx.Err() == nil:
			// The next renewal may work; if none does, the reaper ends
			// the attempt.
			log.Warn("renew lease", "err", err)
		}
	}
}

// errDocumentGone is a job whose document was deleted before it started.
var errDocumentGone = errors.New("document deleted")

// attempt copies the original into a fresh work directory, runs the
// pipeline on it and renders the thumbnail of the archive. A thumbnail that
// cannot be made is logged and left out: the text matters more.
func (w *Worker) attempt(ctx context.Context, job *store.Job, log *slog.Logger) (store.ProcessResult, error) {
	doc, err := w.Store.GetDocument(ctx, store.Scope{All: true}, job.DocumentID)
	if errors.Is(err, store.ErrNotFound) {
		return store.ProcessResult{}, errDocumentGone
	}
	if err != nil {
		return store.ProcessResult{}, err
	}
	dir, err := w.WorkDir.Job(job.ID)
	if err != nil {
		return store.ProcessResult{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "original")
	if err := w.copyOriginal(doc.OriginalSHA256, path); err != nil {
		return store.ProcessResult{}, err
	}
	res, err := w.Pipeline.Run(ctx, Input{Dir: dir, Path: path, MIME: doc.OriginalMIME})
	if err != nil {
		return store.ProcessResult{}, err
	}
	out := store.ProcessResult{
		PageCount: res.PageCount, Text: res.Text, TextSource: res.TextSource, Note: res.Note,
	}
	archive := res.Archive
	if archive == "" {
		// The original is a PDF that serves as its own archive.
		archive = path
		out.ArchiveSHA256, out.ArchiveSize = doc.OriginalSHA256, doc.OriginalSize
	} else if out.ArchiveSHA256, out.ArchiveSize, err = w.putFile(res.Archive, "archive"); err != nil {
		return store.ProcessResult{}, err
	}
	thumb, err := w.Pipeline.Thumbnail(ctx, dir, archive)
	switch {
	case err != nil && ctx.Err() != nil:
		return store.ProcessResult{}, err
	case err != nil:
		attrs := []any{"err", err}
		var tool *ToolError
		if errors.As(err, &tool) && tool.Stderr != "" {
			attrs = append(attrs, "stderr", tool.Stderr)
		}
		log.Warn("no thumbnail; the document is stored without one", attrs...)
	default:
		if out.ThumbnailSHA256, _, err = w.putFile(thumb, "thumbnail"); err != nil {
			return store.ProcessResult{}, err
		}
	}
	return out, nil
}

// putFile stores the file at path (the archive PDF or the thumbnail) in
// the blob store, durably, before the result is committed. If the commit
// does not happen (the document was deleted, the lease lost), the blob is
// an orphan for the cleanup; nothing refers to it.
func (w *Worker) putFile(path, what string) (hash string, size int64, err error) {
	f, err := os.Open(path) //nolint:gosec // in the job's work directory
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", what, err)
	}
	defer func() { _ = f.Close() }()
	b, _, err := w.Blobs.Put(f)
	if err != nil {
		return "", 0, fmt.Errorf("store %s: %w", what, err)
	}
	return b.SHA256, b.Size, nil
}

// msgDamagedOriginal is a stored original whose content does not match its
// name.
const msgDamagedOriginal = "The stored file is damaged: its content does not match its checksum."

// copyOriginal copies the blob hash to path and checks its hash on the way.
func (w *Worker) copyOriginal(hash, path string) error {
	if !blobstore.IsHash(hash) {
		return permanent(msgDamagedOriginal)
	}
	src, err := w.Blobs.OpenHash(hash)
	if err != nil {
		return fmt.Errorf("open original: %w", err)
	}
	defer func() { _ = src.Close() }()
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // path in the job's work directory
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(dst, h), src); err != nil {
		_ = dst.Close()
		return fmt.Errorf("copy original: %w", err)
	}
	if err := dst.Close(); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != hash {
		return permanent(msgDamagedOriginal)
	}
	return nil
}

// Run processes jobs until ctx ends, Workers at a time. It wakes on the
// notifications of new jobs, polls at least every minute, and runs the
// reaper and the cleanup of old jobs every minute. At shutdown, running
// jobs are given back; Run returns when they are.
func (w *Worker) Run(ctx context.Context) {
	n := max(w.Workers, 1)
	wake := make([]chan struct{}, n)
	for i := range wake {
		wake[i] = make(chan struct{}, 1)
	}
	wakeAll := func() {
		for _, c := range wake {
			select {
			case c <- struct{}{}:
			default:
			}
		}
	}
	var wg sync.WaitGroup
	wg.Go(func() { w.listen(ctx, wakeAll) })
	wg.Go(func() { w.housekeeping(ctx) })
	for i := range n {
		wg.Go(func() { w.loop(ctx, wake[i]) })
	}
	wg.Wait()
}

// loop processes jobs until none is due, then waits for a notification or
// the poll interval.
func (w *Worker) loop(ctx context.Context, wake <-chan struct{}) {
	for ctx.Err() == nil {
		outcome, err := w.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			w.Log.Error("processing queue", "err", err)
		}
		if err == nil && outcome != NoJob {
			continue
		}
		select {
		case <-ctx.Done():
		case <-wake:
		case <-w.wait(pollInterval):
		}
	}
}

// listen wakes the workers on each notification of new jobs, on a
// connection of its own, and listens again after the connection failed.
func (w *Worker) listen(ctx context.Context, wake func()) {
	for ctx.Err() == nil {
		l, err := w.Store.ListenJobs(ctx)
		if err == nil {
			if w.listening != nil {
				w.listening()
			}
			wake() // jobs queued while nobody listened
			for err == nil {
				if err = l.Wait(ctx); err == nil {
					wake()
				}
			}
			l.Close()
		}
		if ctx.Err() != nil {
			return
		}
		w.Log.Warn("listen for new jobs; polling until it works again", "err", err)
		select {
		case <-ctx.Done():
		case <-w.wait(listenRetry):
		}
	}
}

// housekeeping runs the reaper and deletes old done jobs, at start and
// then every minute.
func (w *Worker) housekeeping(ctx context.Context) {
	for {
		w.Housekeeping(ctx)
		select {
		case <-ctx.Done():
			return
		case <-w.wait(housekeepingInterval):
		}
	}
}

// Housekeeping ends the attempts whose lease expired (they count) and
// deletes done jobs older than a week.
func (w *Worker) Housekeeping(ctx context.Context) {
	now := w.now()
	requeued, failed, err := w.Store.ReapJobs(ctx, now, Backoff, msgLeaseExpired)
	switch {
	case err != nil && ctx.Err() == nil:
		w.Log.Error("reap expired jobs", "err", err)
	case requeued+failed > 0:
		w.Log.Warn("ended processing attempts whose lease expired", "requeued", requeued, "failed", failed)
	}
	if _, err := w.Store.DeleteDoneJobs(ctx, now.Add(-keepDone)); err != nil && ctx.Err() == nil {
		w.Log.Warn("delete old jobs", "err", err)
	}
}
