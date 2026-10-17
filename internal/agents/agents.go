// Package agents manages agents: durable Pi agents that teams deploy from
// templates, test here, and reach from their own apps with service account
// keys.
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
	"regexp"
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

// DefaultTemplate is the template used when none is named.
const DefaultTemplate = "assistant"

// MaxPerTeam is how many agents a team may create by hand (service-managed
// agents, one per user of a service, are not counted).
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
	errBadKey           = apperr.Invalid("invalid_external_key", "External keys are 1-200 letters, digits or . _ : @ -")
	externalKeyRE       = regexp.MustCompile(`^[A-Za-z0-9._:@-]{1,200}$`)
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
	TeamSlug     string
	TeamName     string
	TemplateSlug string
	TemplateName string
	// TemplateCurrentVersion is the template's newest version (an upgrade is
	// available when it is above the agent's TemplateVersion).
	TemplateCurrentVersion int32
	Running                bool
}

// List returns the agents of the actor's teams (for a service account, its
// team's). Service-managed agents are included only when asked, and only
// for teams where the person is an owner or admin.
func (s *Service) List(ctx context.Context, a authz.Actor, includeManaged bool) ([]View, error) {
	if a.Service != nil {
		return s.ListTeam(ctx, a, a.Service.TeamID.String(), includeManaged)
	}
	rows, err := s.q.ListAgentsForUser(ctx, dbgen.ListAgentsForUserParams{UserID: a.UserID, IncludeManaged: includeManaged})
	if err != nil {
		return nil, err
	}
	out := make([]View, len(rows))
	for i, r := range rows {
		out[i] = View{
			Agent: dbgen.Agent{ID: r.ID, TeamID: r.TeamID, TemplateID: r.TemplateID, Name: r.Name, Description: r.Description,
				Enabled: r.Enabled, Revision: r.Revision, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
				LastUsedAt: r.LastUsedAt, ExternalKey: r.ExternalKey, TemplateVersion: r.TemplateVersion},
			TeamSlug: r.TeamSlug, TeamName: r.TeamName, TemplateSlug: r.TemplateSlug, TemplateName: r.TemplateName,
			TemplateCurrentVersion: r.TemplateCurrentVersion, Running: s.host.Running(r.ID.String()),
		}
	}
	return out, nil
}

// roleIn is the actor's role in a team: a person's membership, or a service
// account's own role in its own team ("" otherwise).
func roleIn(ctx context.Context, q *dbgen.Queries, a authz.Actor, teamID uuid.UUID) (string, error) {
	if a.Service != nil {
		if a.Service.TeamID == teamID {
			return a.Service.Role, nil
		}
		return "", nil
	}
	return teams.MemberRole(ctx, q, teamID, a.UserID)
}

// subject resolves the actor's relationship to an agent.
func subject(ctx context.Context, q *dbgen.Queries, a authz.Actor, ag dbgen.Agent) (authz.AgentSubject, error) {
	role, err := roleIn(ctx, q, a, ag.TeamID)
	return authz.AgentSubject{Role: role, IsService: a.Service != nil, ServiceManaged: ag.ExternalKey != nil}, err
}

func (s *Service) view(ctx context.Context, q *dbgen.Queries, ag dbgen.Agent) (View, error) {
	tpl, err := q.GetAgentTemplate(ctx, ag.TemplateID)
	if err != nil {
		return View{}, err
	}
	t, err := q.GetTeamByID(ctx, ag.TeamID)
	if err != nil {
		return View{}, err
	}
	return View{Agent: ag, TeamSlug: t.Slug, TeamName: t.Name, TemplateSlug: tpl.Slug, TemplateName: tpl.Name,
		TemplateCurrentVersion: tpl.CurrentVersion, Running: s.host.Running(ag.ID.String())}, nil
}

func (s *Service) load(ctx context.Context, q *dbgen.Queries, id uuid.UUID) (dbgen.Agent, error) {
	ag, err := q.GetAgent(ctx, id)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return dbgen.Agent{}, errNotFound
	}
	return ag, err
}

