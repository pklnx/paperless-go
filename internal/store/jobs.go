package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pklnx/paperless-go/internal/store/db"
)

// Job kinds and states.
const (
	JobProcess = "process"

	JobQueued  = "queued"
	JobRunning = "running"
	JobDone    = "done"
	JobFailed  = "failed"
)

// JobsChannel is the PostgreSQL notification channel that a transaction
// queueing jobs notifies when it commits.
const JobsChannel = "paperless_go_jobs"

// Text sources that processing writes. "PDF-Text" is also one of the app's
// values; "Tesseract" is the server's own.
const (
	TextSourcePDF       = "PDF-Text"
	TextSourceTesseract = "Tesseract"
)

// ownTextSources are the text sources processing may overwrite: text
// from an import (for example "Mistral OCR") or an edit stays.
var ownTextSources = []string{TextSourcePDF, TextSourceTesseract}

// ErrLeaseLost means a job is no longer the caller's: its lease expired
// and the reaper put it back, or it was given to another worker. The
// caller drops its result.
var ErrLeaseLost = errors.New("store: job lease lost")

// Job is one entry of the processing queue.
type Job struct {
	ID          int64
	DocumentID  string
	Kind        string
	State       string
	Attempts    int32 // started attempts, including this one while running
	MaxAttempts int32
	RunAfter    time.Time
	LockedUntil *time.Time
	LastError   string
	CreatedAt   time.Time
	FinishedAt  *time.Time

	lease pgtype.UUID // the claimer's lease token
}

func jobFromDB(j db.Job) *Job {
	return &Job{
		ID: j.ID, DocumentID: idString(j.DocumentID), Kind: j.Kind, State: j.State,
		Attempts: j.Attempts, MaxAttempts: j.MaxAttempts, RunAfter: j.RunAfter,
		LockedUntil: j.LockedUntil, LastError: j.LastError, CreatedAt: j.CreatedAt,
		FinishedAt: j.FinishedAt, lease: j.LeaseToken,
	}
}

// enqueue queues a process job for each document that has no queued or
// running one, and notifies the workers at commit if it queued any.
func enqueue(ctx context.Context, q *db.Queries, ids []pgtype.UUID) (int64, error) {
	n, err := q.EnqueueJobs(ctx, ids)
	if err != nil {
		return 0, err
	}
	return n, notifyIf(ctx, q, n)
}

func notifyIf(ctx context.Context, q *db.Queries, n int64) error {
	if n == 0 {
		return nil
	}
	return q.NotifyJobs(ctx)
}

// EnqueueDocument queues the document for processing unless it is queued
// or running already; queued reports whether it was queued now.
func (s *Store) EnqueueDocument(ctx context.Context, id string) (queued bool, err error) {
	u, ok := parseID(id)
	if !ok {
		return false, ErrNotFound
	}
	err = s.inTx(ctx, func(q *db.Queries) error {
		ids, err := q.ExistingDocumentIDs(ctx, []pgtype.UUID{u})
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return ErrNotFound
		}
		n, err := enqueue(ctx, q, ids)
		queued = n > 0
		return err
	})
	return queued, err
}

// ReprocessSelection names the documents Reprocess queues: every document,
// those whose latest job failed, or the listed IDs.
type ReprocessSelection struct {
	All    bool
	Failed bool
	IDs    []string
}

// Reprocess queues the selected documents for processing again, in one
// transaction (with the audit entry from WithAudit, which gets the detail
// "queued"). Documents that are queued or running already are left alone.
// An ID of no document gives an error wrapping ErrNotFound that names it,
// and nothing is queued.
func (s *Store) Reprocess(ctx context.Context, sel ReprocessSelection) (queued int64, err error) {
	var ids []pgtype.UUID
	if !sel.All && !sel.Failed {
		if len(sel.IDs) == 0 {
			return 0, errors.New("store: no documents selected")
		}
		var bad []string
		seen := make(map[[16]byte]bool, len(sel.IDs))
		for _, id := range sel.IDs {
			u, ok := parseID(id)
			if !ok {
				bad = append(bad, id)
				continue
			}
			if !seen[u.Bytes] {
				seen[u.Bytes] = true
				ids = append(ids, u)
			}
		}
		if len(bad) > 0 {
			return 0, fmt.Errorf("%w: no document %s", ErrNotFound, strings.Join(bad, ", "))
		}
	}
	err = s.inTx(ctx, func(q *db.Queries) error {
		var err error
		switch {
		case sel.All:
			queued, err = q.EnqueueAllDocuments(ctx)
		case sel.Failed:
			queued, err = q.EnqueueFailedDocuments(ctx)
		default:
			var found []pgtype.UUID
			if found, err = q.ExistingDocumentIDs(ctx, ids); err != nil {
				return err
			}
			if missing := missingIDs(ids, found); len(missing) > 0 {
				return fmt.Errorf("%w: no document %s", ErrNotFound, strings.Join(missing, ", "))
			}
			queued, err = q.EnqueueJobs(ctx, ids)
		}
		if err != nil {
			return err
		}
		if e := auditFrom(ctx); e != nil {
			e.Detail("queued", queued)
		}
		return notifyIf(ctx, q, queued)
	})
	if err != nil {
		return 0, err
	}
	return queued, nil
}

