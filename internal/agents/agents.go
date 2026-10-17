// Package agents manages agents: durable Pi agents made from templates,
// either a person's own assistant or a team's.
//
// An agent's conversations live in its own schema in the agents database,
// reached with its own database role (internal/agenthost/provision). The
// platform stores the role's password and the runtime's bearer token sealed
// with ENCRYPTION_KEY. Runtimes are started on demand and hibernate when idle
// (internal/agenthost); this package decides who may reach which agent.
package agents

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/agenthost"
	"github.com/ncecere/pi-durable-platform/internal/agenthost/launcher"
	"github.com/ncecere/pi-durable-platform/internal/agenthost/provision"
	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

// DefaultTemplate is the template personal assistants are made from.
const DefaultTemplate = "assistant"

// MaxPerTeam is how many agents a team may have.
const MaxPerTeam = 50

var (
	errNotFound         = apperr.NotFound("agent_not_found", "Agent not found")
	errTemplateNotFound = apperr.NotFound("template_not_found", "Agent template not found")
	errTeamNotFound     = apperr.NotFound("team_not_found", "Team not found")
	errTeamArchived     = apperr.Conflict("team_archived", "This team is archived and read-only")
	errNameTaken        = apperr.Conflict("name_taken", "The team already has an agent with that name")
	errTooMany          = apperr.Conflict("too_many_agents", "A team can have at most 50 agents")
	errDisabled         = apperr.Conflict("agent_disabled", "This agent is turned off")
	errBadName          = apperr.Invalid("invalid_name", "Name must be 1-100 characters on one line")
	errBadDesc          = apperr.Invalid("invalid_description", "Description must be at most 2000 characters")
)

// Service manages agents.
type Service struct {
	pool       *pgxpool.Pool // platform metadata
	agentsPool *pgxpool.Pool // the agents database (provisioning)
	q          *dbgen.Queries
	box        *secrets.Box
	prov       *provision.Provisioner
	host       *agenthost.Manager
	cfg        config.Agents
	agentsURL  string // AGENTS_DATABASE_URL, with RuntimeDatabaseHost applied
}

// New returns a Service. pool is the platform database; agentsPool the
// agents database, where each agent gets its schema and role.
func New(pool, agentsPool *pgxpool.Pool, box *secrets.Box, host *agenthost.Manager, cfg config.Agents) *Service {
	s := &Service{pool: pool, agentsPool: agentsPool, q: dbgen.New(pool), box: box, prov: provision.New(agentsPool), host: host, cfg: cfg, agentsURL: cfg.DatabaseURL}
	host.Resolve = s.resolve
	return s
}

// Prepare hardens the agents database (idempotent; at start-up).
func (s *Service) Prepare(ctx context.Context) error { return s.prov.Prepare(ctx) }

// Ping checks the agents database (readiness).
func (s *Service) Ping(ctx context.Context) error { return s.agentsPool.Ping(ctx) }

// Close releases the agents database pool.
func (s *Service) Close() { s.agentsPool.Close() }

// Host is the runtime manager (for the HTTP proxy).
func (s *Service) Host() *agenthost.Manager { return s.host }

// View is an agent with the names it is shown with and whether it runs now.
type View struct {
	Agent        dbgen.Agent
	TeamSlug     *string
	TeamName     *string
	TemplateSlug string
	TemplateName string
	Running      bool
}

// Templates lists the agent templates (any signed-in person).
func (s *Service) Templates(ctx context.Context) ([]dbgen.AgentTemplate, error) {
	return s.q.ListAgentTemplates(ctx)
}

// List returns the agents the actor may use: their assistant and their
// teams' agents.
func (s *Service) List(ctx context.Context, a authz.Actor) ([]View, error) {
	rows, err := s.q.ListAgentsForUser(ctx, uuid.NullUUID{UUID: a.UserID, Valid: true})
	if err != nil {
		return nil, err
	}
	out := make([]View, len(rows))
	for i, r := range rows {
		out[i] = View{
			Agent: dbgen.Agent{ID: r.ID, Kind: r.Kind, OwnerUserID: r.OwnerUserID, TeamID: r.TeamID, TemplateID: r.TemplateID,
				Name: r.Name, Description: r.Description, Enabled: r.Enabled, Revision: r.Revision, CreatedBy: r.CreatedBy,
				CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, LastUsedAt: r.LastUsedAt},
			TeamSlug: r.TeamSlug, TeamName: r.TeamName, TemplateSlug: r.TemplateSlug, TemplateName: r.TemplateName,
			Running: s.host.Running(r.ID.String()),
		}
	}
	return out, nil
}

