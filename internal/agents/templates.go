package agents

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/llm"
	"github.com/ncecere/pi-durable-platform/internal/mcp"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

// Templates: platform templates curated by platform admins, team templates
// private to their team until a platform admin approves publishing them to
// the catalog, and immutable versions agents pin (authz/templates.go).

var (
	errVersionNotFound = apperr.NotFound("version_not_found", "Template version not found")
	errSlugTaken       = apperr.Conflict("slug_taken", "A template with that slug already exists here")
	errBadSlug         = apperr.Invalid("invalid_slug", "Slugs are 1-64 lower-case letters, digits or dashes, not starting or ending with a dash")
	errBadInstructions = apperr.Invalid("invalid_instructions", "Instructions must be 1-20000 characters")
	errBadNotes        = apperr.Invalid("invalid_notes", "Notes must be at most 2000 characters")
	errNotPending      = apperr.Conflict("not_pending", "This template has no open request to publish it")
	errNotPrivate      = apperr.Conflict("not_private", "Only a private template can be submitted for the catalog")
	slugRE             = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)
)

// A team's own models are the team's only: never in platform templates or
// the catalog.
var (
	errPlatformTeamModel = apperr.Conflict("team_model", "Platform templates run on platform models, not on a team's own models")
	errCatalogTeamModel  = apperr.Conflict("team_model",
		"This template runs on one of the team's own models, which other teams can't use: add a version on a platform model before it goes to the catalog")
)

// TemplateView is a template with its owning team and how many agents use it.
type TemplateView struct {
	Template dbgen.AgentTemplate
	TeamSlug *string
	TeamName *string
	// AgentCount is how many agents use it (one template's view only).
	AgentCount *int64
}

func templateSubject(ctx context.Context, q *dbgen.Queries, a authz.Actor, t dbgen.AgentTemplate) (authz.TemplateSubject, error) {
	sub := authz.TemplateSubject{
		Platform: !t.TeamID.Valid, Status: t.Status, Archived: t.Archived,
		IsService: a.Service != nil, IsPlatformAdmin: a.IsPlatformAdmin(), IsStaff: a.CanReadPlatform(),
	}
	if t.TeamID.Valid {
		role, err := roleIn(ctx, q, a, t.TeamID.UUID)
		if err != nil {
			return sub, err
		}
		sub.Role = role
	}
	return sub, nil
}

func (s *Service) templateView(ctx context.Context, q *dbgen.Queries, id uuid.UUID) (TemplateView, error) {
	r, err := q.GetTemplateView(ctx, id)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return TemplateView{}, errTemplateNotFound
	} else if err != nil {
		return TemplateView{}, err
	}
	return TemplateView{Template: dbgen.AgentTemplate{ID: r.ID, Slug: r.Slug, Name: r.Name, Description: r.Description,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, TeamID: r.TeamID, Status: r.Status, ReviewNote: r.ReviewNote,
		Archived: r.Archived, CurrentVersion: r.CurrentVersion, Revision: r.Revision, CreatedBy: r.CreatedBy},
		TeamSlug: r.TeamSlug, TeamName: r.TeamName, AgentCount: &r.AgentCount}, nil
}

func (s *Service) lockTemplate(ctx context.Context, q *dbgen.Queries, id uuid.UUID) (dbgen.AgentTemplate, error) {
	t, err := q.LockTemplate(ctx, id)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return t, errTemplateNotFound
	}
	return t, err
}

type templateRow interface {
	dbgen.ListDeployableTemplatesRow | dbgen.ListTeamTemplatesRow | dbgen.ListTemplatesAdminRow
}

func templateViews[R templateRow](rows []R) []TemplateView {
	out := make([]TemplateView, len(rows))
	for i, r := range rows {
		switch v := any(r).(type) {
		case dbgen.ListDeployableTemplatesRow:
			out[i] = TemplateView{Template: v.AgentTemplate, TeamSlug: v.TeamSlug, TeamName: v.TeamName}
		case dbgen.ListTeamTemplatesRow:
			out[i] = TemplateView{Template: v.AgentTemplate, TeamSlug: &v.TeamSlug, TeamName: &v.TeamName}
		case dbgen.ListTemplatesAdminRow:
			out[i] = TemplateView{Template: v.AgentTemplate, TeamSlug: v.TeamSlug, TeamName: v.TeamName}
		}
	}
	return out
}

