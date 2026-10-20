package agents

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/mod/semver"

	"github.com/ncecere/pi-durable-platform/internal/agenthost"
	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// ShippedPiDurableVersion is the pi-durable version the platform's runtime is
// pinned to (runtime/package.json; a test fails when the two differ).
const ShippedPiDurableVersion = "1.3.0"

// recordCeiling is the lowest version the upgrade check no longer records:
// the next minor version after the one following the shipped one (1.5.0-0
// when 1.3.0 is shipped). So a runtime may report a pi-durable of the
// shipped major up to one minor version ahead (a custom image that upgraded
// first) and have it recorded; anything newer is not recorded (docs/custom-
// agents.md, Upgrades).
var recordCeiling = func() string {
	parts := strings.SplitN(ShippedPiDurableVersion, ".", 3)
	minor, err := strconv.Atoi(parts[1])
	if len(parts) != 3 || err != nil {
		panic("ShippedPiDurableVersion is not a version")
	}
	return fmt.Sprintf("v%s.%d.0-0", parts[0], minor+2)
}()

// recordable reports whether the upgrade check may record a pi-durable
// version a runtime reported: a valid version of the shipped major, at most
// one minor version ahead of the shipped one. The runtime's key is readable
// by the code its tools run, so what it reports is not trusted further: a
// version recorded too far ahead would make the platform refuse every
// genuine runtime.
func recordable(version string) bool {
	if !agenthost.ValidPiDurableVersion(version) {
		return false
	}
	v := "v" + version
	return semver.Major(v) == semver.Major("v"+ShippedPiDurableVersion) && semver.Compare(v, recordCeiling) < 0
}

// AuditStorageVersionReset is the audit action of ResetStorageVersion.
const AuditStorageVersionReset = "agent.storage_version_reset"

var errBadResetVersion = apperr.Invalid("invalid_version",
	fmt.Sprintf("The version must be a pi-durable version of major %s, at most one minor version ahead of %s", semver.Major("v" + ShippedPiDurableVersion)[1:], ShippedPiDurableVersion))

// StorageVersionReset is what ResetStorageVersion changed.
type StorageVersionReset struct {
	TeamID uuid.UUID
	// From is the version that was recorded ("": none); To the one recorded
	// now ("": the record was removed).
	From, To string
}

// ResetStorageVersion is the operator's recovery of the upgrade check (the
// reset-storage-version command): it removes the agent's recorded storage
// version (to == "") or sets it to `to`, and ends any refusal, so that the
// next runtime start records its own version. Use it when a version was
// recorded that no runtime has actually written the agent's data with. It is
// audited (system actor, in the agent's team's log) as
// agent.storage_version_reset.
func ResetStorageVersion(ctx context.Context, pool *pgxpool.Pool, agentID uuid.UUID, to string) (StorageVersionReset, error) {
	if to != "" && !recordable(to) {
		return StorageVersionReset{}, errBadResetVersion
	}
	var res StorageVersionReset
	err := store.InTx(ctx, pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		res = StorageVersionReset{To: to}
		teamID, err := q.LockAgentRuntime(ctx, agentID)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errNotFound
		} else if err != nil {
			return err
		}
		res.TeamID = teamID
		row, err := q.GetAgentStorageVersion(ctx, agentID)
		if err == nil {
			res.From = row.PiDurableVersion
		} else if !errors.Is(store.NotFound(err), store.ErrNotFound) {
			return err
		}
		if to == "" {
			err = q.DeleteAgentStorageVersion(ctx, agentID)
		} else {
			err = q.RecordAgentStorageVersion(ctx, dbgen.RecordAgentStorageVersionParams{AgentID: agentID, PiDurableVersion: to})
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{ActorKind: audit.ActorSystem, Action: AuditStorageVersionReset, TeamID: teamID,
			TargetType: "agent", TargetID: agentID.String(), Metadata: map[string]any{"from": res.From, "to": to}})
	})
	return res, err
}
