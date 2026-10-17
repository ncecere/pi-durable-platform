package apperr

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// A delete blocked by a foreign key is "in use", whichever code PostgreSQL gives it: 23503 (foreign_key_violation) up to
// PostgreSQL 17, and 23001 (restrict_violation) from 18 for ON DELETE RESTRICT, which most of the schema's references use.
func TestIsForeignKeyViolation(t *testing.T) {
	pg := func(code, constraint string) error { return &pgconn.PgError{Code: code, ConstraintName: constraint} }
	for _, c := range []struct {
		name       string
		err        error
		constraint string
		want       bool
	}{
		{"foreign key violation (PostgreSQL 17 and earlier)", pg("23503", "fk"), "", true},
		{"restrict violation (PostgreSQL 18)", pg("23001", "fk"), "", true},
		{"wrapped, as the store returns it", fmt.Errorf("delete model: %w", pg("23001", "fk")), "", true},
		{"a named constraint that matches", pg("23001", "embedding_profiles_model_id_fkey"), "embedding_profiles_model_id_fkey", true},
		{"a named constraint that does not", pg("23001", "other_fkey"), "embedding_profiles_model_id_fkey", false},
		{"a unique violation is not one", pg("23505", "fk"), "", false},
		{"nor is a not-null violation", pg("23502", "fk"), "", false},
		{"nor is a plain error", errors.New("boom"), "", false},
		{"nor nil", nil, "", false},
	} {
		if got := IsForeignKeyViolation(c.err, c.constraint); got != c.want {
			t.Errorf("%s: IsForeignKeyViolation = %v, want %v", c.name, got, c.want)
		}
	}
}
