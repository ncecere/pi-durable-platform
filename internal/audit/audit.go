// Package audit writes the append-only audit log.
//
// Record must be called with queries bound to the same transaction as the
// change being audited, so a change and its audit entry commit together.
package audit

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Actor kinds.
const (
	ActorUser           = "user"
	ActorAPIKey         = "api_key"
	ActorServiceAccount = "service_account"
	// ActorOAuth is a person acting through an app they connected (OAuth
	// Connect): ActorUserID is the person.
	ActorOAuth  = "oauth"
	ActorSystem = "system"
	// ActorAgent is an agent changing its own schedules (its schedule tool):
	// Metadata names it (agentActorId, agentActorName).
	ActorAgent = "agent"
)

// Entry is one audited event. Before/After hold configuration snapshots and
// must never contain secrets or call payloads.
type Entry struct {
	ActorKind   string
	ActorUserID uuid.UUID // uuid.Nil for system actions and service accounts
	// ActorServiceAccountID is set for service accounts.
	ActorServiceAccountID uuid.UUID
	TeamID                uuid.UUID // uuid.Nil when not team-scoped
	Action                string    // e.g. "auth.login", "team.create"
	TargetType            string
	TargetID              string
	Before                any
	After                 any
	Metadata              map[string]any
	RequestID             string
	ClientIP              string
}

func Record(ctx context.Context, q *dbgen.Queries, e Entry) error {
	if e.ActorKind == "" {
		e.ActorKind = ActorUser
	}
	meta := e.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	before, err := snapshot(e.Before)
	if err != nil {
		return err
	}
	after, err := snapshot(e.After)
	if err != nil {
		return err
	}
	return q.InsertAudit(ctx, dbgen.InsertAuditParams{
		ActorKind:             e.ActorKind,
		ActorUserID:           nullable(e.ActorUserID),
		ActorServiceAccountID: nullable(e.ActorServiceAccountID),
		TeamID:                nullable(e.TeamID),
		Action:                e.Action,
		TargetType:            e.TargetType,
		TargetID:              e.TargetID,
		BeforeState:           before,
		AfterState:            after,
		Metadata:              metaJSON,
		RequestID:             e.RequestID,
		ClientIP:              e.ClientIP,
	})
}

func snapshot(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

func nullable(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}
