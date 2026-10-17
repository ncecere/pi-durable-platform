package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/ncecere/pi-durable-platform/migrations"
)

// MigrationState compares the database's schema with the migrations built
// into this binary, without changing anything (`grounded doctor`).
type MigrationState struct {
	Current int64 // highest applied version (0: none)
	Latest  int64 // highest version in this binary
	Pending bool  // some migration in this binary is not applied
}

// Migrations reads the migration state. A database never migrated has
// Current 0 and every migration pending.
func Migrations(ctx context.Context, pool *pgxpool.Pool) (MigrationState, error) {
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('goose_db_version') IS NOT NULL").Scan(&exists); err != nil {
		return MigrationState{}, err
	}
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return MigrationState{}, fmt.Errorf("goose: %w", err)
	}
	sources := provider.ListSources()
	var st MigrationState
	if len(sources) > 0 {
		st.Latest = sources[len(sources)-1].Version
	}
	if !exists {
		st.Pending = st.Latest > 0
		return st, nil
	}
	if st.Current, err = provider.GetDBVersion(ctx); err != nil {
		return MigrationState{}, fmt.Errorf("migration version: %w", err)
	}
	if st.Pending, err = provider.HasPending(ctx); err != nil {
		return MigrationState{}, fmt.Errorf("pending migrations: %w", err)
	}
	return st, nil
}