// Get returns an agent the actor may see (its settings and status).
func (s *Service) Get(ctx context.Context, a authz.Actor, id uuid.UUID) (View, error) {
	ag, err := s.load(ctx, s.q, id)
	if err != nil {
		return View{}, err
	}
	sub, err := subject(ctx, s.q, a, ag)
	if err != nil {
		return View{}, err
	}
	if err := authz.CheckAgentSee(sub); err != nil {
		return View{}, err
	}
	return s.view(ctx, s.q, ag)
}

// CreateInput is a new team agent: its template by ID, or a platform
// template by slug (default: the assistant). It gets the template's current
// version.
type CreateInput struct {
	Name, Description, Template string
	TemplateID                  *uuid.UUID
}

// CreateTeamAgent makes an agent for a team (editors and above, people or
// editor service accounts).
func (s *Service) CreateTeamAgent(ctx context.Context, a authz.Actor, teamRef string, in CreateInput) (View, error) {
	v, _, err := s.createInTeam(ctx, a, teamRef, in, nil)
	return v, err
}

// EnsureByKey returns the team's service-managed agent with this external
// key, creating it the first time (editor service accounts only). in names a
// new agent; an existing one is returned as it is. created reports which.
func (s *Service) EnsureByKey(ctx context.Context, a authz.Actor, teamRef, key string, in CreateInput) (View, bool, error) {
	if !externalKeyRE.MatchString(key) {
		return View{}, false, errBadKey
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = key
		if len(in.Name) > 100 {
			in.Name = in.Name[:100]
		}
	}
	return s.createInTeam(ctx, a, teamRef, in, &key)
}