// subject resolves the actor's relationship to an agent.
func subject(ctx context.Context, q *dbgen.Queries, a authz.Actor, ag dbgen.Agent) (authz.AgentSubject, error) {
	sub := authz.AgentSubject{Kind: ag.Kind}
	switch ag.Kind {
	case authz.AgentPersonal:
		sub.IsOwner = ag.OwnerUserID.Valid && ag.OwnerUserID.UUID == a.UserID
	case authz.AgentTeam:
		role, err := teams.MemberRole(ctx, q, ag.TeamID.UUID, a.UserID)
		if err != nil {
			return sub, err
		}
		sub.Role = role
	}
	return sub, nil
}

func (s *Service) view(ctx context.Context, q *dbgen.Queries, ag dbgen.Agent) (View, error) {
	tpl, err := q.GetAgentTemplate(ctx, ag.TemplateID)
	if err != nil {
		return View{}, err
	}
	v := View{Agent: ag, TemplateSlug: tpl.Slug, TemplateName: tpl.Name, Running: s.host.Running(ag.ID.String())}
	if ag.TeamID.Valid {
		t, err := q.GetTeamByID(ctx, ag.TeamID.UUID)
		if err != nil {
			return View{}, err
		}
		v.TeamSlug, v.TeamName = &t.Slug, &t.Name
	}
	return v, nil
}

func (s *Service) load(ctx context.Context, q *dbgen.Queries, id uuid.UUID) (dbgen.Agent, error) {
	ag, err := q.GetAgent(ctx, id)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return dbgen.Agent{}, errNotFound
	}
	return ag, err
}

// Get returns an agent the actor may use.
func (s *Service) Get(ctx context.Context, a authz.Actor, id uuid.UUID) (View, error) {
	ag, err := s.load(ctx, s.q, id)
	if err != nil {
		return View{}, err
	}
	sub, err := subject(ctx, s.q, a, ag)
	if err != nil {
		return View{}, err
	}
	if err := authz.CheckAgentUse(sub); err != nil {
		return View{}, err
	}
	return s.view(ctx, s.q, ag)
}

// Personal returns the actor's assistant, creating it from the default
// template the first time (idempotent; concurrent calls create one).
func (s *Service) Personal(ctx context.Context, a authz.Actor) (View, bool, error) {
	if ag, err := s.q.GetPersonalAgent(ctx, uuid.NullUUID{UUID: a.UserID, Valid: true}); err == nil {
		v, err := s.view(ctx, s.q, ag)
		return v, false, err
	} else if !errors.Is(store.NotFound(err), store.ErrNotFound) {
		return View{}, false, err
	}
	var out View
	created := false
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, tx pgx.Tx) error {
		// Serialize creation per person.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('pdp-personal-agent:' || $1::text, 0))", a.UserID.String()); err != nil {
			return err
		}
		if ag, err := q.GetPersonalAgent(ctx, uuid.NullUUID{UUID: a.UserID, Valid: true}); err == nil {
			out, err = s.view(ctx, q, ag)
			return err
		} else if !errors.Is(store.NotFound(err), store.ErrNotFound) {
			return err
		}
		tpl, err := q.GetAgentTemplateBySlug(ctx, DefaultTemplate)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errTemplateNotFound
		} else if err != nil {
			return err
		}
		ag, err := s.insert(ctx, q, a, dbgen.InsertAgentParams{
			Kind: authz.AgentPersonal, OwnerUserID: uuid.NullUUID{UUID: a.UserID, Valid: true},
			TemplateID: tpl.ID, Name: "My assistant", Description: tpl.Description,
		})
		if err != nil {
			return err
		}
		created = true
		out, err = s.view(ctx, q, ag)
		return err
	})
	return out, created, err
}

// CreateInput is a new team agent.
type CreateInput struct {
	Name, Description, Template string
}

