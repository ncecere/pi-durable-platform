package triggers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

// Agent-made schedules (docs/triggers.md): the runtime library's schedule
// tool calls the gateway with the agent's own credential, never a person's,
// and the platform makes an ordinary schedule trigger for that agent only,
// marked as made by the agent (source agent, and the thread it was set in).
// The agent may list and cancel its own; people manage them like any other
// trigger, and every trigger limit holds for their runs.

// Who made a trigger.
const (
	SourcePerson = "person"
	SourceAgent  = "agent"
)

// Where an agent-made schedule fires.
const (
	WhereThis = "this" // the thread it was set in
	WhereNew  = "new"  // a new thread at every fire
)

// maxAgentMessage is the longest message an agent-made schedule sends (the
// column's limit).
const maxAgentMessage = 8000

// maxAhead is how far ahead a one-time reminder may be.
const maxAhead = 366 * 24 * time.Hour

// Refusals the model reads: each says what to change.
var (
	errAgentNotAllowed = apperr.New(403, "schedules_not_allowed",
		"This agent's template version doesn't let it schedule; a team owner or admin can allow it in a new template version")
	errAgentKind = apperr.New(403, "schedules_not_allowed",
		"This agent can't schedule: it is managed by an app for one of its users")
	errAgentArchived = apperr.Conflict("team_archived", "The agent's team is archived: it can't schedule anything")
	errAgentWhen     = apperr.Invalid("invalid_when",
		"Give exactly one of at (an ISO 8601 date and time, such as 2026-10-21T09:00) or cron (five fields: minute hour day month weekday, or @hourly, @daily, @weekly, @monthly, @yearly)")
	errAgentAt = apperr.Invalid("invalid_at",
		"at must be an ISO 8601 date and time, such as 2026-10-21T09:00 (in the time zone given) or 2026-10-21T07:00:00Z")
	errAgentPast    = apperr.Invalid("time_passed", "That time has already passed: give a time in the future")
	errAgentTooFar  = apperr.Invalid("too_far_ahead", "A one-time reminder may be at most a year ahead")
	errAgentMessage = apperr.Invalid("invalid_message",
		fmt.Sprintf("The message is what the agent will be told when the schedule fires: 1-%d characters", maxAgentMessage))
	errAgentThread = apperr.Invalid("invalid_thread", `thread must be "this" (the conversation it is set in) or "new" (a new conversation at every fire)`)
	errAgentName   = apperr.Invalid("invalid_trigger_name", "A name is at most 100 characters on one line")
)

func errAgentTooMany(n int) error {
	return apperr.Conflict("too_many_schedules",
		fmt.Sprintf("This agent already has %d schedules of its own, the most it may have: cancel one it no longer needs first", n))
}

func errAgentInterval(min time.Duration) error {
	return apperr.Invalid("too_frequent",
		fmt.Sprintf("A recurring schedule may fire at most once every %s: choose a cron expression whose fires are further apart", min))
}

// AgentScheduleInput is what the schedule tool asks for. Exactly one of At
// (ISO 8601: with an offset, or a local time in Timezone) and Cron.
type AgentScheduleInput struct {
	Name     string
	At       string
	Cron     string
	Timezone string
	Message  string
	// ThreadID is the thread the tool was called in ("" none: a subagent's
	// conversation, say). Thread is "this" (the default) or "new".
	ThreadID string
	Thread   string
}

// AgentScheduled is a schedule an agent made: the trigger, where it fires,
// and (when it was asked to fire in its thread and can't) why not.
type AgentScheduled struct {
	View
	Where      string
	WhyNotHere string
}

// whyNot says, for the model, why a thread can't take a schedule's message.
func whyNot(code string) string {
	switch code {
	case "thread_private":
		return "this conversation is private to a person in a shared team, and a schedule acts as no person"
	case "thread_reserved":
		return "this conversation hasn't started yet"
	case "thread_mcp_servers":
		return "an app gave this conversation MCP servers with a person's credentials, which a schedule must not use"
	case "thread_unknown":
		return "the platform has no record of this conversation"
	}
	return "this conversation can't take a schedule's message"
}

// parseAt reads an ISO 8601 time: RFC 3339 with an offset, or a local date
// and time (to the minute or second) in loc.
func parseAt(raw string, loc *time.Location) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errAgentAt
}

// minGap is the shortest time between two of the schedule's fires from now
// (scanning at most two years or 2,000 fires: cron fields repeat within a
// year, and the closest pair of a day's fires shows in its first ones).
func (sc schedule) minGap(now time.Time) time.Duration {
	gap := time.Duration(1<<63 - 1)
	prev := sc.next(now)
	end := now.Add(2 * 366 * 24 * time.Hour)
	for i := 0; i < 2000 && !prev.IsZero() && prev.Before(end); i++ {
		n := sc.next(prev)
		if n.IsZero() {
			break
		}
		gap = min(gap, n.Sub(prev))
		prev = n
	}
	return gap
}

