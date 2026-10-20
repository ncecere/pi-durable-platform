package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/ratelimit"
	"github.com/ncecere/pi-durable-platform/internal/triggers"
)

// Triggers (api/openapi.yaml, tag triggers; docs/triggers.md). Reads take a
// session or a key (everyone who may use the agent); changes are a person's
// in a browser session (internal/triggers authorizes: owners and admins).
// The webhook address takes no session or key: its signature is checked by
// internal/triggers.

// webhookRatePerMinute bounds deliveries to one webhook address.
const webhookRatePerMinute = 120

func (a *api) triggerRoutes() []route {
	const list = "/v1/agents/{agentId}/triggers"
	const one = list + "/{triggerId}"
	k, s := a.sessionOrKey, a.session
	return []route{
		{"GET", list, k(a.listAgentTriggers)},
		{"POST", list, s(a.createAgentTrigger)},
		{"GET", one, k(a.getAgentTrigger)},
		{"PATCH", one, s(a.updateAgentTrigger)},
		{"DELETE", one, s(a.deleteAgentTrigger)},
		{"POST", one + "/secret", s(a.rotateAgentTriggerSecret)},
		{"POST", one + "/fire", s(a.fireAgentTrigger)},
		{"GET", "/v1/agents/{agentId}/trigger-settings", k(a.getAgentTriggerSettings)},
		{"PUT", "/v1/agents/{agentId}/trigger-settings", s(a.setAgentTriggerSettings)},
		{"GET", "/v1/agents/{agentId}/runs", k(a.listAgentRuns)},
		{"GET", "/v1/agents/{agentId}/runs/{runId}", k(a.getAgentRun)},
		{"GET", "/v1/teams/{team}/runs", k(a.listTeamRuns)},
		{"POST", "/v1/hooks/{hookId}", http.HandlerFunc(a.deliverWebhook)},
	}
}

func toAPITrigger(v triggers.View) apitypes.Trigger {
	t := v.Trigger
	out := apitypes.Trigger{Id: t.ID, AgentId: t.AgentID, Kind: apitypes.TriggerKind(t.Kind), Name: t.Name,
		Source: apitypes.TriggerSource(t.Source), Once: t.Once, OriginThreadId: t.OriginThreadID, Paused: t.Paused, ThreadId: t.ThreadID,
		WebhookSecretPrefix: t.WebhookSecretPrefix, CallbackUrl: t.CallbackUrl, CallbackSecretPrefix: t.CallbackSecretPrefix,
		ConsecutiveFailures: t.ConsecutiveFailures, BackoffUntil: t.BackoffUntil, LastFiredAt: t.LastFiredAt,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, Revision: t.Revision}
	if v.MessageHidden {
		hidden := true
		out.MessageHidden = &hidden
	} else {
		msg := t.MessageTemplate
		out.MessageTemplate = &msg
	}
	if t.PausedReason != nil {
		r := apitypes.TriggerPausedReason(*t.PausedReason)
		out.PausedReason = &r
	}
	if t.Cron != nil {
		out.Schedule = &apitypes.TriggerSchedule{Cron: *t.Cron, Timezone: *t.Timezone,
			MissedPolicy: apitypes.TriggerScheduleMissedPolicy(*t.MissedPolicy), NextFireAt: t.NextFireAt}
	}
	if v.WebhookURL != "" {
		out.WebhookUrl = &v.WebhookURL
	}
	if t.FilterPath != nil {
		out.Filter = &apitypes.TriggerFilter{Path: *t.FilterPath, Equals: *t.FilterEquals}
	}
	if t.DailyBudgetUsd.Valid {
		b := t.DailyBudgetUsd.Float64
		out.DailyBudgetUsd = &b
	}
	if t.CreatedBy.Valid {
		id := t.CreatedBy.UUID
		out.CreatedBy = &id
	}
	return out
}

