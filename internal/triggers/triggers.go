// Package triggers runs agents without a person: schedules (cron in a time
// zone) and webhooks (a signed inbound address) that send a message to one
// of an agent's threads, the run each fire makes, the limits that hold for
// them, and a signed completion callback. docs/triggers.md is the design.
//
// Every replica runs the trigger loop (Loop); one at a time does the work (a
// database lock per pass), and nothing depends on that for correctness:
// due schedules are locked and advanced in the transaction that records
// their runs, and every fire has a unique ID that is also its message's
// pi-durable requestId, so neither a second replica nor a retry runs it
// twice.
package triggers

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teamnet"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

// Kinds of trigger.
const (
	KindSchedule = "schedule"
	KindWebhook  = "webhook"
)

// MaxPerAgent is how many triggers an agent may have.
const MaxPerAgent = 50

var (
	errNotFound        = apperr.NotFound("trigger_not_found", "Trigger not found")
	errHookNotFound    = apperr.NotFound("hook_not_found", "No webhook at this address")
	errRunNotFound     = apperr.NotFound("run_not_found", "Run not found")
	errBadSignature    = apperr.New(401, "invalid_signature", "The X-PDP-Signature header is missing, malformed, stale or wrong")
	errBadKind         = apperr.Invalid("invalid_trigger_kind", "kind must be schedule or webhook")
	errBadName         = apperr.Invalid("invalid_trigger_name", "Name must be 1-100 characters on one line")
	errNameTaken       = apperr.Conflict("trigger_name_taken", "The agent already has a trigger with that name")
	errTooMany         = apperr.Conflict("too_many_triggers", "An agent can have at most 50 triggers")
	errBadThread       = apperr.Invalid("invalid_thread_id", "threadId is 1-64 letters, digits, dashes or underscores, or empty for a new thread at every fire")
	errBadTemplate     = apperr.Invalid("invalid_message_template", "The message template must be 1-8,000 characters whose placeholders are {{payload}}, {{payload.<path>}}, {{trigger.name}}, {{trigger.kind}}, {{fire.id}} or {{fire.time}}")
	errTemplatePayload = apperr.Invalid("invalid_message_template", "A schedule has no payload: {{payload}} is for webhooks")
	errBadCron         = apperr.Invalid("invalid_cron", "cron must be five fields (minute hour day month weekday) or @hourly, @daily, @weekly, @monthly or @yearly, and fire at some time")
	errBadZone         = apperr.Invalid("invalid_timezone", "timezone must be an IANA time zone such as UTC or Europe/Paris")
	errBadMissed       = apperr.Invalid("invalid_missed_policy", "missedPolicy must be skip or run_once")
	errBadFilter       = apperr.Invalid("invalid_filter", "The filter is a path (names or indexes joined by dots, at most 10) and a value of at most 1,000 characters")
	errBadCallback     = apperr.Invalid("invalid_callback_url", "callbackUrl must be an https URL of at most 2,000 characters")
	errBadBudget       = apperr.Invalid("invalid_budget", "A daily budget is a number of US dollars from 0 to 1,000,000")
	errWrongKind       = apperr.Invalid("invalid_trigger", "That setting is for the other kind of trigger")
	errBadSecretKind   = apperr.Invalid("invalid_secret_kind", "which must be webhook (a webhook trigger's) or callback (a trigger with a callback)")
	errBadLimits       = apperr.Invalid("invalid_trigger_limits", "maxConcurrentRuns and maxRunSeconds must be within the platform's ceilings")
	errAddrNotAllowed  = apperr.New(422, "address_not_allowed", "The callback URL's address is not one teams may use")
	errAddrUnresolved  = apperr.New(422, "address_unresolved", "The callback URL's host could not be resolved")
	errAgentMadeChange = apperr.Conflict("agent_made_trigger", "The agent made this schedule: it can be paused, resumed, run now or deleted, not changed")
)

// onlyPause reports whether a change only pauses or resumes.
func onlyPause(in Input) bool {
	return in.Name == nil && in.ThreadID == nil && in.MessageTemplate == nil && in.Cron == nil && in.Timezone == nil &&
		in.MissedPolicy == nil && in.Filter == nil && in.CallbackURL == nil && in.DailyBudgetUSD == nil
}

