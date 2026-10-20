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
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
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
	"github.com/ncecere/pi-durable-platform/internal/llm"
	"github.com/ncecere/pi-durable-platform/internal/mcp"
	"github.com/ncecere/pi-durable-platform/internal/metrics"
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
	errBadModelID       = apperr.Invalid("invalid_model_id", "modelId is a model's ID, or empty for the template's model")
	errModelUnavailable = apperr.Conflict("agent_model_unavailable", "The agent's model is no longer available to its team: choose another model in its settings")
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
	llm        *llm.Service
	mcp        *mcp.Service
	cfg        config.Agents
	agentsURL  string // AGENTS_DATABASE_URL, with RuntimeDatabaseHost applied
	hub        threadHub
	// Metrics counts thread event streams and agents (nil: none).
	Metrics *metrics.Metrics
}

// New returns a Service. pool is the platform database; agentsPool the
// agents database, where each agent gets its schema and role; models the
// model catalog.
func New(pool, agentsPool *pgxpool.Pool, box *secrets.Box, host *agenthost.Manager, models *llm.Service, servers *mcp.Service, cfg config.Agents) *Service {
	s := &Service{pool: pool, agentsPool: agentsPool, q: dbgen.New(pool), box: box, prov: provision.New(agentsPool), host: host,
		llm: models, mcp: servers, cfg: cfg, agentsURL: cfg.DatabaseURL, hub: threadHub{subs: map[chan ThreadEvent]struct{}{}}}
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
	// Model is the model it runs on (nil: the platform's default model).
	Model *ModelRef
}

// ModelRef is the model an agent or template version uses.
type ModelRef struct {
	ID           uuid.UUID
	Name         string
	ProviderName string
	// Source: "agent" (chosen for this agent) or "template" (its version's).
	Source string
	// Available: enabled, and published to the agent's team.
	Available bool
}

// effectiveModel is the model an agent runs on: its own choice, else its
// template version's (uuid.Nil: the platform's default).
func effectiveModel(ag dbgen.Agent, tv dbgen.AgentTemplateVersion) (uuid.UUID, string) {
	if ag.ModelID.Valid {
		return ag.ModelID.UUID, "agent"
	}
	if tv.ModelID.Valid {
		return tv.ModelID.UUID, "template"
	}
	return uuid.Nil, ""
}

// modelRef describes the agent's model.
func (s *Service) modelRef(ctx context.Context, q *dbgen.Queries, ag dbgen.Agent) (*ModelRef, error) {
	tv, err := version(ctx, q, ag.TemplateID, ag.TemplateVersion)
	if err != nil {
		return nil, err
	}
	id, source := effectiveModel(ag, tv)
	if id == uuid.Nil {
		return nil, nil
	}
	m, err := q.GetModelSummary(ctx, id)
	if err != nil {
		return nil, err
	}
	err = llm.CheckUsable(ctx, q, id, ag.TeamID)
	if err != nil && !errors.Is(err, llm.ErrUnavailable) {
		return nil, err
	}
	return &ModelRef{ID: id, Name: m.Name, ProviderName: m.ProviderName, Source: source, Available: err == nil}, nil
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
			Agent:    r.Agent,
			TeamSlug: r.TeamSlug, TeamName: r.TeamName, TemplateSlug: r.TemplateSlug, TemplateName: r.TemplateName,
			TemplateCurrentVersion: r.TemplateCurrentVersion, Running: s.host.Running(r.Agent.ID.String()),
		}
		if out[i].Model, err = s.modelRef(ctx, s.q, r.Agent); err != nil {
			return nil, err
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
	if err != nil {
		return authz.AgentSubject{}, err
	}
	// Platform staff see any team's agents (read-only); a member's role decides for them on their own teams.
	return authz.AgentSubject{Role: role, IsService: a.Service != nil, ServiceManaged: ag.ServiceManaged,
		Audit: role == "" && a.CanReadPlatform()}, nil
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
	model, err := s.modelRef(ctx, q, ag)
	if err != nil {
		return View{}, err
	}
	return View{Agent: ag, TeamSlug: t.Slug, TeamName: t.Name, TemplateSlug: tpl.Slug, TemplateName: tpl.Name,
		TemplateCurrentVersion: tpl.CurrentVersion, Running: s.host.Running(ag.ID.String()), Model: model}, nil
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
// version, and runs on its model unless ModelID chooses another (one
// published to the team).
type CreateInput struct {
	Name, Description, Template string
	TemplateID                  *uuid.UUID
	ModelID                     *uuid.UUID
}

// CreateTeamAgent makes an agent for a team (editors and above, people or
// editor service accounts).
func (s *Service) CreateTeamAgent(ctx context.Context, a authz.Actor, teamRef string, in CreateInput) (View, error) {
	v, _, err := s.createInTeam(ctx, a, teamRef, in, nil, "")
	return v, err
}

// CreateTeamAgentOnce is CreateTeamAgent with an idempotency key (see
// idempotency.go; "": none). replayed reports that an earlier request with
// the key created the agent returned.
func (s *Service) CreateTeamAgentOnce(ctx context.Context, a authz.Actor, teamRef string, in CreateInput, idempotencyKey string) (View, bool, error) {
	v, created, err := s.createInTeam(ctx, a, teamRef, in, nil, idempotencyKey)
	return v, err == nil && !created, err
}

// EnsureByKey returns the team's agent with this external key, creating it
// the first time: in a shared team a service-managed agent (editor service
// accounts only), in a personal workspace the owner's agent (its owner
// only; authz.CheckKeyedAgentCreate). in names a new agent; an existing one
// is returned as it is. created reports which.
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
	return s.createInTeam(ctx, a, teamRef, in, &key, "")
}