// Catalog lists what the actor's team can deploy: the catalog, plus the
// team's own templates when teamRef names a team the actor is on.
func (s *Service) Catalog(ctx context.Context, a authz.Actor, teamRef string) ([]TemplateView, error) {
	team := uuid.NullUUID{}
	if teamRef != "" {
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
		team = uuid.NullUUID{UUID: t.ID, Valid: true}
	}
	rows, err := s.q.ListDeployableTemplates(ctx, team)
	return templateViews(rows), err
}

// TeamTemplates lists a team's own templates, archived ones too (its members
// and platform staff).
func (s *Service) TeamTemplates(ctx context.Context, a authz.Actor, teamRef string) ([]TemplateView, error) {
	t, err := teams.Resolve(ctx, s.q, teamRef)
	if err != nil {
		return nil, err
	}
	role, err := roleIn(ctx, s.q, a, t.ID)
	if err != nil {
		return nil, err
	}
	if role == "" && !a.CanReadPlatform() {
		return nil, errTeamNotFound
	}
	rows, err := s.q.ListTeamTemplates(ctx, uuid.NullUUID{UUID: t.ID, Valid: true})
	return templateViews(rows), err
}

// AdminTemplates lists every template, or those with a status (platform staff).
func (s *Service) AdminTemplates(ctx context.Context, a authz.Actor, status string) ([]TemplateView, error) {
	if !a.CanReadPlatform() {
		return nil, apperr.Forbidden("Only platform admins and auditors can see every template")
	}
	var st *string
	if status != "" {
		st = &status
	}
	rows, err := s.q.ListTemplatesAdmin(ctx, st)
	return templateViews(rows), err
}

// Template returns one template the actor may see.
func (s *Service) Template(ctx context.Context, a authz.Actor, id uuid.UUID) (TemplateView, error) {
	v, err := s.templateView(ctx, s.q, id)
	if err != nil {
		return v, err
	}
	sub, err := templateSubject(ctx, s.q, a, v.Template)
	if err != nil {
		return v, err
	}
	return v, authz.CheckTemplateSee(sub)
}

// VersionView is a template version with its MCP servers.
type VersionView struct {
	Version dbgen.ListTemplateVersionsRow
	MCP     []dbgen.ListTemplateVersionMCPRow
}