// CreateTeamAgent makes an agent for a team (editors and above).
func (s *Service) CreateTeamAgent(ctx context.Context, a authz.Actor, teamRef string, in CreateInput) (View, error) {
	in.Name = strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > 100 || strings.ContainsAny(in.Name, "\r\n\x00") {
		return View{}, errBadName
	}
	if utf8.RuneCountInString(in.Description) > 2000 {
		return View{}, errBadDesc
	}
	if in.Template == "" {
		in.Template = DefaultTemplate
	}
	var out View
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		t, err := teams.Resolve(ctx, q, teamRef)
		if err != nil {
			return err
		}
		role, err := teams.MemberRole(ctx, q, t.ID, a.UserID)
		if err != nil {
			return err
		}
		if role == "" {
			return errTeamNotFound
		}
		if err := authz.CheckTeamAgentCreate(role); err != nil {
			return err
		}
		if _, err := q.LockTeam(ctx, t.ID); err != nil {
			return err
		}
		if t.Status != teams.StatusActive {
			return errTeamArchived
		}
		if n, err := q.CountTeamAgents(ctx, uuid.NullUUID{UUID: t.ID, Valid: true}); err != nil {
			return err
		} else if n >= MaxPerTeam {
			return errTooMany
		}
		tpl, err := q.GetAgentTemplateBySlug(ctx, in.Template)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errTemplateNotFound
		} else if err != nil {
			return err
		}
		desc := in.Description
		if desc == "" {
			desc = tpl.Description
		}
		ag, err := s.insert(ctx, q, a, dbgen.InsertAgentParams{
			Kind: authz.AgentTeam, TeamID: uuid.NullUUID{UUID: t.ID, Valid: true},
			TemplateID: tpl.ID, Name: in.Name, Description: desc,
		})
		if err != nil {
			return err
		}
		out, err = s.view(ctx, q, ag)
		return err
	})
	return out, err
}

// insert provisions the agent's storage and records it. If recording fails,
// the storage is removed again.
func (s *Service) insert(ctx context.Context, q *dbgen.Queries, a authz.Actor, p dbgen.InsertAgentParams) (dbgen.Agent, error) {
	p.ID = uuid.New()
	p.CreatedBy = uuid.NullUUID{UUID: a.UserID, Valid: a.UserID != uuid.Nil}
	creds, err := s.prov.Create(ctx, p.ID.String())
	if err != nil {
		return dbgen.Agent{}, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = s.prov.Delete(context.WithoutCancel(ctx), p.ID.String())
		}
	}()
	ad := []byte(p.ID.String())
	if p.DbPasswordEnc, err = s.box.Seal([]byte(creds.Password), ad); err != nil {
		return dbgen.Agent{}, err
	}
	if p.RuntimeTokenEnc, err = s.box.Seal([]byte(httpx.NewID(32)), ad); err != nil {
		return dbgen.Agent{}, err
	}
	ag, err := q.InsertAgent(ctx, p)
	if apperr.IsUniqueViolation(err, "agents_team_name_key") {
		return dbgen.Agent{}, errNameTaken
	} else if err != nil {
		return dbgen.Agent{}, err
	}
	e := a.Audit("agent.create", "agent", ag.ID.String())
	e.After = snapshot(ag)
	if ag.TeamID.Valid {
		e.TeamID = ag.TeamID.UUID
	}
	if err := audit.Record(ctx, q, e); err != nil {
		return dbgen.Agent{}, err
	}
	ok = true
	return ag, nil
}

func snapshot(ag dbgen.Agent) map[string]any {
	return map[string]any{"kind": ag.Kind, "name": ag.Name, "description": ag.Description, "enabled": ag.Enabled}
}

// UpdateInput holds optional changes.
type UpdateInput struct {
	Name, Description *string
	Enabled           *bool
}

// Update changes an agent's name, description or whether it is on. Turning it
// off hibernates it and refuses new work.
func (s *Service) Update(ctx context.Context, a authz.Actor, id uuid.UUID, in UpdateInput, expectedRevision int64) (View, error) {
	var out View
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ag, err := q.LockAgent(ctx, id)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errNotFound
		} else if err != nil {
			return err
		}
		sub, err := subject(ctx, q, a, ag)
		if err != nil {
			return err
		}
		if err := authz.CheckAgentEdit(sub); err != nil {
			return err
		}
		if ag.Revision != expectedRevision {
			return apperr.Stale()
		}
		p := dbgen.UpdateAgentParams{ID: id, Name: ag.Name, Description: ag.Description, Enabled: ag.Enabled}
		if in.Name != nil {
			p.Name = strings.TrimSpace(*in.Name)
			if n := utf8.RuneCountInString(p.Name); n < 1 || n > 100 || strings.ContainsAny(p.Name, "\r\n\x00") {
				return errBadName
			}
		}
		if in.Description != nil {
			if utf8.RuneCountInString(*in.Description) > 2000 {
				return errBadDesc
			}
			p.Description = *in.Description
		}
		if in.Enabled != nil {
			p.Enabled = *in.Enabled
		}
		updated, err := q.UpdateAgent(ctx, p)
		if apperr.IsUniqueViolation(err, "agents_team_name_key") {
			return errNameTaken
		} else if err != nil {
			return err
		}
		e := a.Audit("agent.update", "agent", id.String())
		e.Before, e.After = snapshot(ag), snapshot(updated)
		if ag.TeamID.Valid {
			e.TeamID = ag.TeamID.UUID
		}
		if err := audit.Record(ctx, q, e); err != nil {
			return err
		}
		out, err = s.view(ctx, q, updated)
		return err
	})
	if err == nil && !out.Agent.Enabled {
		_ = s.host.Hibernate(ctx, id.String())
		out.Running = false
	}
	return out, err
}

