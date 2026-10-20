package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/store"
)

// ResetStorageVersion removes (to == "") or sets an agent's recorded
// pi-durable storage version (the reset-storage-version command; the upgrade
// check, docs/custom-agents.md). The database must be migrated.
func ResetStorageVersion(ctx context.Context, cfg config.Config, agentID uuid.UUID, to string) (agents.StorageVersionReset, error) {
	if cfg.DatabaseURL == "" {
		return agents.StorageVersionReset{}, errors.New("DATABASE_URL is required")
	}
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return agents.StorageVersionReset{}, err
	}
	defer pool.Close()
	st, err := store.Migrations(ctx, pool)
	if err != nil {
		return agents.StorageVersionReset{}, err
	}
	if st.Pending || st.Current != st.Latest {
		return agents.StorageVersionReset{}, fmt.Errorf("the database is at migration version %d, this binary has %d: run pi-durable-platform migrate first", st.Current, st.Latest)
	}
	return agents.ResetStorageVersion(ctx, pool, agentID, to)
}