func toAPITriggerSaved(s triggers.Saved) apitypes.TriggerSaved {
	out := apitypes.TriggerSaved{Trigger: toAPITrigger(s.View)}
	if s.WebhookSecret != "" {
		out.WebhookSecret = &s.WebhookSecret
	}
	if s.CallbackSecret != "" {
		out.CallbackSecret = &s.CallbackSecret
	}
	return out
}

func toAPIRun(v triggers.RunView) apitypes.TriggerRun {
	r := v.Run
	team := r.TeamID
	out := apitypes.TriggerRun{Id: r.ID, AgentId: r.AgentID, TeamId: &team, TriggerName: r.TriggerName,
		TriggerKind: apitypes.TriggerKind(r.TriggerKind), FiredBy: apitypes.TriggerRunFiredBy(r.FiredBy), FireId: r.FireID,
		ThreadId: r.ThreadID, Status: apitypes.TriggerRunStatus(r.Status), Reason: r.Reason, ScheduledFor: r.ScheduledFor,
		CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, CostUsd: r.CostUsd, Requests: r.Requests, Link: v.Path}
	if r.TriggerID.Valid {
		id := r.TriggerID.UUID
		out.TriggerId = &id
	}
	if r.CallbackStatus != nil {
		cs := apitypes.TriggerRunCallbackStatus(*r.CallbackStatus)
		out.CallbackStatus = &cs
	}
	return out
}

func triggerInput(name *string, paused *bool, threadID, template, cron, zone *string, missed *string, filter *apitypes.TriggerFilter,
	callback *string, budget *float64) triggers.Input {
	in := triggers.Input{Name: name, Paused: paused, ThreadID: threadID, MessageTemplate: template, Cron: cron, Timezone: zone,
		MissedPolicy: missed, CallbackURL: callback, DailyBudgetUSD: budget}
	if filter != nil {
		in.Filter = &triggers.Filter{Path: filter.Path, Equals: filter.Equals}
	}
	return in
}

func (a *api) listAgentTriggers(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	rows, err := a.Triggers.List(r.Context(), a.actor(r), id)
	writeList(w, r, rows, err, toAPITrigger)
}

func (a *api) createAgentTrigger(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	var in apitypes.TriggerCreate
	if !httpx.Decode(w, r, &in) {
		return
	}
	ti := triggerInput(&in.Name, in.Paused, in.ThreadId, &in.MessageTemplate, in.Cron, in.Timezone, (*string)(in.MissedPolicy),
		in.Filter, in.CallbackUrl, in.DailyBudgetUsd)
	ti.Kind = string(in.Kind)
	saved, err := a.Triggers.Create(r.Context(), a.actor(r), id, ti)
	if failed(w, r, err) {
		return
	}
	writeRevised(w, http.StatusCreated, saved.Trigger.Revision, toAPITriggerSaved(saved))
}

// triggerPath reads the agent's and the trigger's IDs.
func triggerPath(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	agentID, ok := pathUUID(w, r, "agentId")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	id, ok := pathUUID(w, r, "triggerId")
	return agentID, id, ok
}

func (a *api) getAgentTrigger(w http.ResponseWriter, r *http.Request) {
	agentID, id, ok := triggerPath(w, r)
	if !ok {
		return
	}
	v, err := a.Triggers.Get(r.Context(), a.actor(r), agentID, id)
	if failed(w, r, err) {
		return
	}
	writeRevised(w, http.StatusOK, v.Trigger.Revision, toAPITrigger(v))
}

func (a *api) updateAgentTrigger(w http.ResponseWriter, r *http.Request) {
	agentID, id, ok := triggerPath(w, r)
	if !ok {
		return
	}
	in, rev, ok := decodeRevised[apitypes.TriggerUpdate](w, r)
	if !ok {
		return
	}
	ti := triggerInput(in.Name, in.Paused, in.ThreadId, in.MessageTemplate, in.Cron, in.Timezone, (*string)(in.MissedPolicy),
		in.Filter, in.CallbackUrl, in.DailyBudgetUsd)
	saved, err := a.Triggers.Update(r.Context(), a.actor(r), agentID, id, ti, rev)
	if failed(w, r, err) {
		return
	}
	writeRevised(w, http.StatusOK, saved.Trigger.Revision, toAPITriggerSaved(saved))
}

