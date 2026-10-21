package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// mapErr's mapping of errors the store's own calls cannot provoke on
// purpose: not-null violations, and the owner foreign key reported as
// restrict_violation (23001) by PostgreSQL 18 and foreign_key_violation
// (23503) by older versions.
func TestMapErr(t *testing.T) {
	pg := func(code, constraint, column string) error {
		return &pgconn.PgError{Code: code, ConstraintName: constraint, ColumnName: column, Message: "raw message"}
	}
	other := pg("23503", "something_else_fkey", "")
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"not null", pg("23502", "", "original_mime"), &ValidationError{Field: "original_mime"}},
		{"unknown check", pg("23514", "documents_source_check", ""), &ValidationError{Constraint: "documents_source_check"}},
		{"owner fk 23503", pg("23503", "documents_owner_id_fkey", ""), ErrUnknownUser},
		{"owner fk 23001", pg("23001", "tags_owner_id_fkey", ""), ErrUnknownUser},
		{"wrapped owner fk", fmt.Errorf("insert: %w", pg("23503", "deadlines_owner_id_fkey", "")), ErrUnknownUser},
		{"known fk", pg("23503", "documents_correspondent_fkey", ""), ErrOwnerMismatch},
		{"known check", pg("23514", "tags_parent_not_self", ""), ErrTagCycle},
		{"other fk", other, other},
		{"other restrict", pg("23001", "something_else_fkey", ""), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mapErr(tc.err)
			var want *ValidationError
			switch {
			case errors.As(tc.want, &want):
				var v *ValidationError
				if !errors.As(got, &v) || *v != *want || !errors.Is(got, ErrInvalid) {
					t.Errorf("mapErr = %#v, want %#v", got, want)
				}
			case tc.want == nil:
				if !errors.Is(got, tc.err) || got.Error() != tc.err.Error() {
					t.Errorf("mapErr = %v, want it unchanged", got)
				}
			default:
				if !errors.Is(got, tc.want) {
					t.Errorf("mapErr = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// An audit failure stays ErrAuditUnavailable even when its cause is a
// check violation.
func TestMapErrKeepsAuditUnavailable(t *testing.T) {
	err := fmt.Errorf("%w: %w", ErrAuditUnavailable, &pgconn.PgError{Code: "23514", Message: "no partition of relation"})
	if got := mapErr(err); !errors.Is(got, ErrAuditUnavailable) {
		t.Errorf("mapErr = %v, want ErrAuditUnavailable", got)
	}
}
