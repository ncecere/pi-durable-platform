package agents

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Idempotency keys (Idempotency-Key on agent creation; the IETF draft
// draft-ietf-httpapi-idempotency-key-header).
//
// A key is remembered for 24 hours per caller, with a digest of the request:
//
//   - a person acting directly (a browser session or a personal API key):
//     their own, across sessions and keys, since both are the person;
//   - an app acting for a person (OAuth Connect): per connection (grant), so
//     two apps that pick the same key never see each other's results, and a
//     person's own retries don't collide with an app's;
//   - a service account: its own, across its keys.
//
// The key is claimed in the transaction that creates the agent, before
// anything else is written, under the table's primary key: a concurrent
// retry waits for the first request's transaction and then replays its
// result (or, if it failed, runs itself). Only successful creations are
// remembered: a refused or failed request can be retried with the same key.
// The same key with a different request is refused with 422 (the draft's
// choice for a key reused with a different payload); the same key with the
// same request returns the first answer: 201 and the agent (as it is now,
// if the caller may still see it; 404 if it was deleted since).

// OpCreateAgent is the operation agent creation's keys are remembered under.
const OpCreateAgent = "agent.create"

var (
	errIdempotencyReused = apperr.New(http.StatusUnprocessableEntity, "idempotency_key_reused",
		"This Idempotency-Key was used for a different request in the last 24 hours: use a new key for a new request")
	errBadIdempotencyKey = apperr.Invalid("invalid_idempotency_key", "Idempotency-Key is 1-255 printable ASCII characters")
)

// ParseIdempotencyKey reads an Idempotency-Key header value: a
// structured-field string ("…", as the draft defines it) or the bare
// characters. An invalid value is 400 invalid_idempotency_key.
func ParseIdempotencyKey(v string) (string, error) {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
	}
	if len(v) < 1 || len(v) > 255 {
		return "", errBadIdempotencyKey
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] > 0x7e {
			return "", errBadIdempotencyKey
		}
	}
	return v, nil
}

// idempotencyCaller is the scope a caller's keys are remembered in.
func idempotencyCaller(a authz.Actor) string {
	switch {
	case a.Service != nil:
		return "service_account:" + a.Service.AccountID.String()
	case a.OAuthGrantID != uuid.Nil:
		return "oauth:" + a.OAuthGrantID.String()
	default:
		return "user:" + a.UserID.String()
	}
}

// createFingerprint is the digest of an agent creation as the platform
// understood it: the team (resolved, so its slug and ID are the same team)
// and the decoded body, so whitespace and field order don't matter.
func createFingerprint(teamID uuid.UUID, in CreateInput) []byte {
	b, _ := json.Marshal(struct {
		Team        uuid.UUID  `json:"team"`
		Name        string     `json:"name"`
		Description string     `json:"description"`
		Template    string     `json:"template"`
		TemplateID  *uuid.UUID `json:"templateId"`
		ModelID     *uuid.UUID `json:"modelId"`
	}{teamID, in.Name, in.Description, in.Template, in.TemplateID, in.ModelID})
	sum := sha256.Sum256(b)
	return sum[:]
}

// idempotentClaim claims a key for a request in the transaction. It returns
// the agent an earlier request with the key created (replay), or nil when
// this request is to run (and complete the claim with completeIdempotent).
func (s *Service) idempotentClaim(ctx context.Context, q *dbgen.Queries, a authz.Actor, key string, fingerprint []byte) (*View, error) {
	caller := idempotencyCaller(a)
	_, err := q.ClaimIdempotencyKey(ctx, dbgen.ClaimIdempotencyKeyParams{Caller: caller, Operation: OpCreateAgent, Key: key, Fingerprint: fingerprint})
	if err == nil {
		return nil, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	prev, err := q.GetIdempotencyKey(ctx, dbgen.GetIdempotencyKeyParams{Caller: caller, Operation: OpCreateAgent, Key: key})
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(prev.Fingerprint, fingerprint) {
		return nil, errIdempotencyReused
	}
	if !prev.AgentID.Valid {
		return nil, errNotFound
	}
	ag, err := s.load(ctx, q, prev.AgentID.UUID)
	if err != nil {
		return nil, err
	}
	sub, err := subject(ctx, q, a, ag)
	if err != nil {
		return nil, err
	}
	if err := authz.CheckAgentSee(sub); err != nil {
		return nil, err
	}
	v, err := s.view(ctx, q, ag)
	return &v, err
}

// completeIdempotent records the agent a claimed key's request created.
func completeIdempotent(ctx context.Context, q *dbgen.Queries, a authz.Actor, key string, agentID uuid.UUID) error {
	return q.CompleteIdempotencyKey(ctx, dbgen.CompleteIdempotencyKeyParams{Caller: idempotencyCaller(a), Operation: OpCreateAgent, Key: key,
		AgentID: uuid.NullUUID{UUID: agentID, Valid: true}, Status: http.StatusCreated})
}

// CleanupIdempotencyKeys forgets keys older than 24 hours (the cleanup loop;
// idempotent, so every replica may run it).
func (s *Service) CleanupIdempotencyKeys(ctx context.Context) (int64, error) {
	return s.q.DeleteExpiredIdempotencyKeys(ctx)
}
