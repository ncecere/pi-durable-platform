package agents

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/mod/semver"

	"github.com/ncecere/pi-durable-platform/internal/agenthost"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// The upgrade check (docs/custom-agents.md, Upgrades; migration 00024).
//
// pi-durable migrates a document when it reads it, and code that finds a
// document a newer version wrote refuses it. So the platform records, per
// agent, the newest pi-durable version that has written its storage, and
// refuses to start the agent on a runtime with an older one: the runtime
// asks before it opens the storage (the gateway's /runtime/start), and the
// manager checks again at /status after a start (agenthost.Manager.Admit).
// Rolling a runtime back after it has written is not supported.

// Refusal is why the platform last refused the agent's runtime.
type Refusal struct {
	// RuntimeVersion is the refused runtime's pi-durable version ("": it
	// reported none).
	RuntimeVersion string
	// StorageVersion is the version that has written the agent's storage.
	StorageVersion string
	At             time.Time
	Message        string
}

// RuntimeState is what the platform knows of the agent's storage format.
type RuntimeState struct {
	// StorageVersion is the newest pi-durable version that has written the
	// agent's storage ("": none recorded).
	StorageVersion string
	// Refused is set while the last start was refused.
	Refused *Refusal
}

func runtimeState(row dbgen.AgentStorageVersion) RuntimeState {
	st := RuntimeState{StorageVersion: row.PiDurableVersion}
	if row.RefusedAt != nil && row.RefusedVersion != nil {
		st.Refused = &Refusal{RuntimeVersion: *row.RefusedVersion, StorageVersion: row.PiDurableVersion, At: *row.RefusedAt,
			Message: refusalMessage(row.PiDurableVersion, *row.RefusedVersion)}
	}
	return st
}

// runtimeStateOf reads the agent's record (the zero state when it has none).
func (s *Service) runtimeStateOf(ctx context.Context, q *dbgen.Queries, agentID uuid.UUID) (RuntimeState, error) {
	row, err := q.GetAgentStorageVersion(ctx, agentID)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return RuntimeState{}, nil
	} else if err != nil {
		return RuntimeState{}, err
	}
	return runtimeState(row), nil
}

// olderThan reports whether a runtime's pi-durable version is older than the
// stored one; a runtime that reports none counts as older (it may be).
func olderThan(runtime, stored string) bool {
	return runtime == "" || semver.Compare("v"+runtime, "v"+stored) < 0
}

func refusalMessage(stored, runtime string) string {
	const fix = "Run it on a runtime with pi-durable %s or newer: rolling a runtime back after it has written an agent's data is not supported."
	if runtime == "" {
		return fmt.Sprintf("This agent's data was written by a runtime with pi-durable %s, and this runtime doesn't report its version, so it may "+
			"not be able to read it. "+fix, stored, stored)
	}
	return fmt.Sprintf("This agent's data was written by a runtime with pi-durable %s; this runtime has pi-durable %s, which can't read it. "+fix,
		stored, runtime, stored)
}

// AdmitRuntime is the upgrade check for one start of the agent's runtime,
// which reports its pi-durable version ("" or malformed: none). A runtime
// older than the version recorded for the agent is refused: the refusal is
// recorded (the agent's page shows it) and the error is a
// *agenthost.RefusedError. Otherwise the version is recorded when it is
// newer, and a previous refusal ends. Changes are audited (system actor, in
// the agent's team's log): a new recorded version or an ended refusal as
// agent.runtime_recorded, the first refusal of a version as
// agent.runtime_refused.
func (s *Service) AdmitRuntime(ctx context.Context, agentID uuid.UUID, version string) error {
	if !agenthost.ValidPiDurableVersion(version) {
		version = ""
	}
	var refused error
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		refused = nil
		teamID, err := q.LockAgentRuntime(ctx, agentID)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errNotFound
		} else if err != nil {
			return err
		}
		row, err := q.GetAgentStorageVersion(ctx, agentID)
		found := err == nil
		if err != nil && !errors.Is(store.NotFound(err), store.ErrNotFound) {
			return err
		}
		entry := audit.Entry{ActorKind: audit.ActorSystem, TeamID: teamID, TargetType: "agent", TargetID: agentID.String()}
		if found && olderThan(version, row.PiDurableVersion) {
			refused = &agenthost.RefusedError{Message: refusalMessage(row.PiDurableVersion, version)}
			if err := q.RefuseAgentRuntime(ctx, dbgen.RefuseAgentRuntimeParams{AgentID: agentID, RefusedVersion: &version}); err != nil {
				return err
			}
			if row.RefusedVersion != nil && *row.RefusedVersion == version {
				return nil // refused again: already recorded and audited
			}
			entry.Action = "agent.runtime_refused"
			entry.Metadata = map[string]any{"storageVersion": row.PiDurableVersion, "runtimeVersion": version}
			return audit.Record(ctx, q, entry)
		}
		if version == "" {
			return nil // nothing recorded, and nothing to record
		}
		newer := !found || semver.Compare("v"+version, "v"+row.PiDurableVersion) > 0
		wasRefused := found && row.RefusedAt != nil
		if !newer && !wasRefused {
			return nil
		}
		from := ""
		if found {
			from = row.PiDurableVersion
		}
		if !newer {
			version = row.PiDurableVersion // equal (not older, not newer): keep the stored spelling
		}
		if err := q.RecordAgentStorageVersion(ctx, dbgen.RecordAgentStorageVersionParams{AgentID: agentID, PiDurableVersion: version}); err != nil {
			return err
		}
		entry.Action = "agent.runtime_recorded"
		entry.Metadata = map[string]any{"from": from, "to": version, "refusalEnded": wasRefused}
		return audit.Record(ctx, q, entry)
	})
	if err != nil {
		return err
	}
	return refused
}