// Runner does what a run needs of the agent (agents.Service).
type Runner interface {
	SendTriggered(ctx context.Context, m agents.TriggerMessage) error
	StopThread(ctx context.Context, agentID uuid.UUID, threadID string) error
	ThreadExcerpt(ctx context.Context, agentID uuid.UUID, threadID string, max int) (string, error)
}

// Locker takes named locks shared by every replica (store.Locks).
type Locker interface {
	TryLock(ctx context.Context, key string) (release func(), ok bool, err error)
}

// Service manages triggers and runs them.
type Service struct {
	pool   *pgxpool.Pool
	q      *dbgen.Queries
	box    *secrets.Box
	runner Runner
	cfg    config.Triggers
	appURL string
	guard  *teamnet.Guard
	locker Locker
	log    *slog.Logger

	// Now is the clock (tests set one of their own).
	Now func() time.Time

	wg sync.WaitGroup // deliveries started by requests
}

// New returns a Service. appURL is the platform's address (webhook
// addresses and run links).
func New(pool *pgxpool.Pool, box *secrets.Box, runner Runner, cfg config.Triggers, appURL string, log *slog.Logger) *Service {
	q := dbgen.New(pool)
	return &Service{pool: pool, q: q, box: box, runner: runner, cfg: cfg, appURL: strings.TrimRight(appURL, "/"), log: log,
		guard: teamnet.New(teamnet.SettingsNetworks(q), 10*time.Second), Now: time.Now}
}

// SetLocker makes the trigger loop take one pass at a time across replicas.
func (s *Service) SetLocker(l Locker) { s.locker = l }

// SetTeamNetwork sets how callbacks reach teams' URLs (at start-up; tests
// replace the resolver, dialer and TLS configuration).
func (s *Service) SetTeamNetwork(n teamnet.Config) { s.guard.Set(n) }

// Wait waits for deliveries that requests started (tests, shutdown).
func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) now() time.Time { return s.Now().UTC() }

// View is a trigger as the API shows it: never its secrets.
type View struct {
	Trigger    dbgen.AgentTrigger
	WebhookURL string
	// MessageHidden: the message (Trigger.MessageTemplate, then "") is
	// withheld from the caller (withhold).
	MessageHidden bool
}

func (s *Service) view(t dbgen.AgentTrigger) View {
	v := View{Trigger: t}
	if t.HookID != nil {
		v.WebhookURL = s.appURL + "/v1/hooks/" + *t.HookID
	}
	return v
}

// Saved is a trigger after a change, with any secret made by it (shown
// once).
type Saved struct {
	View
	WebhookSecret, CallbackSecret string
}

// Input is a trigger's settings as a request gives them (nil: unchanged, or
// the default when creating). ThreadID "" is a new thread at every fire;
// CallbackURL "" removes the callback; a Filter with an empty Path removes
// the filter; a negative DailyBudgetUSD removes the budget.
type Input struct {
	Kind            string
	Name            *string
	Paused          *bool
	ThreadID        *string
	MessageTemplate *string
	Cron            *string
	Timezone        *string
	MissedPolicy    *string
	Filter          *Filter
	CallbackURL     *string
	DailyBudgetUSD  *float64
}

// access loads an agent and the actor's relationship to it, with its team.
func access(ctx context.Context, q *dbgen.Queries, a authz.Actor, agentID uuid.UUID) (dbgen.Agent, dbgen.Team, authz.TriggerSubject, error) {
	ag, err := q.GetAgent(ctx, agentID)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return ag, dbgen.Team{}, authz.TriggerSubject{}, apperr.NotFound("agent_not_found", "Agent not found")
	} else if err != nil {
		return ag, dbgen.Team{}, authz.TriggerSubject{}, err
	}
	team, err := q.GetTeamByID(ctx, ag.TeamID)
	if err != nil {
		return ag, team, authz.TriggerSubject{}, err
	}
	sub, err := agents.Subject(ctx, q, a, ag)
	if err != nil {
		return ag, team, authz.TriggerSubject{}, err
	}
	return ag, team, authz.TriggerSubject{AgentSubject: sub,
		Session:  a.Service == nil && a.APIKeyID == uuid.Nil && a.OAuthGrantID == uuid.Nil,
		Archived: team.Status != teams.StatusActive}, nil
}

func (s *Service) readable(ctx context.Context, a authz.Actor, agentID uuid.UUID) (dbgen.Agent, dbgen.Team, error) {
	ag, team, _, err := s.readableAs(ctx, a, agentID)
	return ag, team, err
}

