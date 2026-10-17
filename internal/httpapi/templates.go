package httpapi

import (
	"net/http"

	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Agent templates (api/openapi.yaml, tag agents): the catalog, team
// templates, versions and publishing. internal/agents authorizes every call.

func (a *api) templateRoutes() []route {
	const one = "/v1/agent-templates/{templateId}"
	k := a.sessionOrKey
	return []route{
		{"GET", "/v1/agent-templates", k(a.listAgentTemplates)},
		{"POST", "/v1/agent-templates", a.session(a.createPlatformTemplate)},
		{"GET", "/v1/teams/{team}/agent-templates", k(a.listTeamTemplates)},
		{"POST", "/v1/teams/{team}/agent-templates", k(a.createTeamTemplate)},
		{"GET", one, k(a.getAgentTemplate)},
		{"PATCH", one, k(a.updateAgentTemplate)},
		{"GET", one + "/versions", k(a.listTemplateVersions)},
		{"POST", one + "/versions", k(a.createTemplateVersion)},
		{"POST", one + "/publish", a.session(a.publishTemplate)},
		{"POST", one + "/unpublish", a.session(a.unpublishTemplate)},
		{"POST", one + "/review", a.session(a.reviewTemplate)},
		{"GET", "/v1/admin/agent-templates", a.admin(a.adminListTemplates)},
	}
}

func toAPITemplate(v agents.TemplateView) apitypes.AgentTemplate {
	t := v.Template
	out := apitypes.AgentTemplate{
		Id: t.ID, Slug: t.Slug, Name: t.Name, Description: t.Description, TeamSlug: v.TeamSlug, TeamName: v.TeamName,
		Status: apitypes.AgentTemplateStatus(t.Status), Archived: t.Archived, CurrentVersion: t.CurrentVersion,
		Revision: t.Revision, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
	if t.TeamID.Valid {
		id := t.TeamID.UUID
		out.TeamId = &id
	}
	if t.ReviewNote != "" {
		out.ReviewNote = &t.ReviewNote
	}
	out.AgentCount = v.AgentCount
	return out
}

func toAPITemplateVersion(v dbgen.ListTemplateVersionsRow) apitypes.AgentTemplateVersion {
	return apitypes.AgentTemplateVersion{Version: v.Version, Instructions: v.Instructions, Notes: v.Notes, CreatedByName: v.CreatedByName, CreatedAt: v.CreatedAt}
}

func (a *api) writeTemplate(w http.ResponseWriter, r *http.Request, status int, v agents.TemplateView, err error) {
	if failed(w, r, err) {
		return
	}
	writeRevised(w, status, v.Template.Revision, toAPITemplate(v))
}

// listAgentTemplates reads ?team= (also that team's own templates).
func (a *api) listAgentTemplates(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Agents.Catalog(r.Context(), a.actor(r), r.URL.Query().Get("team"))
	writeList(w, r, rows, err, toAPITemplate)
}

func (a *api) listTeamTemplates(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Agents.TeamTemplates(r.Context(), a.actor(r), r.PathValue("team"))
	writeList(w, r, rows, err, toAPITemplate)
}

// adminListTemplates reads ?status= (pending: the review queue).
func (a *api) adminListTemplates(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Agents.AdminTemplates(r.Context(), a.actor(r), r.URL.Query().Get("status"))
	writeList(w, r, rows, err, toAPITemplate)
}

func decodeTemplateCreate(w http.ResponseWriter, r *http.Request) (agents.TemplateInput, bool) {
	var in apitypes.AgentTemplateCreate
	if !httpx.Decode(w, r, &in) {
		return agents.TemplateInput{}, false
	}
	ti := agents.TemplateInput{Slug: in.Slug, Name: in.Name, Instructions: in.Instructions}
	setIf(&ti.Description, in.Description)
	setIf(&ti.Notes, in.Notes)
	return ti, true
}

func (a *api) createPlatformTemplate(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeTemplateCreate(w, r)
	if !ok {
		return
	}
	v, err := a.Agents.CreateTemplate(r.Context(), a.actor(r), "", in)
	a.writeTemplate(w, r, http.StatusCreated, v, err)
}

func (a *api) createTeamTemplate(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeTemplateCreate(w, r)
	if !ok {
		return
	}
	v, err := a.Agents.CreateTemplate(r.Context(), a.actor(r), r.PathValue("team"), in)
	a.writeTemplate(w, r, http.StatusCreated, v, err)
}

func (a *api) getAgentTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "templateId")
	if !ok {
		return
	}
	v, err := a.Agents.Template(r.Context(), a.actor(r), id)
	a.writeTemplate(w, r, http.StatusOK, v, err)
}

func (a *api) updateAgentTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "templateId")
	if !ok {
		return
	}
	in, rev, ok := decodeRevised[apitypes.AgentTemplateUpdate](w, r)
	if !ok {
		return
	}
	v, err := a.Agents.UpdateTemplate(r.Context(), a.actor(r), id, agents.TemplateUpdate{Name: in.Name, Description: in.Description, Archived: in.Archived}, rev)
	a.writeTemplate(w, r, http.StatusOK, v, err)
}

func (a *api) listTemplateVersions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "templateId")
	if !ok {
		return
	}
	rows, err := a.Agents.Versions(r.Context(), a.actor(r), id)
	writeList(w, r, rows, err, toAPITemplateVersion)
}

func (a *api) createTemplateVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "templateId")
	if !ok {
		return
	}
	var in apitypes.AgentTemplateVersionCreate
	if !httpx.Decode(w, r, &in) {
		return
	}
	notes := ""
	setIf(&notes, in.Notes)
	v, err := a.Agents.AddVersion(r.Context(), a.actor(r), id, in.Instructions, notes)
	a.writeTemplate(w, r, http.StatusCreated, v, err)
}

func (a *api) publishTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "templateId")
	if !ok {
		return
	}
	v, err := a.Agents.RequestPublish(r.Context(), a.actor(r), id)
	a.writeTemplate(w, r, http.StatusOK, v, err)
}

func (a *api) unpublishTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "templateId")
	if !ok {
		return
	}
	v, err := a.Agents.Unpublish(r.Context(), a.actor(r), id)
	a.writeTemplate(w, r, http.StatusOK, v, err)
}

func (a *api) reviewTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "templateId")
	if !ok {
		return
	}
	var in apitypes.AgentTemplateReview
	if !httpx.Decode(w, r, &in) {
		return
	}
	note := ""
	setIf(&note, in.Note)
	v, err := a.Agents.Review(r.Context(), a.actor(r), id, in.Decision == apitypes.Approve, note)
	a.writeTemplate(w, r, http.StatusOK, v, err)
}