func (s *Service) createInTeam(ctx context.Context, a authz.Actor, teamRef string, in CreateInput, key *string) (View, bool, error) {
	in.Name = strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > 100 || strings.ContainsAny(in.Name, "\r\n\x00") {
		return View{}, false, errBadName
	}
	if utf8.RuneCountInString(in.Description) > 2000 {
		return View{}, false, errBadDesc
	}
	var out View
	created := false
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		t, err := teams.Resolve(ctx, q, teamRef)
		if err != nil {
			return err
		}
		role, err := roleIn(ctx, q, a, t.ID)
		if err != nil {
			return err
		}
		if role == "" {
			return errTeamNotFound
		}
		if key != nil {
			if err := authz.CheckServiceManagedCreate(a.Service != nil, role); err != nil {
				return err
			}
		} else if err := authz.CheckTeamAgentCreate(role); err != nil {
			return err
		}
		if _, err := q.LockTeam(ctx, t.ID); err != nil {
			return err
		}
		if t.Status != teams.StatusActive {
			return errTeamArchived
		}
		if key != nil {
			ag, err := q.GetAgentByExternalKey(ctx, dbgen.GetAgentByExternalKeyParams{TeamID: t.ID, ExternalKey: key})
			if err == nil {
				out, err = s.view(ctx, q, ag)
				return err
			} else if !errors.Is(store.NotFound(err), store.ErrNotFound) {
				return err
			}
		} else if n, err := q.CountTeamAgents(ctx, t.ID); err != nil {
			return err
		} else if n >= MaxPerTeam {
			return errTooMany
		}
		tpl, err := deployable(ctx, q, t.ID, in.TemplateID, in.Template)
		if err != nil {
			return err
		}
		desc := in.Description
		if desc == "" {
			desc = tpl.Description
		}
		ag, err := s.insert(ctx, q, a, dbgen.InsertAgentParams{
			TeamID: t.ID, TemplateID: tpl.ID, TemplateVersion: tpl.CurrentVersion, Name: in.Name, Description: desc, ExternalKey: key,
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

// ListTeam lists a team's agents for its members and service accounts.
// Service-managed agents are included when asked, for the team's service
// accounts and its owners and admins (who see them, not their
// conversations).
func (s *Service) ListTeam(ctx context.Context, a authz.Actor, teamRef string, includeManaged bool) ([]View, error) {
	t, err := teams.Resolve(ctx, s.q, teamRef)
	if err != nil {
		return nil, err
	}
	role, err := roleIn(ctx, s.q, a, t.ID)
	if err != nil {
		return nil, err
	}
	if role == "" {
		return nil, errTeamNotFound
	}
	managed := includeManaged && (a.Service != nil || authz.RoleAtLeast(role, authz.RoleAdmin))
	rows, err := s.q.ListTeamAgents(ctx, dbgen.ListTeamAgentsParams{TeamID: t.ID, IncludeManaged: managed})
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(rows))
	for _, ag := range rows {
		v, err := s.view(ctx, s.q, ag)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// insert provisions the agent's storage and records it. If recording fails,
// the storage is removed again.
func (s *Service) insert(ctx context.Context, q *dbgen.Queries, a authz.Actor, p dbgen.InsertAgentParams) (dbgen.Agent, error) {
	p.ID = uuid.New()
	p.CreatedBy = uuid.NullUUID{UUID: a.UserID, Valid: a.UserID != uuid.Nil && a.Service == nil}
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
	e.After, e.TeamID = snapshot(ag), ag.TeamID
	if err := audit.Record(ctx, q, e); err != nil {
		return dbgen.Agent{}, err
	}
	ok = true
	return ag, nil
}

func snapshot(ag dbgen.Agent) map[string]any {
	m := map[string]any{"name": ag.Name, "description": ag.Description, "enabled": ag.Enabled, "templateVersion": ag.TemplateVersion}
	if ag.ExternalKey != nil {
		m["externalKey"] = *ag.ExternalKey
	}
	return m
}

// UpdateInput holds optional changes. TemplateVersion moves the agent to
// another version of its template (upgrade or roll back); it takes effect
// the next time the agent starts, so a running agent is hibernated.
type UpdateInput struct {
	Name, Description *string
	Enabled           *bool
	TemplateVersion   *int32
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
		p := dbgen.UpdateAgentParams{ID: id, Name: ag.Name, Description: ag.Description, Enabled: ag.Enabled, TemplateVersion: ag.TemplateVersion}
		if in.TemplateVersion != nil && *in.TemplateVersion != ag.TemplateVersion {
			if _, err := version(ctx, q, ag.TemplateID, *in.TemplateVersion); err != nil {
				return err
			}
			p.TemplateVersion = *in.TemplateVersion
		}
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
		e.Before, e.After, e.TeamID = snapshot(ag), snapshot(updated), ag.TeamID
		if err := audit.Record(ctx, q, e); err != nil {
			return err
		}
		out, err = s.view(ctx, q, updated)
		return err
	})
	if err == nil && (!out.Agent.Enabled || (in.TemplateVersion != nil && out.Running)) {
		// Turned off, or a new version: the runtime restarts with it on the next request.
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
		if err := s.host.Remove(ctx, id.String()); err != nil {
			return err
		}
		if err := s.prov.Delete(ctx, id.String()); err != nil {
			return err
		}
		if err := q.DeleteAgent(ctx, id); err != nil {
			return err
		}
		e := a.Audit("agent.delete", "agent", id.String())
		e.Before, e.TeamID = snapshot(ag), ag.TeamID
		return audit.Record(ctx, q, e)
	})
}

// PruneStorage removes agent storage (schema and role) that no agent owns:
// left by a failed deletion, or by agents removed in a migration. Run it
// when no agents are being created: storage is made just before its agent is
// recorded. Returns the names removed (or, with dryRun, that would be).
func (s *Service) PruneStorage(ctx context.Context, dryRun bool) ([]string, error) {
	ids, err := s.q.AgentIDs(ctx)
	if err != nil {
		return nil, err
	}
	keep := map[string]bool{}
	for _, id := range ids {
		n, err := provision.NameFor(id.String())
		if err != nil {
			return nil, err
		}
		keep[n] = true
	}
	names, err := s.prov.Names(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if keep[n] {
			continue
		}
		if !dryRun {
			if err := s.prov.DeleteName(ctx, n); err != nil {
				return out, err
			}
		}
		out = append(out, n)
	}
	return out, nil
}

// DeleteAllStorage removes every agent's storage (tests, before dropping the
// agents database: database roles outlive it).
func (s *Service) DeleteAllStorage(ctx context.Context) error { return s.prov.DeleteAll(ctx) }

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
	tv, err := version(ctx, s.q, ag.TemplateID, ag.TemplateVersion)
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
		AgentID: ag.ID.String(), Name: ag.Name, Instructions: tv.Instructions,
		Schema: schema, DatabaseURL: dsn, Token: string(token), Env: env,
	}, nil
}