// readableAs is readable, with the actor's relationship to the agent.
func (s *Service) readableAs(ctx context.Context, a authz.Actor, agentID uuid.UUID) (dbgen.Agent, dbgen.Team, authz.AgentSubject, error) {
	ag, team, sub, err := access(ctx, s.q, a, agentID)
	if err != nil {
		return ag, team, sub.AgentSubject, err
	}
	return ag, team, sub.AgentSubject, authz.CheckTriggerRead(sub.AgentSubject)
}

// withhold hides agent-made triggers' messages from those who mustn't read
// them (docs/triggers.md). The agent wrote the message in a conversation, so
// it is conversation-derived text: platform staff, who read the agent's
// triggers through their platform role (never its conversations), never see
// it, whatever the thread or team (a personal workspace's included); people
// and service accounts who may use the agent see it when it was made outside
// a thread or in a thread they may read (shared, or their own private one),
// not when that thread is another's private one or is gone. People's
// triggers' messages are the team's settings and stay visible.
func withhold(ctx context.Context, q *dbgen.Queries, a authz.Actor, sub authz.AgentSubject, views []View) error {
	canUse := authz.CheckAgentUse(sub) == nil
	for i := range views {
		t := &views[i].Trigger
		if t.Source != SourceAgent {
			continue
		}
		hide := !canUse
		if !hide && t.OriginThreadID != nil {
			th, err := q.GetAgentThread(ctx, dbgen.GetAgentThreadParams{AgentID: t.AgentID, ThreadID: *t.OriginThreadID})
			switch {
			case errors.Is(store.NotFound(err), store.ErrNotFound):
				hide = true
			case err != nil:
				return err
			default:
				hide = !agents.CanSeeThread(a, th)
			}
		}
		if hide {
			t.MessageTemplate, views[i].MessageHidden = "", true
		}
	}
	return nil
}

// List returns the agent's triggers (agent-made ones' messages withheld
// from some: withhold).
func (s *Service) List(ctx context.Context, a authz.Actor, agentID uuid.UUID) ([]View, error) {
	_, _, sub, err := s.readableAs(ctx, a, agentID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListAgentTriggers(ctx, agentID)
	if err != nil {
		return nil, err
	}
	out := make([]View, len(rows))
	for i, t := range rows {
		out[i] = s.view(t)
	}
	return out, withhold(ctx, s.q, a, sub, out)
}

// Get returns one of the agent's triggers (withhold applies).
func (s *Service) Get(ctx context.Context, a authz.Actor, agentID, id uuid.UUID) (View, error) {
	_, _, sub, err := s.readableAs(ctx, a, agentID)
	if err != nil {
		return View{}, err
	}
	t, err := s.q.GetTrigger(ctx, dbgen.GetTriggerParams{ID: id, AgentID: agentID})
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return View{}, errNotFound
	} else if err != nil {
		return View{}, err
	}
	out := []View{s.view(t)}
	if err := withhold(ctx, s.q, a, sub, out); err != nil {
		return View{}, err
	}
	return out[0], nil
}

// draft is a trigger's settings being checked.
type draft struct {
	name, template          string
	paused                  bool
	threadID                *string
	cron, zone, missed      *string
	filter                  *Filter
	callbackURL             *string
	budget                  pgtype.Float8
	nextFire                *time.Time
	callbackAdded, rescheds bool
}

// precheck checks what needs the network (a callback URL's address, through
// the address guard), before a transaction holds a connection.
func (s *Service) precheck(ctx context.Context, in Input) error {
	if in.CallbackURL != nil {
		if raw := strings.TrimSpace(*in.CallbackURL); raw != "" {
			return s.checkCallbackURL(ctx, raw)
		}
	}
	return nil
}

