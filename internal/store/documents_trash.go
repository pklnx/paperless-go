package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pklnx/paperless-go/internal/store/db"
)

// The trash (#12, part 1; docs/development/architecture.md, "Trash"). A
// deleted document keeps its row with deleted_at set: tags, ASN, the
// unique original hash and the search vectors stay, so a restore only
// clears two columns and never conflicts. It leaves every list, search,
// count and bulk match and shows only in the trash, until it is due for
// purge: when its purge was asked for (purge_at, "delete permanently") or
// the retention passed since deleted_at. From then on it is hidden from
// every read, and the sweeper (internal/sweep) deletes the row through
// PurgeDueDocuments and queues its files for removal.
//
// Every decision uses one clock, the store's Now, and the store's
// TrashRetention: the writes (deleted_at, purge_at), the reads' "not yet
// due" condition and, through its arguments, the sweeper's scan. So "due"
// means the same in the API and in the sweeper, and tests move a clock
// instead of waiting.

// DefaultTrashRetention is how long a trashed document stays when
// PAPERLESS_GO_TRASH_RETENTION is not set: 30 days.
const DefaultTrashRetention = 720 * time.Hour

// blobGrace is how long a queued file removal waits (blob_removals.due_at):
// a writer that stored the same file meanwhile takes the row away (#12,
// part 2).
const blobGrace = time.Hour

// trashClock returns the time of a trash decision, in the database's
// precision (microseconds, no monotonic reading), and the retention.
func (s *Store) trashClock() (time.Time, time.Duration) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	return now().Round(0).Truncate(time.Microsecond), max(s.TrashRetention, 0).Truncate(time.Microsecond)
}

// trashState is where a document is.
type trashState int

const (
	stateLive    trashState = iota
	stateTrashed            // in the trash, not yet due for purge
	stateDue                // due for purge: hidden, the sweeper deletes it
)

// trashStateOf decides a document's state with the expression of the SQL
// reads: due when coalesce(purge_at, deleted_at + retention) <= now.
func trashStateOf(deletedAt, purgeAt *time.Time, now time.Time, ret time.Duration) trashState {
	if deletedAt == nil {
		return stateLive
	}
	if at := purgeTime(deletedAt, purgeAt, ret); !at.After(now) {
		return stateDue
	}
	return stateTrashed
}

// purgeTime is when a trashed document is due for purge: purge_at, else
// deleted_at plus the retention. nil for a live document.
func purgeTime(deletedAt, purgeAt *time.Time, ret time.Duration) *time.Time {
	switch {
	case deletedAt == nil:
		return nil
	case purgeAt != nil:
		t := *purgeAt
		return &t
	default:
		t := deletedAt.Add(ret)
		return &t
	}
}

// BulkAction is what a bulk change does with its documents.
type BulkAction string

// Bulk actions. BulkUpdate (also "") sets fields (BulkChange); the others
// take no fields: BulkTrash moves live documents to the trash, BulkRestore
// brings trashed ones back, BulkPurge asks for their permanent deletion.
const (
	BulkUpdate  BulkAction = "update"
	BulkTrash   BulkAction = "trash"
	BulkRestore BulkAction = "restore"
	BulkPurge   BulkAction = "purge"
)

// normal returns the action with "" as BulkUpdate, and ok false for an
// unknown one.
func (a BulkAction) normal() (BulkAction, bool) {
	switch a {
	case "", BulkUpdate:
		return BulkUpdate, true
	case BulkTrash, BulkRestore, BulkPurge:
		return a, true
	}
	return "", false
}

// onTrash reports whether the action works on trashed documents (restore,
// purge) rather than on live ones (update, trash).
func (a BulkAction) onTrash() bool { return a == BulkRestore || a == BulkPurge }

// checkState returns the error for a locked document in state st that the
// action a may not touch: a due document is ErrNotFound (it is hidden), a
// trashed one ErrInTrash for update and trash, a live one ErrNotInTrash
// for restore and purge.
func (a BulkAction) checkState(st trashState) error {
	switch {
	case st == stateDue:
		return ErrNotFound
	case a.onTrash() && st == stateLive:
		return ErrNotInTrash
	case !a.onTrash() && st == stateTrashed:
		return ErrInTrash
	}
	return nil
}