// Versions lists a template's versions, newest first.
func (s *Service) Versions(ctx context.Context, a authz.Actor, id uuid.UUID) ([]VersionView, error) {
	if _, err := s.Template(ctx, a, id); err != nil {
		return nil, err
	}
	rows, err := s.q.ListTemplateVersions(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]VersionView, len(rows))
	for i, r := range rows {
		out[i].Version = r
		if out[i].MCP, err = s.q.ListTemplateVersionMCP(ctx, dbgen.ListTemplateVersionMCPParams{TemplateID: id, Version: r.Version}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// TemplateInput is a new template and its first version. ModelID is the
// version's model (nil: the platform's default).
type TemplateInput struct {
	Slug, Name, Description, Instructions, Notes string
	ModelID                                      *uuid.UUID
	// MCP are the MCP servers its agents get; ClientMCP lets apps give them
	// their own for a thread.
	MCP       []mcp.Attach
	ClientMCP bool
}

// checkTemplateModel checks a template version's model: a team template's
// must be usable by the team; a platform template's must be enabled and a
// platform model (each deploying team is checked when it deploys).
func checkTemplateModel(ctx context.Context, q *dbgen.Queries, teamID uuid.NullUUID, modelID uuid.NullUUID) error {
	if !modelID.Valid {
		return nil
	}
	if teamID.Valid {
		return llm.CheckUsable(ctx, q, modelID.UUID, teamID.UUID)
	}
	if err := llm.CheckEnabled(ctx, q, modelID.UUID); errors.Is(err, llm.ErrTeamModel) {
		return errPlatformTeamModel
	} else if err != nil {
		return err
	}
	return nil
}

// checkCatalogModel refuses a team model in a template that is, or is asked
// to be, in the catalog (other teams can't use it).
func checkCatalogModel(ctx context.Context, q *dbgen.Queries, modelID uuid.NullUUID) error {
	if !modelID.Valid {
		return nil
	}
	team, err := llm.IsTeamModel(ctx, q, modelID.UUID)
	if err != nil {
		return err
	}
	if team {
		return errCatalogTeamModel
	}
	return nil
}

// checkCatalogVersion is checkCatalogModel for a template's current version.
func checkCatalogVersion(ctx context.Context, q *dbgen.Queries, t dbgen.AgentTemplate) error {
	cur, err := version(ctx, q, t.ID, t.CurrentVersion)
	if err != nil {
		return err
	}
	return checkCatalogModel(ctx, q, cur.ModelID)
}

func nullID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

func (in *TemplateInput) check() error {
	in.Slug, in.Name = strings.TrimSpace(in.Slug), strings.TrimSpace(in.Name)
	if !slugRE.MatchString(in.Slug) {
		return errBadSlug
	}
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > 100 || strings.ContainsAny(in.Name, "\r\n\x00") {
		return errBadName
	}
	if utf8.RuneCountInString(in.Description) > 2000 {
		return errBadDesc
	}
	return checkVersion(in.Instructions, in.Notes)
}

func checkVersion(instructions, notes string) error {
	if n := utf8.RuneCountInString(strings.TrimSpace(instructions)); n < 1 || n > 20000 {
		return errBadInstructions
	}
	if utf8.RuneCountInString(notes) > 2000 {
		return errBadNotes
	}
	return nil
}

// CreateTemplate makes a template with its first version: a platform
// template (teamRef "", platform admins) or a team's private template (its
// editors and above).
func (s *Service) CreateTemplate(ctx context.Context, a authz.Actor, teamRef string, in TemplateInput) (TemplateView, error) {
	if err := in.check(); err != nil {
		return TemplateView{}, err
	}
	var out TemplateView
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		p := dbgen.InsertTemplateParams{Slug: in.Slug, Name: in.Name, Description: in.Description, Status: authz.TemplatePublished,
			CreatedBy: uuid.NullUUID{UUID: a.UserID, Valid: a.UserID != uuid.Nil && a.Service == nil}}
		e := a.Audit("agent_template.create", "agent_template", "")
		if teamRef == "" {
			if !a.IsPlatformAdmin() {
				return apperr.Forbidden("Only platform admins can create platform templates")
			}
		} else {
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
			if !authz.RoleAtLeast(role, authz.RoleEditor) {
				return apperr.Forbidden("Only the team's editors, admins and owners can create templates")
			}
			if t.Status != teams.StatusActive {
				return errTeamArchived
			}
			if t.Kind == teams.KindPersonal {
				return teams.ErrPersonal // catalog templates only, for now
			}
			p.TeamID, p.Status, e.TeamID = uuid.NullUUID{UUID: t.ID, Valid: true}, authz.TemplatePrivate, t.ID
		}
		tpl, err := q.InsertTemplate(ctx, p)
		if apperr.IsUniqueViolation(err, "agent_templates_platform_slug") || apperr.IsUniqueViolation(err, "agent_templates_team_slug") {
			return errSlugTaken
		} else if err != nil {
			return err
		}
		if err := checkTemplateModel(ctx, q, tpl.TeamID, nullID(in.ModelID)); err != nil {
			return err
		}
		if err := mcp.CheckAttach(ctx, q, tpl.TeamID, in.MCP); err != nil {
			return err
		}
		if _, err := q.InsertTemplateVersion(ctx, dbgen.InsertTemplateVersionParams{TemplateID: tpl.ID, Version: 1,
			Instructions: strings.TrimSpace(in.Instructions), Notes: in.Notes, CreatedBy: p.CreatedBy, ModelID: nullID(in.ModelID),
			ClientMcp: in.ClientMCP}); err != nil {
			return err
		}
		if err := mcp.SaveAttach(ctx, q, tpl.ID, 1, in.MCP); err != nil {
			return err
		}
		e.TargetID, e.After = tpl.ID.String(), templateSnapshot(tpl)
		if err := audit.Record(ctx, q, e); err != nil {
			return err
		}
		out, err = s.templateView(ctx, q, tpl.ID)
		return err
	})
	return out, err
}

func templateSnapshot(t dbgen.AgentTemplate) map[string]any {
	return map[string]any{"slug": t.Slug, "name": t.Name, "description": t.Description, "status": t.Status,
		"archived": t.Archived, "currentVersion": t.CurrentVersion}
}

// TemplateUpdate holds optional changes.
type TemplateUpdate struct {
	Name, Description *string
	Archived          *bool
}