// apply checks in over d (the trigger as it is, or defaults for a new one);
// precheck has checked the callback URL's address.
func (s *Service) apply(kind string, d *draft, in Input, now time.Time) error {
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if n := utf8.RuneCountInString(name); n < 1 || n > 100 || strings.ContainsAny(name, "\r\n\x00") {
			return errBadName
		}
		d.name = name
	}
	if in.Paused != nil {
		if d.paused && !*in.Paused {
			d.rescheds = true // resumed: from now, nothing missed
		}
		d.paused = *in.Paused
	}
	if in.ThreadID != nil {
		switch id := strings.TrimSpace(*in.ThreadID); {
		case id == "":
			d.threadID = nil
		case len(id) <= 64 && threadIDRE.MatchString(id):
			d.threadID = &id
		default:
			return errBadThread
		}
	}
	if in.MessageTemplate != nil {
		d.template = *in.MessageTemplate
	}
	if err := checkTemplate(d.template, kind == KindWebhook); err != nil {
		return err
	}
	if kind == KindWebhook && (in.Cron != nil || in.Timezone != nil || in.MissedPolicy != nil) {
		return errWrongKind
	}
	if kind == KindSchedule && in.Filter != nil {
		return errWrongKind
	}
	if kind == KindSchedule {
		if in.Cron != nil {
			c := strings.TrimSpace(*in.Cron)
			d.cron, d.rescheds = &c, true
		}
		if in.Timezone != nil {
			z := strings.TrimSpace(*in.Timezone)
			if z == "" {
				z = "UTC"
			}
			d.zone, d.rescheds = &z, true
		}
		if in.MissedPolicy != nil {
			if *in.MissedPolicy != MissedSkip && *in.MissedPolicy != MissedRunOnce {
				return errBadMissed
			}
			d.missed = in.MissedPolicy
		}
		if d.cron == nil {
			return errBadCron
		}
		sched, err := parseSchedule(*d.cron, *d.zone)
		if err != nil {
			return err
		}
		if d.rescheds || d.nextFire == nil {
			next := sched.next(now)
			d.nextFire = &next
		}
	}
	if in.Filter != nil {
		if in.Filter.Path == "" {
			d.filter = nil
		} else {
			if err := checkFilter(*in.Filter); err != nil {
				return err
			}
			f := *in.Filter
			d.filter = &f
		}
	}
	if in.CallbackURL != nil {
		raw := strings.TrimSpace(*in.CallbackURL)
		if raw == "" {
			d.callbackURL = nil
		} else {
			// Its address was checked before the transaction (precheck).
			d.callbackAdded = d.callbackURL == nil
			d.callbackURL = &raw
		}
	}
	if in.DailyBudgetUSD != nil {
		b := *in.DailyBudgetUSD
		switch {
		case b < 0:
			d.budget = pgtype.Float8{}
		case math.IsNaN(b) || math.IsInf(b, 0) || b > 1e6:
			return errBadBudget
		default:
			d.budget = pgtype.Float8{Float64: b, Valid: true}
		}
	}
	return nil
}

func (s *Service) checkCallbackURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2000 || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errBadCallback
	}
	switch err := s.guard.CheckURL(ctx, raw); {
	case errors.Is(err, teamnet.ErrScheme):
		return errBadCallback
	case errors.Is(err, teamnet.ErrNotAllowed):
		return errAddrNotAllowed
	case errors.Is(err, teamnet.ErrUnresolved):
		return errAddrUnresolved
	case err != nil:
		return err
	}
	return nil
}

// manageable loads the agent (its team locked) for a change by the actor.
func manageable(ctx context.Context, q *dbgen.Queries, a authz.Actor, agentID uuid.UUID) (dbgen.Agent, dbgen.Team, error) {
	ag, team, sub, err := access(ctx, q, a, agentID)
	if err != nil {
		return ag, team, err
	}
	return ag, team, authz.CheckTriggerManage(sub)
}

func (s *Service) seal(plain, ad string) ([]byte, error) {
	return s.box.Seal([]byte(plain), []byte(ad))
}

func webhookAD(id uuid.UUID) string  { return "trigger_webhook:" + id.String() }
func callbackAD(id uuid.UUID) string { return "trigger_callback:" + id.String() }
func runAD(id uuid.UUID) string      { return "trigger_run:" + id.String() }

func prefixOf(secret string) *string {
	p := secret[:shownPrefix]
	return &p
}