// applyTrash runs the query of a trash action on the locked documents ids.
func applyTrash(ctx context.Context, q *db.Queries, a BulkAction, ids []pgtype.UUID, now time.Time) error {
	switch a {
	case BulkTrash:
		return q.TrashDocuments(ctx, db.TrashDocumentsParams{Now: now, Ids: ids})
	case BulkRestore:
		return q.RestoreDocuments(ctx, ids)
	default:
		return q.PurgeDocuments(ctx, db.PurgeDocumentsParams{Now: now, Ids: ids})
	}
}

// trashOne applies a trash action to one document of the scope, in one
// transaction: it reads the owner, takes the owner's gate and write lock
// (ownerTx), locks the row, checks its state (BulkAction.checkState) and
// writes. The audit entry of ctx gets the document as its target and, for
// an admin's change of another user's document, the owner's ID.
func (s *Store) trashOne(ctx context.Context, sc Scope, id string, a BulkAction) error {
	u, ok := parseID(id)
	if !ok {
		return ErrNotFound
	}
	o, err := s.q.DocumentOwner(ctx, db.DocumentOwnerParams{ID: u, Owner: sc.owner()})
	if err != nil {
		return mapErr(err)
	}
	now, ret := s.trashClock()
	return mapErr(s.ownerTx(ctx, []int64{o}, func(ctx context.Context, t *writeTx) error {
		locked, err := t.q.LockDocument(ctx, db.LockDocumentParams{ID: u, Owner: sc.owner()})
		if err != nil {
			return err
		}
		if err := t.holds(locked.OwnerID); err != nil {
			return err
		}
		afterLock(ctx)
		if err := a.checkState(trashStateOf(locked.DeletedAt, locked.PurgeAt, now, ret)); err != nil {
			return err
		}
		if err := applyTrash(ctx, t.q, a, []pgtype.UUID{u}, now); err != nil {
			return err
		}
		if e := auditFrom(ctx); e != nil && e.TargetType == TargetDocument {
			e.TargetID = idString(u)
			if locked.OwnerID != sc.UserID {
				e.Detail("ownerId", locked.OwnerID)
			}
		}
		return nil
	}))
}

// TrashDocument moves a live document of the scope to the trash. Errors:
// ErrNotFound outside the scope or for a document due for purge,
// ErrInTrash for one already in the trash. modified_at stays: nothing of
// the document changed. With an entry from WithAudit (target document) it
// is written in the same transaction, with the document's ID and, for an
// admin's change of another user's document, ownerId.
func (s *Store) TrashDocument(ctx context.Context, sc Scope, id string) error {
	return s.trashOne(ctx, sc, id, BulkTrash)
}

// RestoreDocument brings a trashed document of the scope back and returns
// it. Errors: ErrNotFound outside the scope and for a document already due
// for purge (it is hidden; the sweeper removes it), ErrNotInTrash for a
// live one. Tags, ASN, favourite, inbox and modified_at are as before the
// trash. Audit as TrashDocument.
func (s *Store) RestoreDocument(ctx context.Context, sc Scope, id string) (*Document, error) {
	if err := s.trashOne(ctx, sc, id, BulkRestore); err != nil {
		return nil, err
	}
	return s.GetDocument(ctx, sc, id)
}

// PurgeDocument asks for the permanent deletion of a trashed document of
// the scope: it sets purge_at, which makes the document due at once, so it
// is hidden from then on and the sweeper deletes the row and its files.
// The request never waits for that. Errors as RestoreDocument; audit as
// TrashDocument.
func (s *Store) PurgeDocument(ctx context.Context, sc Scope, id string) error {
	return s.trashOne(ctx, sc, id, BulkPurge)
}

// PurgeResult is what one PurgeDueDocuments call did.
type PurgeResult struct {
	// Scanned is the number of due documents the scan found (at most the
	// limit); Purged those deleted. A document restored between the scan
	// and the lock is scanned but not purged.
	Scanned, Purged int
	// Retention is the number of the purged documents whose retention
	// passed (the others' purge was asked for).
	Retention int
}