// agentMadeName is a name for an agent-made trigger: the one the agent asked
// for, or one from its time (never from its message, which is the
// conversation's: names are in the audit log), unique among the agent's
// triggers.
func agentMadeName(asked, fallback string, taken map[string]bool) string {
	base := strings.TrimSpace(asked)
	if base == "" {
		base = fallback
	}
	name := base
	for i := 2; taken[name]; i++ {
		suffix := fmt.Sprintf(" (%d)", i)
		r := []rune(base)
		if len(r)+utf8.RuneCountInString(suffix) > 100 {
			r = r[:100-utf8.RuneCountInString(suffix)]
		}
		name = string(r) + suffix
	}
	return name
}

// agentActor is an audit entry made by the agent itself.
func agentActor(ag dbgen.Agent, action, targetID string) audit.Entry {
	return audit.Entry{ActorKind: audit.ActorAgent, Action: action, TargetType: "trigger", TargetID: targetID, TeamID: ag.TeamID,
		Metadata: map[string]any{"agentActorId": ag.ID.String(), "agentActorName": ag.Name}}
}

// agentFor loads the agent the gateway authenticated, for a change to its own
// schedules: one a team uses (not an app's agent for one of its users) in a
// team that isn't archived. create also requires its template version to let
// it schedule.
func agentFor(ctx context.Context, q *dbgen.Queries, agentID uuid.UUID, create bool) (dbgen.Agent, error) {
	ag, err := q.GetAgent(ctx, agentID)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return ag, apperr.NotFound("agent_not_found", "Agent not found")
	} else if err != nil {
		return ag, err
	}
	if ag.ServiceManaged {
		return ag, errAgentKind
	}
	team, err := q.GetTeamByID(ctx, ag.TeamID)
	if err != nil {
		return ag, err
	}
	if team.Status != teams.StatusActive {
		return ag, errAgentArchived
	}
	if !create {
		return ag, nil
	}
	tv, err := q.GetTemplateVersion(ctx, dbgen.GetTemplateVersionParams{TemplateID: ag.TemplateID, Version: ag.TemplateVersion})
	if err != nil {
		return ag, err
	}
	if !tv.AgentSchedules {
		return ag, errAgentNotAllowed
	}
	return ag, nil
}

// AgentCreate makes a schedule for the agent itself (the gateway
// authenticated its credential): a one-time reminder (At), removed when it
// fires, or a recurring schedule (Cron), within the agent-made limits
// (TRIGGERS_AGENT_MAX, TRIGGERS_AGENT_MIN_INTERVAL) and the 50 triggers an
// agent may have. It fires into the thread it was set in when that thread
// may take a trigger's message, otherwise into a new thread at every fire;
// the answer says which.
func (s *Service) AgentCreate(ctx context.Context, agentID uuid.UUID, in AgentScheduleInput) (AgentScheduled, error) {
	now := s.now()
	message := strings.TrimSpace(in.Message)
	if n := utf8.RuneCountInString(message); n < 1 || n > maxAgentMessage {
		return AgentScheduled{}, errAgentMessage
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(in.Name)); n > 100 || strings.ContainsAny(in.Name, "\r\n\x00") {
		return AgentScheduled{}, errAgentName
	}
	switch in.Thread {
	case "", WhereThis, WhereNew:
	default:
		return AgentScheduled{}, errAgentThread
	}
	zone := strings.TrimSpace(in.Timezone)
	if zone == "" {
		zone = "UTC"
	}
	at, cronExpr := strings.TrimSpace(in.At), strings.TrimSpace(in.Cron)
	if (at == "") == (cronExpr == "") {
		return AgentScheduled{}, errAgentWhen
	}
	missed, once := MissedSkip, at != ""
	var next time.Time
	var fallback string // its name, unless it is given one
	if once {
		if _, err := parseSchedule("@daily", zone); err != nil {
			return AgentScheduled{}, err // the zone
		}
		loc, _ := time.LoadLocation(zone)
		t, err := parseAt(at, loc)
		if err != nil {
			return AgentScheduled{}, err
		}
		switch {
		case !t.After(now):
			return AgentScheduled{}, errAgentPast
		case t.Sub(now) > maxAhead:
			return AgentScheduled{}, errAgentTooFar
		}
		// The record reads as any schedule: its cron names the same minute
		// (in its zone). The time itself, to the second, is next_fire_at.
		local := t.In(loc)
		cronExpr = fmt.Sprintf("%d %d %d %d *", local.Minute(), local.Hour(), local.Day(), int(local.Month()))
		// A reminder missed while the platform was down still runs, once.
		next, missed = t.UTC(), MissedRunOnce
		fallback = "Reminder " + local.Format("2006-01-02 15:04") + " " + zone
	} else {
		sc, err := parseSchedule(cronExpr, zone)
		if err != nil {
			return AgentScheduled{}, err
		}
		if sc.minGap(now) < s.cfg.AgentMinInterval {
			return AgentScheduled{}, errAgentInterval(s.cfg.AgentMinInterval)
		}
		next = sc.next(now)
		fallback = "Schedule " + cronExpr + " " + zone
		if utf8.RuneCountInString(fallback) > 90 {
			fallback = string([]rune(fallback)[:89]) + "…"
		}
	}
	var out AgentScheduled
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ag, err := agentFor(ctx, q, agentID, true)
		if err != nil {
			return err
		}
		// One change to the agent's triggers at a time (the counts hold).
		if err := q.LockAgentRuns(ctx, ag.ID); err != nil {
			return err
		}
		if n, err := q.CountAgentMadeTriggers(ctx, ag.ID); err != nil {
			return err
		} else if n >= int64(s.cfg.AgentMax) {
			return errAgentTooMany(s.cfg.AgentMax)
		}
		existing, err := q.ListAgentTriggers(ctx, ag.ID)
		if err != nil {
			return err
		}
		if len(existing) >= MaxPerAgent {
			return errTooMany
		}
		taken := map[string]bool{}
		for _, t := range existing {
			taken[t.Name] = true
		}
		var origin, thread *string
		out.Where = WhereNew
		if in.ThreadID != "" && threadIDRE.MatchString(in.ThreadID) {
			info, err := q.TriggerThreadInfo(ctx, dbgen.TriggerThreadInfoParams{AgentID: ag.ID, ThreadID: in.ThreadID})
			if err != nil {
				return err
			}
			if info.Known {
				id := in.ThreadID
				origin = &id
			}
			if in.Thread != WhereNew {
				if code := agents.TriggerThreadRefusal(info, true); code != "" {
					out.WhyNotHere = whyNot(code)
				} else {
					thread, out.Where = origin, WhereThis
				}
			}
		} else if in.Thread != WhereNew {
			out.WhyNotHere = whyNot("thread_unknown")
		}
		zoneCopy, cronCopy, missedCopy := zone, cronExpr, missed
		t, err := q.InsertTrigger(ctx, dbgen.InsertTriggerParams{AgentID: ag.ID, Kind: KindSchedule, Name: agentMadeName(in.Name, fallback, taken),
			Source: SourceAgent, Once: once, OriginThreadID: origin, ThreadID: thread, MessageTemplate: message, Cron: &cronCopy,
			Timezone: &zoneCopy, MissedPolicy: &missedCopy, NextFireAt: &next, Now: now})
		if err != nil {
			return err
		}
		e := agentActor(ag, "trigger.create", t.ID.String())
		e.After = snapshot(t)
		out.View = s.view(t)
		return audit.Record(ctx, q, e)
	})
	if err == nil {
		s.log.Info("triggers: an agent made a schedule", "agent", agentID, "trigger", out.Trigger.ID, "once", once, "where", out.Where)
	}
	return out, err
}

