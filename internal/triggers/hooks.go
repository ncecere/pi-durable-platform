package triggers

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

// MaxWebhookBody is the largest webhook body taken.
const MaxWebhookBody = 256 << 10

// WebhookResult is what a webhook delivery did: nothing (Matched false: the
// filter didn't match), or a run (Replayed: an earlier delivery's, for a
// replay of it).
type WebhookResult struct {
	Matched  bool
	Run      dbgen.TriggerRun
	Replayed bool
}

// Webhook takes a delivery to a webhook's address: the signature is checked
// first (against the body and the clock), then the filter; a matching
// delivery fires the trigger (or finds the run of an earlier delivery with
// the same signature). The run is delivered in the background.
func (s *Service) Webhook(ctx context.Context, hookID, signature string, body []byte) (WebhookResult, error) {
	now := s.now()
	trig, err := s.q.GetTriggerByHook(ctx, &hookID)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return WebhookResult{}, errHookNotFound
	} else if err != nil {
		return WebhookResult{}, err
	}
	secret, err := s.box.Open(trig.WebhookSecretEnc, []byte(webhookAD(trig.ID)))
	if err != nil {
		return WebhookResult{}, err
	}
	sig, ok := verify(signature, string(secret), body, now)
	if !ok {
		return WebhookResult{}, errBadSignature
	}
	payload, isJSON := decodePayload(body)
	if trig.FilterPath != nil && !(Filter{Path: *trig.FilterPath, Equals: *trig.FilterEquals}).matches(payload, isJSON) {
		return WebhookResult{}, nil
	}
	f := fire{firedBy: FiredByWebhook, fireID: webhookFireID(trig.ID, sig)}
	vars := fireVars{TriggerName: trig.Name, TriggerKind: trig.Kind, FireID: f.fireID, FireTime: now, HasPayload: true,
		PayloadText: string(body)}
	if isJSON {
		vars.Payload = payload
	}
	out := WebhookResult{Matched: true}
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		// Locked first (the order record expects); it may have changed or gone.
		t, err := q.LockTrigger(ctx, dbgen.LockTriggerParams{ID: trig.ID, AgentID: trig.AgentID})
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errHookNotFound
		} else if err != nil {
			return err
		}
		if t.HookID == nil || *t.HookID != hookID {
			return errHookNotFound
		}
		ag, err := q.GetAgent(ctx, t.AgentID)
		if err != nil {
			return err
		}
		team, err := q.GetTeamByID(ctx, ag.TeamID)
		if err != nil {
			return err
		}
		f.trig, f.teamID = t, ag.TeamID
		vars.TriggerName = t.Name
		f.message = render(t.MessageTemplate, vars)
		if team.Status != teams.StatusActive {
			f.skip = ReasonTeamArchived
		}
		out.Run, out.Replayed, err = s.record(ctx, q, f, now)
		return err
	})
	if err != nil {
		return WebhookResult{}, err
	}
	if !out.Replayed {
		s.deliverSoon(out.Run)
	}
	return out, nil
}

// Fire is a person's Run now: the trigger fires at once (a webhook's with no
// payload), whether it is paused or backing off; the agent's limits hold.
func (s *Service) Fire(ctx context.Context, a authz.Actor, agentID, id uuid.UUID) (dbgen.TriggerRun, error) {
	now := s.now()
	var run dbgen.TriggerRun
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		t, err := q.LockTrigger(ctx, dbgen.LockTriggerParams{ID: id, AgentID: agentID})
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			t = dbgen.AgentTrigger{} // not found, once the agent is known to be visible
		} else if err != nil {
			return err
		}
		ag, _, err := manageable(ctx, q, a, agentID)
		if err != nil {
			return err
		}
		if t.ID == uuid.Nil {
			return errNotFound
		}
		f := fire{trig: t, teamID: ag.TeamID, firedBy: FiredByManual, fireID: manualFireID(), manual: &a}
		f.message = render(t.MessageTemplate, fireVars{TriggerName: t.Name, TriggerKind: t.Kind, FireID: f.fireID, FireTime: now})
		run, _, err = s.record(ctx, q, f, now)
		return err
	})
	if err == nil {
		s.deliverSoon(run)
	}
	return run, err
}