// Create adds a trigger to the agent. Its secrets (a webhook's, a callback's)
// are returned once.
func (s *Service) Create(ctx context.Context, a authz.Actor, agentID uuid.UUID, in Input) (Saved, error) {
	if in.Kind != KindSchedule && in.Kind != KindWebhook {
		return Saved{}, errBadKind
	}
	if in.Name == nil {
		return Saved{}, errBadName
	}
	if in.MessageTemplate == nil {
		return Saved{}, errBadTemplate
	}
	now := s.now()
	utc, skip := "UTC", MissedSkip
	d := draft{}
	if in.Kind == KindSchedule {
		d.zone, d.missed = &utc, &skip
	}
	// Authorized before the callback's address is looked up (and again in the transaction).
	if _, _, err := manageable(ctx, s.q, a, agentID); err != nil {
		return Saved{}, err
	}
	if err := s.precheck(ctx, in); err != nil {
		return Saved{}, err
	}
	var out Saved
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ag, _, err := manageable(ctx, q, a, agentID)
		if err != nil {
			return err
		}
		if err := s.apply(in.Kind, &d, in, now); err != nil {
			return err
		}
		if n, err := q.CountAgentTriggers(ctx, ag.ID); err != nil {
			return err
		} else if n >= MaxPerAgent {
			return errTooMany
		}
		p := dbgen.InsertTriggerParams{AgentID: ag.ID, Kind: in.Kind, Name: d.name, Source: SourcePerson, ThreadID: d.threadID,
			MessageTemplate: d.template, Cron: d.cron, Timezone: d.zone, MissedPolicy: d.missed, NextFireAt: d.nextFire,
			DailyBudgetUsd: d.budget, CallbackUrl: d.callbackURL, CreatedBy: uuid.NullUUID{UUID: a.UserID, Valid: a.UserID != uuid.Nil},
			Now: now}
		if d.filter != nil {
			p.FilterPath, p.FilterEquals = &d.filter.Path, &d.filter.Equals
		}
		// Sealed once the ID is known (it is the associated data): a placeholder first.
		placeholder := []byte{0}
		if in.Kind == KindWebhook {
			hook := newHookID()
			out.WebhookSecret = newSecret(webhookSecretPrefix)
			p.HookID, p.WebhookSecretEnc, p.WebhookSecretPrefix = &hook, placeholder, prefixOf(out.WebhookSecret)
		}
		if d.callbackURL != nil {
			out.CallbackSecret = newSecret(callbackSecretPrefix)
			p.CallbackSecretEnc, p.CallbackSecretPrefix = placeholder, prefixOf(out.CallbackSecret)
		}
		t, err := q.InsertTrigger(ctx, p)
		if apperr.IsUniqueViolation(err, "agent_triggers_name_key") {
			return errNameTaken
		} else if err != nil {
			return err
		}
		var sp dbgen.TriggerSetSecretsParams
		sp.ID = t.ID
		if out.WebhookSecret != "" {
			if sp.WebhookSecretEnc, err = s.seal(out.WebhookSecret, webhookAD(t.ID)); err != nil {
				return err
			}
		}
		if out.CallbackSecret != "" {
			if sp.CallbackSecretEnc, err = s.seal(out.CallbackSecret, callbackAD(t.ID)); err != nil {
				return err
			}
		}
		if err := q.TriggerSetSecrets(ctx, sp); err != nil {
			return err
		}
		t.WebhookSecretEnc, t.CallbackSecretEnc = sp.WebhookSecretEnc, sp.CallbackSecretEnc
		e := a.Audit("trigger.create", "trigger", t.ID.String())
		e.TeamID, e.After = ag.TeamID, snapshot(t)
		out.View = s.view(t)
		return audit.Record(ctx, q, e)
	})
	return out, err
}