func (a *api) deleteAgentTrigger(w http.ResponseWriter, r *http.Request) {
	agentID, id, ok := triggerPath(w, r)
	if !ok {
		return
	}
	writeOK(w, r, a.Triggers.Delete(r.Context(), a.actor(r), agentID, id))
}

func (a *api) rotateAgentTriggerSecret(w http.ResponseWriter, r *http.Request) {
	agentID, id, ok := triggerPath(w, r)
	if !ok {
		return
	}
	var in apitypes.TriggerSecretRotate
	if !httpx.Decode(w, r, &in) {
		return
	}
	saved, err := a.Triggers.RotateSecret(r.Context(), a.actor(r), agentID, id, string(in.Which))
	if failed(w, r, err) {
		return
	}
	httpx.JSON(w, http.StatusOK, toAPITriggerSaved(saved))
}

func (a *api) fireAgentTrigger(w http.ResponseWriter, r *http.Request) {
	agentID, id, ok := triggerPath(w, r)
	if !ok {
		return
	}
	run, err := a.Triggers.Fire(r.Context(), a.actor(r), agentID, id)
	if failed(w, r, err) {
		return
	}
	httpx.JSON(w, http.StatusAccepted, toAPIRun(triggers.RunView{Run: run}))
}

func toAPITriggerSettings(st triggers.Settings) apitypes.TriggerSettings {
	return apitypes.TriggerSettings{Paused: st.Paused, MaxConcurrentRuns: st.MaxConcurrentRuns, MaxRunSeconds: st.MaxRunSeconds,
		DailyBudgetUsd: st.DailyBudgetUSD, EffectiveConcurrentRuns: st.EffectiveConcurrentRuns, EffectiveRunSeconds: st.EffectiveRunSeconds,
		CeilingConcurrentRuns: st.CeilingConcurrentRuns, CeilingRunSeconds: st.CeilingRunSeconds, Revision: st.Revision, UpdatedAt: st.UpdatedAt}
}

func (a *api) getAgentTriggerSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	st, err := a.Triggers.GetSettings(r.Context(), a.actor(r), id)
	if failed(w, r, err) {
		return
	}
	writeRevised(w, http.StatusOK, st.Revision, toAPITriggerSettings(st))
}

func (a *api) setAgentTriggerSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	in, rev, ok := decodeRevised[apitypes.TriggerSettingsUpdate](w, r)
	if !ok {
		return
	}
	st, err := a.Triggers.SetSettings(r.Context(), a.actor(r), id, triggers.Settings{Paused: in.Paused,
		MaxConcurrentRuns: in.MaxConcurrentRuns, MaxRunSeconds: in.MaxRunSeconds, DailyBudgetUSD: in.DailyBudgetUsd}, rev)
	if failed(w, r, err) {
		return
	}
	writeRevised(w, http.StatusOK, st.Revision, toAPITriggerSettings(st))
}