// Delete removes an agent and all its conversations. Audited.
func (s *Service) Delete(ctx context.Context, a authz.Actor, id uuid.UUID) error {
	return store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ag, err := q.LockAgent(ctx, id)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errNotFound
		} else if err != nil {
			return err
		}
		sub, err := subject(ctx, q, a, ag)
		if err != nil {
			return err
		}
		if err := authz.CheckAgentDelete(sub, a.IsPlatformAdmin()); err != nil {
			return err
		}
		if err := s.host.Hibernate(ctx, id.String()); err != nil {
			return err
		}
		if err := s.prov.Delete(ctx, id.String()); err != nil {
			return err
		}
		if err := q.DeleteAgent(ctx, id); err != nil {
			return err
		}
		e := a.Audit("agent.delete", "agent", id.String())
		e.Before = snapshot(ag)
		if ag.TeamID.Valid {
			e.TeamID = ag.TeamID.UUID
		}
		return audit.Record(ctx, q, e)
	})
}

// Hibernate stops the agent's runtime now (anyone who may use it).
func (s *Service) Hibernate(ctx context.Context, a authz.Actor, id uuid.UUID) error {
	if _, err := s.Get(ctx, a, id); err != nil {
		return err
	}
	return s.host.Hibernate(ctx, id.String())
}

// Spec returns what is needed to run an agent the actor may use, for the
// proxy. Turned-off agents are refused.
func (s *Service) Spec(ctx context.Context, a authz.Actor, id uuid.UUID) (launcher.Spec, error) {
	ag, err := s.load(ctx, s.q, id)
	if err != nil {
		return launcher.Spec{}, err
	}
	sub, err := subject(ctx, s.q, a, ag)
	if err != nil {
		return launcher.Spec{}, err
	}
	if err := authz.CheckAgentUse(sub); err != nil {
		return launcher.Spec{}, err
	}
	if !ag.Enabled {
		return launcher.Spec{}, errDisabled
	}
	_ = s.q.TouchAgent(ctx, id)
	return s.spec(ctx, ag)
}

// resolve returns an agent's spec without an actor (the reaper).
func (s *Service) resolve(ctx context.Context, agentID string) (launcher.Spec, error) {
	id, err := uuid.Parse(agentID)
	if err != nil {
		return launcher.Spec{}, err
	}
	ag, err := s.load(ctx, s.q, id)
	if err != nil {
		return launcher.Spec{}, err
	}
	return s.spec(ctx, ag)
}

func (s *Service) spec(ctx context.Context, ag dbgen.Agent) (launcher.Spec, error) {
	ad := []byte(ag.ID.String())
	password, err := s.box.Open(ag.DbPasswordEnc, ad)
	if err != nil {
		return launcher.Spec{}, err
	}
	token, err := s.box.Open(ag.RuntimeTokenEnc, ad)
	if err != nil {
		return launcher.Spec{}, err
	}
	tpl, err := s.q.GetAgentTemplate(ctx, ag.TemplateID)
	if err != nil {
		return launcher.Spec{}, err
	}
	schema, err := provision.NameFor(ag.ID.String())
	if err != nil {
		return launcher.Spec{}, err
	}
	creds := provision.Credentials{Schema: schema, Role: schema, Password: string(password)}
	dsn, err := creds.DSN(s.agentsURL, s.cfg.RuntimeDatabaseHost)
	if err != nil {
		return launcher.Spec{}, err
	}
	env := map[string]string{"MODEL_PROVIDER": s.cfg.ModelProvider}
	if s.cfg.ModelProvider == "openai-compatible" {
		env["MODEL_BASE_URL"], env["MODEL_API_KEY"], env["MODEL_ID"] = s.cfg.ModelBaseURL, s.cfg.ModelAPIKey, s.cfg.ModelID
		if s.cfg.ModelReasoning {
			env["MODEL_REASONING"] = "true"
		}
	}
	return launcher.Spec{
		AgentID: ag.ID.String(), Name: ag.Name, Instructions: tpl.Instructions,
		Schema: schema, DatabaseURL: dsn, Token: string(token), Env: env,
	}, nil
}