// Update changes a trigger (expectedRevision: the revision the caller
// loaded). A callback added now gets a secret, returned once.
func (s *Service) Update(ctx context.Context, a authz.Actor, agentID, id uuid.UUID, in Input, expectedRevision int64) (Saved, error) {
	if in.Kind != "" {
		return Saved{}, apperr.Invalid("invalid_trigger", "A trigger's kind can't change")
	}
	now := s.now()
	// Authorized before the callback's address is looked up (and again in the transaction).
	if _, _, err := manageable(ctx, s.q, a, agentID); err != nil {
		return Saved{}, err
	}
	if err := s.precheck(ctx, in); err != nil {
		return Saved{}, err
	}
	var out Saved
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ag, _, err := manageable(ctx, q, a, agentID)
		if err != nil {
			return err
		}
		t, err := q.LockTrigger(ctx, dbgen.LockTriggerParams{ID: id, AgentID: ag.ID})
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errNotFound
		} else if err != nil {
			return err
		}
		if t.Revision != expectedRevision {
			return apperr.Stale()
		}
		if t.Source == SourceAgent && !onlyPause(in) {
			return errAgentMadeChange
		}
		d := draft{name: t.Name, template: t.MessageTemplate, paused: t.Paused, threadID: t.ThreadID, cron: t.Cron,
			zone: t.Timezone, missed: t.MissedPolicy, callbackURL: t.CallbackUrl, budget: t.DailyBudgetUsd, nextFire: t.NextFireAt}
		if t.FilterPath != nil {
			d.filter = &Filter{Path: *t.FilterPath, Equals: *t.FilterEquals}
		}
		if err := s.apply(t.Kind, &d, in, now); err != nil {
			return err
		}
		p := dbgen.UpdateTriggerParams{ID: t.ID, Name: d.name, Paused: d.paused, PausedReason: t.PausedReason,
			ThreadID: d.threadID, MessageTemplate: d.template, Cron: d.cron, Timezone: d.zone, MissedPolicy: d.missed,
			NextFireAt: d.nextFire, WebhookSecretEnc: t.WebhookSecretEnc, WebhookSecretPrefix: t.WebhookSecretPrefix,
			CallbackUrl: d.callbackURL, CallbackSecretEnc: t.CallbackSecretEnc, CallbackSecretPrefix: t.CallbackSecretPrefix,
			DailyBudgetUsd: d.budget, ConsecutiveFailures: t.ConsecutiveFailures, BackoffUntil: t.BackoffUntil, Now: now}
		if d.filter != nil {
			p.FilterPath, p.FilterEquals = &d.filter.Path, &d.filter.Equals
		}
		switch {
		case d.paused && !t.Paused:
			reason := "user"
			p.PausedReason = &reason
		case !d.paused && t.Paused:
			// Resumed: a fresh start (no back-off, the failures forgotten).
			p.PausedReason, p.ConsecutiveFailures, p.BackoffUntil = nil, 0, nil
			// A one-time reminder keeps its time, or fires at the next pass
			// once it has passed (its cron would name next year's); one that
			// fired already (no next time: its run is in progress) stays so.
			if t.Once {
				p.NextFireAt = t.NextFireAt
				if t.NextFireAt != nil && !t.NextFireAt.After(now) {
					p.NextFireAt = &now
				}
			}
		}
		if d.callbackURL == nil {
			p.CallbackSecretEnc, p.CallbackSecretPrefix = nil, nil
		} else if d.callbackAdded {
			out.CallbackSecret = newSecret(callbackSecretPrefix)
			if p.CallbackSecretEnc, err = s.seal(out.CallbackSecret, callbackAD(t.ID)); err != nil {
				return err
			}
			p.CallbackSecretPrefix = prefixOf(out.CallbackSecret)
		}
		updated, err := q.UpdateTrigger(ctx, p)
		if apperr.IsUniqueViolation(err, "agent_triggers_name_key") {
			return errNameTaken
		} else if err != nil {
			return err
		}
		e := a.Audit("trigger.update", "trigger", t.ID.String())
		e.TeamID, e.Before, e.After = ag.TeamID, snapshot(t), snapshot(updated)
		out.View = s.view(updated)
		return audit.Record(ctx, q, e)
	})
	if err != nil || out.Trigger.Source != SourceAgent {
		return out, err
	}
	// Paused or resumed: its message as the caller may see it.
	_, _, sub, err := access(ctx, s.q, a, agentID)
	if err != nil {
		return Saved{}, err
	}
	views := []View{out.View}
	if err := withhold(ctx, s.q, a, sub.AgentSubject, views); err != nil {
		return Saved{}, err
	}
	out.View = views[0]
	return out, nil
}

// Delete removes a trigger: its address stops working, and its runs stay
// (with its name).
func (s *Service) Delete(ctx context.Context, a authz.Actor, agentID, id uuid.UUID) error {
	return store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ag, _, err := manageable(ctx, q, a, agentID)
		if err != nil {
			return err
		}
		t, err := q.LockTrigger(ctx, dbgen.LockTriggerParams{ID: id, AgentID: ag.ID})
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errNotFound
		} else if err != nil {
			return err
		}
		if _, err := q.DeleteTrigger(ctx, dbgen.DeleteTriggerParams{ID: id, AgentID: ag.ID}); err != nil {
			return err
		}
		e := a.Audit("trigger.delete", "trigger", t.ID.String())
		e.TeamID, e.Before = ag.TeamID, snapshot(t)
		return audit.Record(ctx, q, e)
	})
}