func (s *Service) createInTeam(ctx context.Context, a authz.Actor, teamRef string, in CreateInput, key *string, idempotencyKey string) (View, bool, error) {
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
		personal := t.Kind == teams.KindPersonal
		if key != nil {
			if err := authz.CheckKeyedAgentCreate(a.Service != nil, role, personal); err != nil {
				return err
			}
		} else if err := authz.CheckTeamAgentCreate(role); err != nil {
			return err
		}
		if idempotencyKey != "" {
			// Claimed before the team's lock: a retry waits here for the first request.
			prev, err := s.idempotentClaim(ctx, q, a, idempotencyKey, createFingerprint(t.ID, in))
			if err != nil || prev != nil {
				if prev != nil {
					out = *prev
				}
				return err
			}
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
		// A personal workspace's limit counts every agent, service-managed ones too.
		if limits, err := teams.LimitsOf(ctx, q, t); err != nil {
			return err
		} else if limits.MaxAgents != nil {
			if n, err := q.CountAgentsInTeam(ctx, t.ID); err != nil {
				return err
			} else if n >= int64(*limits.MaxAgents) {
				return teams.LimitError("agents", *limits.MaxAgents)
			}
		}
		tpl, err := deployable(ctx, q, t.ID, in.TemplateID, in.Template)
		if err != nil {
			return err
		}
		desc := in.Description
		if desc == "" {
			desc = tpl.Description
		}
		tv, err := version(ctx, q, tpl.ID, tpl.CurrentVersion)
		if err != nil {
			return err
		}
		modelID := uuid.NullUUID{}
		if in.ModelID != nil {
			modelID = uuid.NullUUID{UUID: *in.ModelID, Valid: true}
		}
		if id, _ := effectiveModel(dbgen.Agent{ModelID: modelID}, tv); id != uuid.Nil {
			if err := llm.CheckUsable(ctx, q, id, t.ID); err != nil {
				return err
			}
		}
		ag, err := s.insert(ctx, q, a, dbgen.InsertAgentParams{
			TeamID: t.ID, TemplateID: tpl.ID, TemplateVersion: tpl.CurrentVersion, Name: in.Name, Description: desc, ExternalKey: key,
			ModelID: modelID, ServiceManaged: key != nil && !personal,
		})
		if err != nil {
			return err
		}
		if idempotencyKey != "" {
			if err := completeIdempotent(ctx, q, a, idempotencyKey, ag.ID); err != nil {
				return err
			}
		}
		created = true
		out, err = s.view(ctx, q, ag)
		return err
	})
	return out, created, err
}

