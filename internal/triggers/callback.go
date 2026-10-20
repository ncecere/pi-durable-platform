package triggers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Completion callbacks (docs/triggers.md): when a run ends, its trigger's
// callback URL gets a signed summary (never the transcript), through the
// address guard, retried with back-off.

const (
	callbackAttempts = 6
	callbackLease    = 2 * time.Minute
	callbackTimeout  = 10 * time.Second
	// ExcerptLength is the most of the run's last reply a callback carries.
	ExcerptLength = 500
)

// Callback is what a completion callback sends.
type Callback struct {
	Type         string     `json:"type"`
	RunID        uuid.UUID  `json:"runId"`
	TriggerID    *uuid.UUID `json:"triggerId"`
	TriggerName  string     `json:"triggerName"`
	AgentID      uuid.UUID  `json:"agentId"`
	ThreadID     *string    `json:"threadId"`
	Status       string     `json:"status"`
	Reason       *string    `json:"reason"`
	ScheduledFor *time.Time `json:"scheduledFor"`
	StartedAt    *time.Time `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
	CostUSD      float64    `json:"costUsd"`
	Excerpt      *string    `json:"excerpt"`
	Link         *string    `json:"link"`
}

// sendCallbacks sends every due callback (each under a lease, so a slow one
// isn't sent twice by two passes).
func (s *Service) sendCallbacks(ctx context.Context) error {
	ids, err := s.q.CallbacksDue(ctx, s.now())
	if err != nil {
		return err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.sendCallback(ctx, id)
	}
	return nil
}

func (s *Service) sendCallback(ctx context.Context, id uuid.UUID) {
	now := s.now()
	run, err := s.q.ClaimCallback(ctx, dbgen.ClaimCallbackParams{ID: id, Now: now, Lease: now.Add(callbackLease)})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Warn("triggers: claim a callback", "run", id, "err", err)
		}
		return
	}
	attempts := run.CallbackAttempts + 1
	err = s.postCallback(ctx, run)
	p := dbgen.CallbackResultParams{ID: run.ID, CallbackStatus: "delivered", CallbackAttempts: attempts}
	switch {
	case errors.Is(err, errNoCallback):
		p.CallbackStatus = "failed"
	case err != nil && attempts >= callbackAttempts:
		p.CallbackStatus = "failed"
		s.log.Info("triggers: callback failed; giving up", "run", run.ID, "trigger", run.TriggerID.UUID, "attempts", attempts, "err", err)
	case err != nil:
		next := s.now().Add(30 * time.Second << (attempts - 1))
		p.CallbackStatus, p.CallbackNextAt = "pending", &next
		s.log.Info("triggers: callback failed; will retry", "run", run.ID, "trigger", run.TriggerID.UUID, "attempts", attempts, "err", err)
	}
	if err := s.q.CallbackResult(context.WithoutCancel(ctx), p); err != nil {
		s.log.Warn("triggers: record a callback", "run", run.ID, "err", err)
	}
}

// postCallback sends one run's callback. Its response's body is never read
// beyond a little (to reuse the connection), stored or shown.
func (s *Service) postCallback(ctx context.Context, run dbgen.TriggerRun) error {
	if !run.TriggerID.Valid {
		return errNoCallback
	}
	trig, err := s.q.GetTriggerByID(ctx, run.TriggerID.UUID)
	if errors.Is(store.NotFound(err), store.ErrNotFound) || (err == nil && trig.CallbackUrl == nil) {
		return errNoCallback
	} else if err != nil {
		return err
	}
	secret, err := s.box.Open(trig.CallbackSecretEnc, []byte(callbackAD(trig.ID)))
	if err != nil {
		return errNoCallback
	}
	body, err := json.Marshal(s.callbackBody(ctx, run))
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, callbackTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, *trig.CallbackUrl, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "pi-durable-platform-callback")
	req.Header.Set("X-PDP-Event", "run.finished")
	req.Header.Set("X-PDP-Delivery", run.ID.String())
	req.Header.Set(SignatureHeader, Sign(string(secret), s.now(), body))
	resp, err := s.guard.Client().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return errors.New("the callback URL could not be reached")
		}
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return errors.New("the callback URL answered " + http.StatusText(resp.StatusCode))
	}
	return nil
}

// callbackBody is the run's summary: an excerpt of its last reply (read from
// the runtime now; none when it can't be) and the console's link to it.
func (s *Service) callbackBody(ctx context.Context, run dbgen.TriggerRun) Callback {
	out := Callback{Type: "run.finished", RunID: run.ID, TriggerName: run.TriggerName, AgentID: run.AgentID, ThreadID: run.ThreadID,
		Status: run.Status, Reason: run.Reason, ScheduledFor: run.ScheduledFor, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
		CostUSD: run.CostUsd, Link: s.link(ctx, run)}
	if run.TriggerID.Valid {
		id := run.TriggerID.UUID
		out.TriggerID = &id
	}
	if run.ThreadID != nil && run.StartedAt != nil {
		ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
		text, err := s.runner.ThreadExcerpt(ectx, run.AgentID, *run.ThreadID, ExcerptLength)
		cancel()
		if err == nil && text != "" {
			out.Excerpt = &text
		}
	}
	return out
}

// link is the console's address of a run's thread (nil: none).
func (s *Service) link(ctx context.Context, run dbgen.TriggerRun) *string {
	if run.ThreadID == nil {
		return nil
	}
	slug, err := s.q.AgentTeamSlug(ctx, run.AgentID)
	if err != nil {
		return nil
	}
	l := s.appURL + RunPath(slug, run.AgentID, *run.ThreadID)
	return &l
}

// RunPath is the console's path of a run's thread.
func RunPath(teamSlug string, agentID uuid.UUID, threadID string) string {
	return "/teams/" + url.PathEscape(teamSlug) + "/agents/" + agentID.String() + "?tab=test&thread=" + url.QueryEscape(threadID)
}