// Secret kinds RotateSecret takes.
const (
	SecretWebhook  = "webhook"
	SecretCallback = "callback"
)

// RotateSecret replaces a trigger's webhook or callback secret; the old one
// stops working at once. The new one is returned once.
func (s *Service) RotateSecret(ctx context.Context, a authz.Actor, agentID, id uuid.UUID, which string) (Saved, error) {
	now := s.now()
	var out Saved
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ag, _, err := manageable(ctx, q, a, agentID)
		if err != nil {
			return err
		}
		t, err := q.LockTrigger(ctx, dbgen.LockTriggerParams{ID: id, AgentID: ag.ID})
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errNotFound
		} else if err != nil {
			return err
		}
		p := dbgen.UpdateTriggerParams{ID: t.ID, Name: t.Name, Paused: t.Paused, PausedReason: t.PausedReason, ThreadID: t.ThreadID,
			MessageTemplate: t.MessageTemplate, Cron: t.Cron, Timezone: t.Timezone, MissedPolicy: t.MissedPolicy, NextFireAt: t.NextFireAt,
			WebhookSecretEnc: t.WebhookSecretEnc, WebhookSecretPrefix: t.WebhookSecretPrefix, FilterPath: t.FilterPath,
			FilterEquals: t.FilterEquals, CallbackUrl: t.CallbackUrl, CallbackSecretEnc: t.CallbackSecretEnc,
			CallbackSecretPrefix: t.CallbackSecretPrefix, DailyBudgetUsd: t.DailyBudgetUsd, ConsecutiveFailures: t.ConsecutiveFailures,
			BackoffUntil: t.BackoffUntil, Now: now}
		switch {
		case which == SecretWebhook && t.Kind == KindWebhook:
			out.WebhookSecret = newSecret(webhookSecretPrefix)
			if p.WebhookSecretEnc, err = s.seal(out.WebhookSecret, webhookAD(t.ID)); err != nil {
				return err
			}
			p.WebhookSecretPrefix = prefixOf(out.WebhookSecret)
		case which == SecretCallback && t.CallbackUrl != nil:
			out.CallbackSecret = newSecret(callbackSecretPrefix)
			if p.CallbackSecretEnc, err = s.seal(out.CallbackSecret, callbackAD(t.ID)); err != nil {
				return err
			}
			p.CallbackSecretPrefix = prefixOf(out.CallbackSecret)
		default:
			return errBadSecretKind
		}
		updated, err := q.UpdateTrigger(ctx, p)
		if err != nil {
			return err
		}
		e := a.Audit("trigger.secret_rotate", "trigger", t.ID.String())
		e.TeamID = ag.TeamID
		e.Metadata = withMeta(e.Metadata, map[string]any{"secret": which, "name": t.Name})
		out.View = s.view(updated)
		return audit.Record(ctx, q, e)
	})
	return out, err
}

func withMeta(m map[string]any, add map[string]any) map[string]any {
	if m == nil {
		m = map[string]any{}
	}
	for k, v := range add {
		m[k] = v
	}
	return m
}

// snapshot is a trigger for the audit log: its settings, never its secrets.
func snapshot(t dbgen.AgentTrigger) map[string]any {
	m := map[string]any{"agentId": t.AgentID.String(), "kind": t.Kind, "name": t.Name, "source": t.Source, "paused": t.Paused,
		"messageTemplateLength": utf8.RuneCountInString(t.MessageTemplate)}
	if t.Once {
		m["once"] = true
	}
	if t.OriginThreadID != nil {
		m["originThreadId"] = *t.OriginThreadID
	}
	if t.ThreadID != nil {
		m["threadId"] = *t.ThreadID
	}
	if t.Cron != nil {
		m["cron"], m["timezone"], m["missedPolicy"] = *t.Cron, *t.Timezone, *t.MissedPolicy
	}
	if t.FilterPath != nil {
		m["filterPath"] = *t.FilterPath
	}
	if t.CallbackUrl != nil {
		m["callback"] = true
	}
	if t.DailyBudgetUsd.Valid {
		m["dailyBudgetUsd"] = t.DailyBudgetUsd.Float64
	}
	if t.PausedReason != nil {
		m["pausedReason"] = *t.PausedReason
	}
	return m
}
