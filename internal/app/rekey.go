package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/rekey"
	"github.com/ncecere/pi-durable-platform/internal/store"
)

// Rekey re-seals every stored secret under ENCRYPTION_KEY (the rekey
// command): with ENCRYPTION_KEY_PREVIOUS set when values are still sealed
// with it. The database must be migrated.
func Rekey(ctx context.Context, cfg config.Config, opts rekey.Options) (rekey.Result, error) {
	if cfg.DatabaseURL == "" {
		return rekey.Result{}, errors.New("DATABASE_URL is required")
	}
	if cfg.EncryptionKey == "" {
		return rekey.Result{}, errors.New("ENCRYPTION_KEY is required (and ENCRYPTION_KEY_PREVIOUS while values are still sealed with the old key)")
	}
	box, err := secretBox(cfg)
	if err != nil {
		return rekey.Result{}, err
	}
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return rekey.Result{}, err
	}
	defer pool.Close()
	st, err := store.Migrations(ctx, pool)
	if err != nil {
		return rekey.Result{}, err
	}
	if st.Pending || st.Current != st.Latest {
		return rekey.Result{}, fmt.Errorf("the database is at migration version %d, this binary has %d: run pi-durable-platform migrate first", st.Current, st.Latest)
	}
	return rekey.Run(ctx, pool, box, opts)
}