// PurgeDueDocuments deletes up to limit documents that are due for purge
// at now with the retention ret, the sweeper's row pass. It scans on the
// pool, without a lock, then per owner (ascending) takes one owner
// transaction (ownerTx): it locks the scanned documents, checks again
// that each is still due (a restore may have come first), deletes those
// with their tag links, deadlines and jobs (cascades), queues their
// original, archive and thumbnail hashes in blob_removals (due after
// blobGrace) and writes a document.purge entry by the system for each
// document whose retention passed (a requested purge was audited when it
// was asked for). Between the owners' transactions it gives up when ctx
// ends. Callers loop until Scanned is 0.
func (s *Store) PurgeDueDocuments(ctx context.Context, now time.Time, ret time.Duration, limit int) (PurgeResult, error) {
	var res PurgeResult
	now, ret = now.Round(0).Truncate(time.Microsecond), max(ret, 0).Truncate(time.Microsecond)
	rows, err := s.q.DueDocuments(ctx, db.DueDocumentsParams{RetentionUs: ret.Microseconds(), Now: now, Lim: clampInt32(limit)})
	if err != nil {
		return res, err
	}
	res.Scanned = len(rows)
	if len(rows) == 0 {
		return res, nil
	}
	beforeLock(ctx)
	byOwner := map[int64][]pgtype.UUID{}
	var owners []int64
	for _, r := range rows {
		if _, ok := byOwner[r.OwnerID]; !ok {
			owners = append(owners, r.OwnerID)
		}
		byOwner[r.OwnerID] = append(byOwner[r.OwnerID], r.ID)
	}
	// The scan is ordered by owner, so owners is ascending.
	for _, o := range owners {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		purged, retention, err := s.purgeOwner(ctx, o, byOwner[o], now, ret)
		if err != nil {
			return res, mapErr(err)
		}
		res.Purged += purged
		res.Retention += retention
	}
	return res, nil
}

// purgeOwner is PurgeDueDocuments' transaction for the scanned documents
// ids of one owner.
func (s *Store) purgeOwner(ctx context.Context, owner int64, ids []pgtype.UUID, now time.Time, ret time.Duration) (purged, retention int, err error) {
	noPartition := false
	defer func() {
		// After the transaction, which held a connection: create the
		// partition for the next run.
		if noPartition {
			_, _ = s.EnsureAuditPartitions(context.WithoutCancel(ctx), 1)
		}
	}()
	err = s.ownerTx(ctx, []int64{owner}, func(ctx context.Context, t *writeTx) error {
		locked, err := t.q.LockDocuments(ctx, db.LockDocumentsParams{Ids: ids, Owner: &owner})
		if err != nil {
			return err
		}
		afterLock(ctx)
		var due []pgtype.UUID
		for _, l := range locked {
			if err := t.holds(l.OwnerID); err != nil {
				return err
			}
			if trashStateOf(l.DeletedAt, l.PurgeAt, now, ret) == stateDue {
				due = append(due, l.ID)
			}
		}
		if len(due) == 0 {
			return nil
		}
		deleted, err := t.q.DeleteDueDocuments(ctx, due)
		if err != nil {
			return err
		}
		var hashes []string
		var entries []*AuditEntry
		for _, d := range deleted {
			hashes = append(hashes, d.OriginalSha256)
			for _, h := range []*string{d.ArchiveSha256, d.ThumbnailSha256} {
				if h != nil {
					hashes = append(hashes, *h)
				}
			}
			if d.PurgeAt == nil {
				e := &AuditEntry{Actor: ActorSystem, Action: "document.purge"}
				e.SetTarget(TargetDocument, idString(d.ID), "")
				e.Detail("ownerId", d.OwnerID)
				e.Detail("reason", "retention")
				entries = append(entries, e)
			}
		}
		if err := t.q.QueueBlobRemovals(ctx, db.QueueBlobRemovalsParams{DueAt: now.Add(blobGrace), Hashes: hashes}); err != nil {
			return err
		}
		if len(entries) > 0 {
			base := &AuditEntry{Actor: ActorSystem}
			if err := insertAuditEntries(ctx, t.q, base, entries, nil); err != nil {
				noPartition = missingPartition(err)
				return fmt.Errorf("%w: %w", ErrAuditUnavailable, err)
			}
		}
		purged, retention = len(deleted), len(entries)
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return purged, retention, nil
}