// ListTeam lists a team's agents for its members and service accounts, and
// for platform staff (any team's, read-only). Service-managed agents are
// included when asked, for the team's service accounts, its owners and
// admins, and platform staff (who see them, not their conversations).
func (s *Service) ListTeam(ctx context.Context, a authz.Actor, teamRef string, includeManaged bool) ([]View, error) {
	t, err := teams.Resolve(ctx, s.q, teamRef)
	if err != nil {
		return nil, err
	}
	role, err := roleIn(ctx, s.q, a, t.ID)
	if err != nil {
		return nil, err
	}
	audit := role == "" && a.CanReadPlatform()
	if role == "" && !audit {
		return nil, errTeamNotFound
	}
	managed := includeManaged && (a.Service != nil || authz.RoleAtLeast(role, authz.RoleAdmin) || audit)
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
	if ag.ModelID.Valid {
		m["modelId"] = ag.ModelID.UUID.String()
	}
	if ag.ExternalKey != nil {
		m["externalKey"] = *ag.ExternalKey
	}
	if ag.ServiceManaged {
		m["serviceManaged"] = true
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
	// ModelID chooses the agent's model ("" goes back to the template's).
	// Like a version change, it applies when the agent next starts.
	ModelID *string
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
		p := dbgen.UpdateAgentParams{ID: id, Name: ag.Name, Description: ag.Description, Enabled: ag.Enabled,
			TemplateVersion: ag.TemplateVersion, ModelID: ag.ModelID}
		if in.TemplateVersion != nil && *in.TemplateVersion != ag.TemplateVersion {
			if _, err := version(ctx, q, ag.TemplateID, *in.TemplateVersion); err != nil {
				return err
			}
			p.TemplateVersion = *in.TemplateVersion
		}
		if in.ModelID != nil {
			p.ModelID = uuid.NullUUID{}
			if *in.ModelID != "" {
				mid, err := uuid.Parse(*in.ModelID)
				if err != nil {
					return errBadModelID
				}
				p.ModelID = uuid.NullUUID{UUID: mid, Valid: true}
			}
		}
		if p.TemplateVersion != ag.TemplateVersion || p.ModelID != ag.ModelID {
			tv, err := version(ctx, q, ag.TemplateID, p.TemplateVersion)
			if err != nil {
				return err
			}
			if mid, _ := effectiveModel(dbgen.Agent{ModelID: p.ModelID}, tv); mid != uuid.Nil {
				if err := llm.CheckUsable(ctx, q, mid, ag.TeamID); err != nil {
					return err
				}
			}
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
	if err == nil && (!out.Agent.Enabled || ((in.TemplateVersion != nil || in.ModelID != nil) && out.Running)) {
		// Turned off, or a new version or model: the runtime restarts with it on the next request.
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

// agentIDStrings lists every agent's ID (the platform's, not the agents database's).
func (s *Service) agentIDStrings(ctx context.Context) ([]string, error) {
	ids, err := s.q.AgentIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out, nil
}

// PruneRoles drops the agent roles that belong to no agent and have no schema
// in the agents database (roles are server-wide, so a dropped agents database
// leaves them behind). With dryRun it lists them. Roles that look like agent
// roles but aren't safe to drop are returned as skipped, with the reason.
func (s *Service) PruneRoles(ctx context.Context, dryRun bool) (dropped []string, skipped []provision.SkippedRole, err error) {
	ids, err := s.agentIDStrings(ctx)
	if err != nil {
		return nil, nil, err
	}
	return s.prov.PruneRoles(ctx, ids, dryRun)
}

// DeleteAllStorage removes every agent's storage (tests, before dropping the
// agents database: database roles outlive it).
func (s *Service) DeleteAllStorage(ctx context.Context) error { return s.prov.DeleteAll(ctx) }

// Hibernate stops the agent's runtime now (anyone who may use it).
func (s *Service) Hibernate(ctx context.Context, a authz.Actor, id uuid.UUID) error {
	ag, err := s.load(ctx, s.q, id)
	if err != nil {
		return err
	}
	sub, err := subject(ctx, s.q, a, ag)
	if err != nil {
		return err
	}
	if err := authz.CheckAgentHibernate(sub); err != nil {
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
	if err := s.checkRunning(ctx, ag); err != nil {
		return launcher.Spec{}, err
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
	env, err := s.modelEnv(ctx, ag, tv, string(token))
	if err != nil {
		return launcher.Spec{}, err
	}
	if err := s.mcpEnv(ctx, ag, tv, string(token), env); err != nil {
		return launcher.Spec{}, err
	}
	// Where the runtime reports its threads' status (the gateway).
	env["PLATFORM_GATEWAY_URL"] = strings.TrimRight(s.cfg.GatewayURL, "/")
	env["PLATFORM_KEY"] = llm.ProxyCredential(ag.ID.String(), string(token))
	return launcher.Spec{
		Disabled: !ag.Enabled,
		AgentID:  ag.ID.String(), Name: ag.Name, Instructions: tv.Instructions,
		Schema: schema, DatabaseURL: dsn, Token: string(token), Env: env,
	}, nil
}

// modelEnv is the runtime's model configuration: the agent's catalog model
// (through the platform's model proxy, or the provider directly), or the
// platform's default model (AGENTS_MODEL_*).
func (s *Service) modelEnv(ctx context.Context, ag dbgen.Agent, tv dbgen.AgentTemplateVersion, token string) (map[string]string, error) {
	modelID, _ := effectiveModel(ag, tv)
	// Usage of models the proxy does not see is reported by the agent.
	reportUsage := func(env map[string]string) {
		env["MODEL_USAGE_URL"] = strings.TrimRight(s.cfg.GatewayURL, "/") + "/usage"
		env["MODEL_USAGE_KEY"] = llm.ProxyCredential(ag.ID.String(), token)
	}
	if modelID == uuid.Nil {
		env := map[string]string{"MODEL_PROVIDER": s.cfg.ModelProvider}
		if s.cfg.ModelProvider == "openai-compatible" {
			env["MODEL_BASE_URL"], env["MODEL_API_KEY"], env["MODEL_ID"] = s.cfg.ModelBaseURL, s.cfg.ModelAPIKey, s.cfg.ModelID
			if s.cfg.ModelReasoning {
				env["MODEL_REASONING"] = "true"
			}
			reportUsage(env)
		}
		return env, nil
	}
	r, err := s.llm.Resolve(ctx, modelID, ag.TeamID)
	if errors.Is(err, llm.ErrUnavailable) {
		return nil, errModelUnavailable
	} else if err != nil {
		return nil, err
	}
	env := map[string]string{
		"MODEL_PROVIDER": "catalog", "MODEL_API": r.Kind.API, "MODEL_ID": r.Model.UpstreamID, "MODEL_NAME": r.Model.Name,
		"MODEL_PROVIDER_NAME":  r.Provider.Name,
		"MODEL_CONTEXT_WINDOW": fmt.Sprint(r.Model.ContextWindow), "MODEL_MAX_TOKENS": fmt.Sprint(r.Model.MaxTokens),
		"MODEL_REASONING": fmt.Sprint(r.Model.Reasoning), "MODEL_IMAGES": fmt.Sprint(r.Model.Images),
	}
	if r.Provider.AccessMode == llm.ModeDirect {
		env["MODEL_BASE_URL"], env["MODEL_API_KEY"] = r.Provider.BaseURL, r.APIKey
		if len(r.Headers) > 0 {
			b, err := json.Marshal(r.Headers)
			if err != nil {
				return nil, err
			}
			env["MODEL_HEADERS"] = string(b)
		}
		reportUsage(env)
	} else {
		env["MODEL_BASE_URL"] = llm.ProxyBaseURL(s.cfg.GatewayURL, r.Provider.ID)
		env["MODEL_API_KEY"] = llm.ProxyCredential(ag.ID.String(), token)
	}
	return env, nil
}

// AuthenticateProxy checks an agent's model-proxy credential (its runtime
// token) and returns its team and model. Turned-off agents are refused.
func (s *Service) AuthenticateProxy(ctx context.Context, agentID uuid.UUID, token string) (uuid.UUID, uuid.UUID, error) {
	ag, err := s.load(ctx, s.q, agentID)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	want, err := s.box.Open(ag.RuntimeTokenEnc, []byte(ag.ID.String()))
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if subtle.ConstantTimeCompare(want, []byte(token)) != 1 {
		return uuid.Nil, uuid.Nil, apperr.New(401, "unauthorized", "Invalid credential")
	}
	if !ag.Enabled {
		return uuid.Nil, uuid.Nil, errDisabled
	}
	tv, err := version(ctx, s.q, ag.TemplateID, ag.TemplateVersion)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	modelID, _ := effectiveModel(ag, tv)
	return ag.TeamID, modelID, nil
}

// mcpEnv adds the agent's MCP servers to its runtime's environment: the
// template version's (MCP_SERVERS, JSON), and where to fetch the servers apps
// give its threads (MCP_THREADS_URL), with its gateway credential (MCP_KEY).
func (s *Service) mcpEnv(ctx context.Context, ag dbgen.Agent, tv dbgen.AgentTemplateVersion, token string, env map[string]string) error {
	info := mcp.Agent{ID: ag.ID, TeamID: ag.TeamID, TemplateID: ag.TemplateID, Version: ag.TemplateVersion, ClientMCP: tv.ClientMcp,
		Credential: llm.ProxyCredential(ag.ID.String(), token)}
	servers, err := s.mcp.TemplateServers(ctx, info, s.cfg.GatewayURL)
	if err != nil {
		return err
	}
	if len(servers) > 0 {
		b, err := json.Marshal(servers)
		if err != nil {
			return err
		}
		env["MCP_SERVERS"] = string(b)
	}
	if tv.ClientMcp && len(s.mcp.Client.Hosts) > 0 {
		env["MCP_THREADS_URL"] = strings.TrimRight(s.cfg.GatewayURL, "/") + "/thread-mcp"
		env["MCP_KEY"] = info.Credential
	}
	return nil
}

// AuthenticateMCP checks an agent's gateway credential for the MCP routes.
func (s *Service) AuthenticateMCP(ctx context.Context, agentID uuid.UUID, token string) (mcp.Agent, error) {
	ag, err := s.load(ctx, s.q, agentID)
	if err != nil {
		return mcp.Agent{}, err
	}
	want, err := s.box.Open(ag.RuntimeTokenEnc, []byte(ag.ID.String()))
	if err != nil {
		return mcp.Agent{}, err
	}
	if subtle.ConstantTimeCompare(want, []byte(token)) != 1 {
		return mcp.Agent{}, apperr.New(401, "unauthorized", "Invalid credential")
	}
	if !ag.Enabled {
		return mcp.Agent{}, errDisabled
	}
	tv, err := version(ctx, s.q, ag.TemplateID, ag.TemplateVersion)
	if err != nil {
		return mcp.Agent{}, err
	}
	return mcp.Agent{ID: ag.ID, TeamID: ag.TeamID, TemplateID: ag.TemplateID, Version: ag.TemplateVersion, ClientMCP: tv.ClientMcp,
		Credential: llm.ProxyCredential(ag.ID.String(), token)}, nil
}

// SetThreadMCP replaces the MCP servers an app gives one of the agent's
// threads: anyone who may use the agent and reach the thread, with a
// template version that allows it.
//
// Servers belong to a thread whose creator is known (thread_mcp_servers
// reference agent_threads). Setting servers on a thread the platform has no
// record of therefore starts it: the caller becomes its creator, with the
// visibility given ("" is shared), as its first message would. The thread
// is reserved until a message is accepted (listed for the creator only).
// Another person's private thread is not found, and a visibility that
// differs from an existing thread's is refused (visibility_fixed). An empty
// list on an unknown thread starts nothing.
//
// A reserved thread (no message yet) is its creator's: servers can't be set on
// it by anyone else (not found, as for a private thread), or they would become
// active for the creator's first message without the creator knowing. And the
// first message from someone other than the creator drops the servers the
// creator set (takeOverReserved).
func (s *Service) SetThreadMCP(ctx context.Context, a authz.Actor, agentID uuid.UUID, threadID, visibility string, servers []mcp.ClientServer) error {
	ag, err := s.load(ctx, s.q, agentID)
	if err != nil {
		return err
	}
	sub, err := subject(ctx, s.q, a, ag)
	if err != nil {
		return err
	}
	if err := authz.CheckAgentUse(sub); err != nil {
		return err
	}
	if err := checkVisibility(visibility); err != nil {
		return err
	}
	tv, err := version(ctx, s.q, ag.TemplateID, ag.TemplateVersion)
	if err != nil {
		return err
	}
	// Refuse bad servers before a thread is started for them.
	if err := s.mcp.CheckThreadServers(tv.ClientMcp, servers); err != nil {
		return err
	}
	if len(servers) == 0 {
		t, found, err := s.thread(ctx, s.q, a, agentID, threadID)
		if err != nil {
			return err
		}
		if found && t.Reserved && !owns(a, t) {
			return errThreadNotFound
		}
		return s.mcp.SetThreadServers(ctx, agentID, threadID, tv.ClientMcp, nil, stillIn(agentID, ag.TeamID))
	}
	t, created, err := s.claim(ctx, a, agentID, threadID, visibility, DefaultTitle, true, time.Now())
	if err != nil {
		return err
	}
	if !created && t.Reserved && !owns(a, t) {
		return errThreadNotFound
	}
	if err := s.mcp.SetThreadServers(ctx, agentID, threadID, tv.ClientMcp, servers, stillIn(agentID, ag.TeamID)); err != nil {
		s.Unclaim(ctx, Claim{Thread: t, Created: created})
		return err
	}
	if created {
		s.announce(ctx, t)
	}
	return nil
}

// stillIn re-checks, in the transaction that writes a thread's MCP servers,
// that the agent is still in the team the caller was authorized for (not
// found otherwise), and locks its row until the write commits. Without it a
// write authorized before a move could land after the move removed the
// thread servers, and the old team's caller's credentials would reach the new
// team's people: the move locks the agent, so it waits for this write (and
// removes its servers), or this write waits for the move and is refused.
func stillIn(agentID, teamID uuid.UUID) func(context.Context, *dbgen.Queries) error {
	return func(ctx context.Context, q *dbgen.Queries) error {
		team, err := q.ShareLockAgentTeam(ctx, agentID)
		if errors.Is(store.NotFound(err), store.ErrNotFound) || (err == nil && team != teamID) {
			return errNotFound
		}
		return err
	}
}

// ThreadMCP lists the MCP servers apps gave a thread (names and URLs only).
func (s *Service) ThreadMCP(ctx context.Context, a authz.Actor, agentID uuid.UUID, threadID string) ([]dbgen.ThreadMcpServer, error) {
	ag, err := s.load(ctx, s.q, agentID)
	if err != nil {
		return nil, err
	}
	sub, err := subject(ctx, s.q, a, ag)
	if err != nil {
		return nil, err
	}
	if err := authz.CheckAgentUse(sub); err != nil {
		return nil, err
	}
	if err := s.ThreadAccess(ctx, a, agentID, threadID); err != nil {
		return nil, err
	}
	return s.mcp.ThreadServers(ctx, agentID, threadID)
}

// checkRunning refuses to wake an agent of a workspace already running as
// many agents as its limit allows (personal workspaces). Running agents
// carry on; the limit applies to waking another.
func (s *Service) checkRunning(ctx context.Context, ag dbgen.Agent) error {
	t, err := s.q.GetTeamByID(ctx, ag.TeamID)
	if err != nil {
		return err
	}
	limits, err := teams.LimitsOf(ctx, s.q, t)
	if err != nil || limits.MaxRunning == nil {
		return err
	}
	running := s.host.Launcher().Running()
	if _, ok := running[ag.ID.String()]; ok {
		return nil
	}
	ids, err := s.q.TeamAgentIDs(ctx, ag.TeamID)
	if err != nil {
		return err
	}
	n := int32(0)
	for _, id := range ids {
		if _, ok := running[id.String()]; ok {
			n++
		}
	}
	if n >= *limits.MaxRunning {
		return apperr.Conflict("too_many_running", fmt.Sprintf(
			"This workspace may run at most %d agents at once: hibernate one, or wait until one goes idle", *limits.MaxRunning))
	}
	return nil
}