// UpdateTemplate renames, describes or archives a template (If-Match).
func (s *Service) UpdateTemplate(ctx context.Context, a authz.Actor, id uuid.UUID, in TemplateUpdate, expectedRevision int64) (TemplateView, error) {
	return s.changeTemplate(ctx, a, id, expectedRevision, "agent_template.update", authz.CheckTemplateEdit,
		func(q *dbgen.Queries, t dbgen.AgentTemplate) (dbgen.AgentTemplate, error) {
			p := dbgen.UpdateTemplateParams{ID: id, Name: t.Name, Description: t.Description, Archived: t.Archived}
			if in.Name != nil {
				p.Name = strings.TrimSpace(*in.Name)
				if n := utf8.RuneCountInString(p.Name); n < 1 || n > 100 || strings.ContainsAny(p.Name, "\r\n\x00") {
					return t, errBadName
				}
			}
			if in.Description != nil {
				if utf8.RuneCountInString(*in.Description) > 2000 {
					return t, errBadDesc
				}
				p.Description = *in.Description
			}
			if in.Archived != nil {
				p.Archived = *in.Archived
			}
			return q.UpdateTemplate(ctx, p)
		})
}

// VersionInput is a new template version. ModelID: nil keeps the current
// version's model, "" the platform's default, or a model's ID.
type VersionInput struct {
	Instructions, Notes string
	ModelID             *string
	// MCP and ClientMCP: nil keeps the current version's.
	MCP       *[]mcp.Attach
	ClientMCP *bool
}

// AddVersion adds a version, which becomes the one new agents get. Existing
// agents keep theirs until they are upgraded.
func (s *Service) AddVersion(ctx context.Context, a authz.Actor, id uuid.UUID, in VersionInput) (TemplateView, error) {
	if err := checkVersion(in.Instructions, in.Notes); err != nil {
		return TemplateView{}, err
	}
	var model *uuid.UUID
	if in.ModelID != nil && *in.ModelID != "" {
		mid, err := uuid.Parse(*in.ModelID)
		if err != nil {
			return TemplateView{}, errBadModelID
		}
		model = &mid
	}
	return s.changeTemplate(ctx, a, id, -1, "agent_template.version", authz.CheckTemplateEdit,
		func(q *dbgen.Queries, t dbgen.AgentTemplate) (dbgen.AgentTemplate, error) {
			cur, err := version(ctx, q, id, t.CurrentVersion)
			if err != nil {
				return t, err
			}
			modelID := nullID(model)
			if in.ModelID == nil {
				modelID = cur.ModelID
			} else if err := checkTemplateModel(ctx, q, t.TeamID, modelID); err != nil {
				return t, err
			}
			if t.TeamID.Valid && t.Status != authz.TemplatePrivate {
				if err := checkCatalogModel(ctx, q, modelID); err != nil {
					return t, err
				}
			}
			clientMCP := cur.ClientMcp
			if in.ClientMCP != nil {
				clientMCP = *in.ClientMCP
			}
			var attach []mcp.Attach
			if in.MCP == nil {
				rows, err := q.ListTemplateVersionMCP(ctx, dbgen.ListTemplateVersionMCPParams{TemplateID: id, Version: t.CurrentVersion})
				if err != nil {
					return t, err
				}
				for _, r := range rows {
					attach = append(attach, mcp.Attach{ServerID: r.ServerID, Tools: r.Tools})
				}
			} else {
				attach = *in.MCP
				if err := mcp.CheckAttach(ctx, q, t.TeamID, attach); err != nil {
					return t, err
				}
			}
			bumped, err := q.BumpTemplateVersion(ctx, id)
			if err != nil {
				return t, err
			}
			if _, err = q.InsertTemplateVersion(ctx, dbgen.InsertTemplateVersionParams{TemplateID: id, Version: bumped.CurrentVersion,
				Instructions: strings.TrimSpace(in.Instructions), Notes: in.Notes, ModelID: modelID, ClientMcp: clientMCP,
				CreatedBy: uuid.NullUUID{UUID: a.UserID, Valid: a.UserID != uuid.Nil && a.Service == nil}}); err != nil {
				return t, err
			}
			return bumped, mcp.SaveAttach(ctx, q, id, bumped.CurrentVersion, attach)
		})
}

// RequestPublish asks a platform admin to put a private team template in the
// catalog (the team's owners and admins); refused while its current
// version runs on one of the team's own models (team_model).
func (s *Service) RequestPublish(ctx context.Context, a authz.Actor, id uuid.UUID) (TemplateView, error) {
	return s.changeTemplate(ctx, a, id, -1, "agent_template.publish_request", authz.CheckTemplatePublish,
		func(q *dbgen.Queries, t dbgen.AgentTemplate) (dbgen.AgentTemplate, error) {
			if t.Status != authz.TemplatePrivate {
				return t, errNotPrivate
			}
			if err := checkCatalogVersion(ctx, q, t); err != nil {
				return t, err
			}
			return q.SetTemplateStatus(ctx, dbgen.SetTemplateStatusParams{ID: id, Status: authz.TemplatePending})
		})
}

