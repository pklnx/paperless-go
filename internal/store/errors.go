package store

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrNotFound means the row does not exist or lies outside the scope.
	// The two are not told apart, so a caller never learns that another
	// user's row exists.
	ErrNotFound = errors.New("store: not found")

	// ErrDuplicate means the owner already has a document with the same
	// original file. The error is a *DuplicateError naming that document.
	ErrDuplicate = errors.New("store: duplicate document")

	// ErrNameTaken means the owner already has a correspondent, document
	// type or sibling tag with this name (ignoring case), or a user with
	// this name exists.
	ErrNameTaken = errors.New("store: name taken")

	// ErrASNTaken means the owner already has a document with this
	// archive serial number.
	ErrASNTaken = errors.New("store: archive serial number taken")

	// ErrTagCycle means the new parent would make a tag its own parent or
	// its own ancestor.
	ErrTagCycle = errors.New("store: tag cycle")

	// ErrOwnerMismatch means a linked tag, parent tag, correspondent,
	// document type or (for a deadline) document is not one of the owner's
	// own. It covers both "belongs
	// to another owner" and "does not exist" on purpose: telling them apart
	// would reveal that another user's row exists. Do not split it.
	ErrOwnerMismatch = errors.New("store: linked row does not belong to the owner")

	// ErrTagNameConflict means a tag cannot be deleted because one of its
	// children would become a root tag with the name of an existing root
	// tag. The error is a *TagNameConflictError naming that child. Nothing
	// is renamed or merged.
	ErrTagNameConflict = errors.New("store: a child tag would collide with a root tag")

	// ErrInvalid means a value was refused: by the store's own input
	// validation or by a database check or not-null constraint the store
	// has no more specific error for. The error is a *ValidationError; its
	// message never contains SQL or the refused value.
	ErrInvalid = errors.New("store: invalid value")

	// ErrRetry means PostgreSQL aborted the change because it deadlocked
	// with a concurrent one (SQLSTATE 40P01). Nothing was written; the
	// same call can be tried again. Store writers of documents, tags and
	// the other owner tables do not deadlock with each other (they take
	// the owner's write lock first, locking.go), so this is the net for
	// SQL outside the store and for deadlocks among jobs-only writers.
	ErrRetry = errors.New("store: concurrent change, try again")

	// ErrBusy means a write gave up waiting for another change of the
	// same owner: the bound of WithOwnerWait passed at the owner gate or
	// at the owner lock (lock_timeout, SQLSTATE 55P03 at the lock only).
	// Nothing was written; the same call can be tried again later.
	ErrBusy = errors.New("store: another change of the owner is still running")

	// ErrUnknownUser means the owner of a new row does not exist (any
	// more), for example because the user was deleted meanwhile.
	ErrUnknownUser = errors.New("store: unknown user")
)

// ValidationError is a refused value. errors.Is(err, ErrInvalid) holds.
type ValidationError struct {
	// Field names the refused input field or column, if known.
	Field string
	// Constraint names the database constraint that refused it, if any.
	Constraint string
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return "store: invalid " + e.Field
	}
	return "store: invalid value"
}

// Is makes errors.Is(err, ErrInvalid) true.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }

// invalid returns a *ValidationError for an input field.
func invalid(field string) error { return &ValidationError{Field: field} }

// DuplicateError is returned by CreateDocument when the owner already has a
// document with the same original file. errors.Is(err, ErrDuplicate) holds.
type DuplicateError struct {
	// ExistingID is the document that has the file. It is empty in the
	// rare case that this document was deleted right after the collision.
	ExistingID string
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("store: duplicate of document %s", e.ExistingID)
}

// Is makes errors.Is(err, ErrDuplicate) true.
func (e *DuplicateError) Is(target error) bool { return target == ErrDuplicate }

// TagNameConflictError is returned by DeleteTag when a child of the tag
// would collide with a root tag of the same name. errors.Is(err,
// ErrTagNameConflict) holds.
type TagNameConflictError struct {
	ChildID   string
	ChildName string
}

func (e *TagNameConflictError) Error() string {
	return fmt.Sprintf("store: child tag %q (%s) would collide with a root tag of the same name", e.ChildName, e.ChildID)
}

// Is makes errors.Is(err, ErrTagNameConflict) true.
func (e *TagNameConflictError) Is(target error) bool { return target == ErrTagNameConflict }

// errTagCycleCode is the SQLSTATE the tags_no_cycle trigger raises.
const errTagCycleCode = "PGT01"

// mapErr turns PostgreSQL errors into the store's errors. Check and
// not-null violations without a more specific error become a
// *ValidationError (ErrInvalid), and a missing owner ErrUnknownUser. Other
// errors are returned unchanged. Audit errors stay ErrAuditUnavailable.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if errors.Is(err, ErrAuditUnavailable) {
		return err
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "40P01": // deadlock_detected
		return ErrRetry
	case errTagCycleCode:
		return ErrTagCycle
	case "23514": // check_violation
		switch pgErr.ConstraintName {
		case "tags_parent_not_self":
			return ErrTagCycle
		case "saved_views_filter_object":
			return ErrInvalidFilter
		}
		return &ValidationError{Constraint: pgErr.ConstraintName}
	case "23502": // not_null_violation
		return &ValidationError{Field: pgErr.ColumnName}
	case "23505": // unique_violation
		switch pgErr.ConstraintName {
		case "users_name_key", "correspondents_owner_name_key", "document_types_owner_name_key", "tags_owner_parent_name_key":
			return ErrNameTaken
		case "documents_owner_asn_key":
			return ErrASNTaken
		}
	case "23503": // foreign_key_violation
		switch pgErr.ConstraintName {
		case "tags_parent_fkey", "documents_correspondent_fkey", "documents_document_type_fkey", "document_tags_tag_fkey",
			"deadlines_document_fkey":
			return ErrOwnerMismatch
		case "document_tags_document_fkey":
			return ErrNotFound
		}
		if isOwnerFKey(pgErr.ConstraintName) {
			return ErrUnknownUser
		}
	case "23001": // restrict_violation (PostgreSQL 18, for a racing user delete)
		if isOwnerFKey(pgErr.ConstraintName) {
			return ErrUnknownUser
		}
	}
	return err
}

// isOwnerFKey reports whether constraint is the foreign key from a row's
// owner_id to users.
func isOwnerFKey(constraint string) bool { return strings.HasSuffix(constraint, "_owner_id_fkey") }

// isUniqueViolation reports whether err is a unique violation of constraint.
func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