// AgentList returns the schedules the agent made itself.
func (s *Service) AgentList(ctx context.Context, agentID uuid.UUID) ([]View, error) {
	if _, err := agentFor(ctx, s.q, agentID, false); err != nil && !errors.Is(err, errAgentArchived) {
		return nil, err
	}
	rows, err := s.q.ListAgentMadeTriggers(ctx, agentID)
	if err != nil {
		return nil, err
	}
	out := make([]View, len(rows))
	for i, t := range rows {
		out[i] = s.view(t)
	}
	return out, nil
}

// AgentCancel deletes one of the schedules the agent made itself. Another
// agent's triggers, and those people made, are not found.
func (s *Service) AgentCancel(ctx context.Context, agentID, id uuid.UUID) (View, error) {
	var out View
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ag, err := agentFor(ctx, q, agentID, false)
		if err != nil {
			return err
		}
		t, err := q.LockTrigger(ctx, dbgen.LockTriggerParams{ID: id, AgentID: ag.ID})
		if errors.Is(store.NotFound(err), store.ErrNotFound) || (err == nil && t.Source != SourceAgent) {
			return errNotFound
		} else if err != nil {
			return err
		}
		if _, err := q.DeleteTrigger(ctx, dbgen.DeleteTriggerParams{ID: id, AgentID: ag.ID}); err != nil {
			return err
		}
		e := agentActor(ag, "trigger.delete", t.ID.String())
		e.Before = snapshot(t)
		out = s.view(t)
		return audit.Record(ctx, q, e)
	})
	return out, err
}

// messageFor is the message a fire of t sends: an agent-made trigger's is
// plain text (the agent wrote it; nothing in it is a placeholder), a person's
// is its template rendered.
func messageFor(t dbgen.AgentTrigger, v fireVars) string {
	if t.Source == SourceAgent {
		return t.MessageTemplate
	}
	return render(t.MessageTemplate, v)
}

// removeFired deletes a one-time reminder that has fired (its run is
// recorded, with its name), audited as the system's.
func removeFired(ctx context.Context, q *dbgen.Queries, t dbgen.AgentTrigger, teamID, runID uuid.UUID) error {
	if _, err := q.DeleteTrigger(ctx, dbgen.DeleteTriggerParams{ID: t.ID, AgentID: t.AgentID}); err != nil {
		return err
	}
	return audit.Record(ctx, q, audit.Entry{ActorKind: audit.ActorSystem, Action: "trigger.delete", TargetType: "trigger",
		TargetID: t.ID.String(), TeamID: teamID, Before: snapshot(t),
		Metadata: map[string]any{"name": t.Name, "reason": "fired", "runId": runID.String()}})
}