func missingIDs(want, found []pgtype.UUID) []string {
	have := make(map[[16]byte]bool, len(found))
	for _, u := range found {
		have[u.Bytes] = true
	}
	var out []string
	for _, u := range want {
		if !have[u.Bytes] {
			out = append(out, idString(u))
		}
	}
	return out
}

// newLeaseToken returns a random version 4 UUID.
func newLeaseToken() (pgtype.UUID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return pgtype.UUID{}, err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return pgtype.UUID{Bytes: b, Valid: true}, nil
}

// ClaimJob takes the oldest queued job due at now and leases it until
// now+lease; jobs other workers are claiming are skipped. It counts the
// attempt. Without a due job it returns nil, nil.
func (s *Store) ClaimJob(ctx context.Context, now time.Time, lease time.Duration) (*Job, error) {
	token, err := newLeaseToken()
	if err != nil {
		return nil, err
	}
	until := now.Add(lease)
	j, err := s.q.ClaimJob(ctx, db.ClaimJobParams{LeaseToken: token, LockedUntil: &until, Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return jobFromDB(j), nil
}

// leaseResult turns the row count of a lease-checked update into
// ErrLeaseLost when no row matched.
func leaseResult(n int64, err error) error {
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLeaseLost
	}
	return nil
}

// ExtendJobLease moves the end of j's lease to until (the heartbeat).
func (s *Store) ExtendJobLease(ctx context.Context, j *Job, until time.Time) error {
	err := leaseResult(s.q.ExtendJobLease(ctx, db.ExtendJobLeaseParams{LockedUntil: &until, ID: j.ID, LeaseToken: j.lease}))
	if err == nil {
		j.LockedUntil = &until
	}
	return err
}

// RetryJob ends j's attempt with msg; the job runs again from runAfter.
func (s *Store) RetryJob(ctx context.Context, j *Job, runAfter time.Time, msg string) error {
	return leaseResult(s.q.RetryJob(ctx, db.RetryJobParams{RunAfter: runAfter, LastError: msg, ID: j.ID, LeaseToken: j.lease}))
}

// FailJob ends j for good with msg.
func (s *Store) FailJob(ctx context.Context, j *Job, now time.Time, msg string) error {
	return leaseResult(s.q.FailJob(ctx, db.FailJobParams{FinishedAt: &now, LastError: msg, ID: j.ID, LeaseToken: j.lease}))
}

// ReleaseJob gives j back without counting the attempt, for a worker that
// shuts down; it is due again at now.
func (s *Store) ReleaseJob(ctx context.Context, j *Job, now time.Time) error {
	return leaseResult(s.q.ReleaseJob(ctx, db.ReleaseJobParams{RunAfter: now, ID: j.ID, LeaseToken: j.lease}))
}

// ReapJobs ends the attempts whose lease expired before now, with msg: a
// job with attempts left is due again at now+backoff(attempts), the others
// fail. The expired attempt counts.
func (s *Store) ReapJobs(ctx context.Context, now time.Time, backoff func(attempts int32) time.Duration, msg string) (requeued, failed int, err error) {
	err = s.inTx(ctx, func(q *db.Queries) error {
		rows, err := q.ExpiredJobs(ctx, &now)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Attempts >= r.MaxAttempts {
				if _, err := q.FailJob(ctx, db.FailJobParams{FinishedAt: &now, LastError: msg, ID: r.ID, LeaseToken: r.LeaseToken}); err != nil {
					return err
				}
				failed++
				continue
			}
			if _, err := q.RetryJob(ctx, db.RetryJobParams{
				RunAfter: now.Add(backoff(r.Attempts)), LastError: msg, ID: r.ID, LeaseToken: r.LeaseToken,
			}); err != nil {
				return err
			}
			requeued++
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return requeued, failed, nil
}

// ProcessResult is what processing found out about a document.
type ProcessResult struct {
	PageCount     int32
	Text          string
	TextSource    string
	ArchiveSHA256 string
	ArchiveSize   int64
	// ThumbnailSHA256 is the thumbnail's blob; "" keeps the stored one
	// (the thumbnail could not be made this time).
	ThumbnailSHA256 string
	Note            string
}

// CompleteJob stores r on j's document and marks j done, in one
// transaction, without changing the document's modified_at. The text and
// its source are replaced only when the stored text is empty or came from
// processing ("PDF-Text", "Tesseract"); text from an import or an edit
// stays. A document deleted meanwhile gives ErrNotFound (its job went with
// it), a lease that is no longer j's ErrLeaseLost; either way nothing is
// stored. Older finished jobs of the document are deleted.
func (s *Store) CompleteJob(ctx context.Context, j *Job, now time.Time, r ProcessResult) error {
	doc, ok := parseID(j.DocumentID)
	if !ok {
		return ErrNotFound
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockDocument(ctx, db.LockDocumentParams{ID: doc}); err != nil {
			return mapErr(err)
		}
		if _, err := q.LockJobLease(ctx, db.LockJobLeaseParams{ID: j.ID, LeaseToken: j.lease}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrLeaseLost
			}
			return err
		}
		var archive *string
		var size *int64
		if r.ArchiveSHA256 != "" {
			archive, size = &r.ArchiveSHA256, &r.ArchiveSize
		}
		var thumb *string
		if r.ThumbnailSHA256 != "" {
			thumb = &r.ThumbnailSHA256
		}
		if err := q.SetDocumentProcessed(ctx, db.SetDocumentProcessedParams{
			PageCount: r.PageCount, ArchiveSha256: archive, ArchiveSize: size, ProcessingNote: r.Note, ThumbnailSha256: thumb,
			OwnSources: ownTextSources, ExtractedText: r.Text, TextSource: r.TextSource, ID: doc,
		}); err != nil {
			return mapErr(err)
		}
		if err := q.FinishJob(ctx, db.FinishJobParams{FinishedAt: &now, ID: j.ID}); err != nil {
			return err
		}
		return q.DeleteOlderFinishedJobs(ctx, db.DeleteOlderFinishedJobsParams{DocumentID: doc, ID: j.ID})
	})
}

// DeleteDoneJobs deletes the done jobs that finished before before. Failed
// jobs stay: they show the error until the document is processed again.
func (s *Store) DeleteDoneJobs(ctx context.Context, before time.Time) (int64, error) {
	return s.q.DeleteDoneJobsBefore(ctx, &before)
}

// GetJob returns one job.
func (s *Store) GetJob(ctx context.Context, id int64) (*Job, error) {
	j, err := s.q.GetJob(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return jobFromDB(j), nil
}

// DocumentJobs returns a document's jobs, newest first: the first is its
// processing status.
func (s *Store) DocumentJobs(ctx context.Context, documentID string) ([]*Job, error) {
	u, ok := parseID(documentID)
	if !ok {
		return nil, ErrNotFound
	}
	rows, err := s.q.DocumentJobs(ctx, u)
	if err != nil {
		return nil, err
	}
	out := make([]*Job, len(rows))
	for i, r := range rows {
		out[i] = jobFromDB(r)
	}
	return out, nil
}

// LatestJobs returns the newest process job of each of the documents with
// these IDs, by document ID: their processing status. A document without
// one (its done job was deleted after a week) is missing from the map, as
// are malformed IDs. The caller has checked that the documents are in its
// scope.
func (s *Store) LatestJobs(ctx context.Context, documentIDs []string) (map[string]*Job, error) {
	ids := make([]pgtype.UUID, 0, len(documentIDs))
	for _, id := range documentIDs {
		if u, ok := parseID(id); ok {
			ids = append(ids, u)
		}
	}
	out := make(map[string]*Job, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.q.LatestDocumentJobs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		j := jobFromDB(r)
		out[j.DocumentID] = j
	}
	return out, nil
}

// JobCounts counts the queue at now: queued jobs that are due and those
// waiting for a retry, running and failed jobs.
type JobCounts struct {
	Due, Waiting, Running, Failed int64
}

// JobCounts counts the queue's jobs by state at now.
func (s *Store) JobCounts(ctx context.Context, now time.Time) (JobCounts, error) {
	r, err := s.q.JobCounts(ctx, now)
	if err != nil {
		return JobCounts{}, err
	}
	return JobCounts{Due: r.Due, Waiting: r.Waiting, Running: r.Running, Failed: r.Failed}, nil
}

// JobListener receives the notifications of JobsChannel on a connection of
// its own, outside the pool.
type JobListener struct {
	conn *pgx.Conn
}

// ListenJobs opens a connection that listens on JobsChannel.
func (s *Store) ListenJobs(ctx context.Context) (*JobListener, error) {
	conn, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{JobsChannel}.Sanitize()); err != nil {
		_ = conn.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	return &JobListener{conn: conn}, nil
}

// Wait blocks until a notification arrives, ctx ends or the connection
// fails. After an error the listener is unusable; close it and listen
// again.
func (l *JobListener) Wait(ctx context.Context) error {
	_, err := l.conn.WaitForNotification(ctx)
	return err
}

// Close closes the listener's connection.
func (l *JobListener) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = l.conn.Close(ctx)
}