// Unpublish takes a team template out of the catalog, or withdraws its
// request (the team's owners and admins). Agents already deployed from it
// keep working.
func (s *Service) Unpublish(ctx context.Context, a authz.Actor, id uuid.UUID) (TemplateView, error) {
	return s.changeTemplate(ctx, a, id, -1, "agent_template.unpublish", authz.CheckTemplatePublish,
		func(q *dbgen.Queries, t dbgen.AgentTemplate) (dbgen.AgentTemplate, error) {
			return q.SetTemplateStatus(ctx, dbgen.SetTemplateStatusParams{ID: id, Status: authz.TemplatePrivate})
		})
}

// Review approves (published) or rejects (private, with the note) a request
// to publish a team template (platform admins).
func (s *Service) Review(ctx context.Context, a authz.Actor, id uuid.UUID, approve bool, note string) (TemplateView, error) {
	if utf8.RuneCountInString(note) > 2000 {
		return TemplateView{}, errBadNotes
	}
	action := "agent_template.reject"
	if approve {
		action = "agent_template.approve"
	}
	return s.changeTemplate(ctx, a, id, -1, action, authz.CheckTemplateReview,
		func(q *dbgen.Queries, t dbgen.AgentTemplate) (dbgen.AgentTemplate, error) {
			if t.Status != authz.TemplatePending {
				return t, errNotPending
			}
			status := authz.TemplatePrivate
			if approve {
				if err := checkCatalogVersion(ctx, q, t); err != nil {
					return t, err
				}
				status, note = authz.TemplatePublished, ""
			}
			return q.SetTemplateStatus(ctx, dbgen.SetTemplateStatusParams{ID: id, Status: status, ReviewNote: note})
		})
}

// changeTemplate locks a template, checks the rule (and the revision when
// expectedRevision >= 0), applies the change and audits it.
func (s *Service) changeTemplate(ctx context.Context, a authz.Actor, id uuid.UUID, expectedRevision int64, action string,
	rule func(authz.TemplateSubject) error, change func(*dbgen.Queries, dbgen.AgentTemplate) (dbgen.AgentTemplate, error)) (TemplateView, error) {
	var out TemplateView
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		t, err := s.lockTemplate(ctx, q, id)
		if err != nil {
			return err
		}
		sub, err := templateSubject(ctx, q, a, t)
		if err != nil {
			return err
		}
		if err := rule(sub); err != nil {
			return err
		}
		if expectedRevision >= 0 && t.Revision != expectedRevision {
			return apperr.Stale()
		}
		updated, err := change(q, t)
		if err != nil {
			return err
		}
		e := a.Audit(action, "agent_template", id.String())
		e.Before, e.After = templateSnapshot(t), templateSnapshot(updated)
		if t.TeamID.Valid {
			e.TeamID = t.TeamID.UUID
		}
		if err := audit.Record(ctx, q, e); err != nil {
			return err
		}
		out, err = s.templateView(ctx, q, id)
		return err
	})
	return out, err
}

// deployable resolves the template (by ID, or a platform template by slug,
// default the assistant) and checks the team may deploy it.
func deployable(ctx context.Context, q *dbgen.Queries, teamID uuid.UUID, templateID *uuid.UUID, slug string) (dbgen.AgentTemplate, error) {
	var t dbgen.AgentTemplate
	var err error
	if templateID != nil {
		t, err = q.GetAgentTemplate(ctx, *templateID)
	} else {
		if slug == "" {
			slug = DefaultTemplate
		}
		t, err = q.GetPlatformTemplateBySlug(ctx, slug)
	}
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return t, errTemplateNotFound
	} else if err != nil {
		return t, err
	}
	own := t.TeamID.Valid && t.TeamID.UUID == teamID
	return t, authz.CheckTemplateDeploy(authz.TemplateSubject{Platform: !t.TeamID.Valid, Status: t.Status, Archived: t.Archived}, own)
}

// version returns one version of a template.
func version(ctx context.Context, q *dbgen.Queries, templateID uuid.UUID, v int32) (dbgen.AgentTemplateVersion, error) {
	tv, err := q.GetTemplateVersion(ctx, dbgen.GetTemplateVersionParams{TemplateID: templateID, Version: v})
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return tv, errVersionNotFound
	}
	return tv, err
}