// runPage reads ?status=, ?limit= and ?cursor= of a run list (it fetches
// one more than limit, to know whether a next page follows).
func runPage(w http.ResponseWriter, r *http.Request) (triggers.RunPage, int, bool) {
	var page triggers.RunPage
	limit, ok := pageLimit(w, r)
	if !ok {
		return page, 0, false
	}
	page.Limit = limit + 1
	if v := r.URL.Query().Get("status"); v != "" {
		if !apitypes.TriggerRunStatus(v).Valid() {
			httpx.Error(w, http.StatusBadRequest, "invalid_status", "status must be pending, running, succeeded, failed, stopped or skipped")
			return page, 0, false
		}
		page.Status = &v
	}
	keys, ok := decodeCursor(w, r, 2)
	if !ok {
		return page, 0, false
	}
	if keys != nil {
		micros, err := strconv.ParseInt(keys[0], 10, 64)
		id, err2 := uuid.Parse(keys[1])
		if err != nil || err2 != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid_cursor", "Invalid cursor")
			return page, 0, false
		}
		at := time.UnixMicro(micros)
		page.AfterAt, page.AfterID = &at, &id
	}
	return page, int(limit), true
}

func writeRuns(w http.ResponseWriter, r *http.Request, rows []triggers.RunView, limit int, err error) {
	if failed(w, r, err) {
		return
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1].Run
		next = encodeCursor(strconv.FormatInt(last.CreatedAt.UnixMicro(), 10), last.ID.String())
	}
	out := make([]apitypes.TriggerRun, len(rows))
	for i, v := range rows {
		out[i] = toAPIRun(v)
	}
	httpx.JSONWithCursor(w, http.StatusOK, out, next)
}

func (a *api) listAgentRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	page, limit, ok := runPage(w, r)
	if !ok {
		return
	}
	if v := r.URL.Query().Get("triggerId"); v != "" {
		tid, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid_trigger_id", "triggerId is a trigger's ID")
			return
		}
		page.TriggerID = &tid
	}
	rows, err := a.Triggers.Runs(r.Context(), a.actor(r), id, page)
	writeRuns(w, r, rows, limit, err)
}

func (a *api) getAgentRun(w http.ResponseWriter, r *http.Request) {
	agentID, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	runID, ok := pathUUID(w, r, "runId")
	if !ok {
		return
	}
	v, err := a.Triggers.Run(r.Context(), a.actor(r), agentID, runID)
	if failed(w, r, err) {
		return
	}
	httpx.JSON(w, http.StatusOK, toAPIRun(v))
}

func (a *api) listTeamRuns(w http.ResponseWriter, r *http.Request) {
	page, limit, ok := runPage(w, r)
	if !ok {
		return
	}
	rows, err := a.Triggers.TeamRuns(r.Context(), a.actor(r), r.PathValue("team"), page)
	writeRuns(w, r, rows, limit, err)
}

// deliverWebhook takes a delivery to a webhook's address: at most 120 a
// minute per address, a body of at most 256 KiB, signed (internal/triggers
// checks the signature before anything else). Neither the body nor the
// signature is logged.
func (a *api) deliverWebhook(w http.ResponseWriter, r *http.Request) {
	hookID := r.PathValue("hookId")
	if len(hookID) > 64 {
		httpx.Error(w, http.StatusNotFound, "hook_not_found", "No webhook at this address")
		return
	}
	ok, wait, err := ratelimit.Allow(r.Context(), a.limiter, ratelimit.Bucket("webhook", hookID), webhookRatePerMinute)
	if failed(w, r, err) {
		return
	}
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(ratelimit.RetryAfter(wait)))
		httpx.Error(w, http.StatusTooManyRequests, "rate_limited", "Too many deliveries to this address. Try again shortly.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, triggers.MaxWebhookBody))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "too_large", "The body is larger than 256 KiB")
		return
	} else if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid_request", "The body could not be read")
		return
	}
	res, err := a.Triggers.Webhook(r.Context(), hookID, r.Header.Get(triggers.SignatureHeader), body)
	if failed(w, r, err) {
		return
	}
	out := apitypes.WebhookAccepted{Matched: res.Matched, Replayed: res.Replayed}
	if res.Matched {
		id, status := res.Run.ID, apitypes.TriggerRunStatus(res.Run.Status)
		out.RunId, out.Status, out.Reason = &id, &status, res.Run.Reason
	}
	httpx.JSON(w, http.StatusAccepted, out)
}
