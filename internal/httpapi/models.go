package httpapi

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/llm"
)

// The model catalog (api/openapi.yaml, tag models): providers and models,
// managed by platform admins; the models a team may use. internal/llm
// authorizes every call. Provider keys and header values are write-only.

func (a *api) modelRoutes() []route {
	const provider = "/v1/admin/model-providers/{providerId}"
	return []route{
		{"GET", "/v1/admin/model-provider-kinds", a.admin(a.listProviderKinds)},
		{"GET", "/v1/admin/model-providers", a.admin(a.listModelProviders)},
		{"POST", "/v1/admin/model-providers", a.admin(a.createModelProvider)},
		{"GET", provider, a.admin(a.getModelProvider)},
		{"PATCH", provider, a.admin(a.updateModelProvider)},
		{"DELETE", provider, a.admin(a.deleteModelProvider)},
		{"POST", provider + "/discover", a.admin(a.discoverModels)},
		{"GET", provider + "/models", a.admin(a.listProviderModels)},
		{"POST", provider + "/models", a.admin(a.createModel)},
		{"GET", "/v1/admin/models", a.admin(a.listModels)},
		{"PATCH", "/v1/admin/models/{modelId}", a.admin(a.updateModel)},
		{"DELETE", "/v1/admin/models/{modelId}", a.admin(a.deleteModel)},
		{"GET", "/v1/teams/{team}/models", a.sessionOrKey(a.listTeamModels)},
		{"GET", "/v1/teams/{team}/usage", a.sessionOrKey(a.getTeamUsage)},
		{"GET", "/v1/admin/usage", a.admin(a.adminGetUsage)},
	}
}

func toAPIProvider(v llm.ProviderView) apitypes.ModelProvider {
	p := v.Provider
	return apitypes.ModelProvider{Id: p.ID, Name: p.Name, Kind: apitypes.ModelProviderKind(p.Kind), BaseUrl: p.BaseURL,
		AccessMode: apitypes.ModelProviderAccessMode(p.AccessMode), Enabled: p.Enabled, HasHeaders: v.HasHeaders,
		ModelCount: v.ModelCount, Revision: p.Revision, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

func toAPIModel(v llm.ModelView) apitypes.Model {
	m := v.Model
	out := apitypes.Model{Id: m.ID, ProviderId: m.ProviderID, ProviderName: v.ProviderName, ProviderKind: v.ProviderKind,
		UpstreamId: m.UpstreamID, Name: m.Name, ContextWindow: m.ContextWindow, MaxTokens: m.MaxTokens, Images: m.Images,
		Reasoning: m.Reasoning, Enabled: m.Enabled, Visibility: apitypes.ModelVisibility(m.Visibility), Revision: m.Revision,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
	if m.InputPrice.Valid {
		out.InputPrice = &m.InputPrice.Float64
	}
	if m.OutputPrice.Valid {
		out.OutputPrice = &m.OutputPrice.Float64
	}
	if v.ProviderAccessMode != "" {
		mode := apitypes.ModelAccessMode(v.ProviderAccessMode)
		out.AccessMode = &mode
		enabled := v.ProviderEnabled
		out.ProviderEnabled = &enabled
	}
	if m.Visibility == "teams" {
		teams := make([]apitypes.ModelTeam, len(v.Teams))
		for i, t := range v.Teams {
			teams[i] = apitypes.ModelTeam{Id: t.ID, Slug: t.Slug, Name: t.Name}
		}
		out.Teams = &teams
	}
	return out
}

func (a *api) listProviderKinds(w http.ResponseWriter, r *http.Request) {
	out := make([]apitypes.ProviderType, len(llm.Kinds))
	for i, k := range llm.Kinds {
		out[i] = apitypes.ProviderType{Id: apitypes.ProviderTypeId(k.ID), Name: k.Name, Api: k.API,
			AuthHeader: apitypes.ProviderTypeAuthHeader(k.AuthHeader)}
		if k.DefaultBaseURL != "" {
			u := k.DefaultBaseURL
			out[i].DefaultBaseUrl = &u
		}
	}
	writeList(w, r, out, nil, func(t apitypes.ProviderType) apitypes.ProviderType { return t })
}

func (a *api) listModelProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Models.Providers(r.Context(), a.actor(r))
	writeList(w, r, rows, err, toAPIProvider)
}

func (a *api) writeProvider(w http.ResponseWriter, r *http.Request, status int, v llm.ProviderView, err error) {
	if failed(w, r, err) {
		return
	}
	writeRevised(w, status, v.Provider.Revision, toAPIProvider(v))
}

func (a *api) createModelProvider(w http.ResponseWriter, r *http.Request) {
	var in apitypes.ModelProviderCreate
	if !httpx.Decode(w, r, &in) {
		return
	}
	pi := llm.ProviderInput{Name: in.Name, Kind: string(in.Kind), Enabled: in.Enabled}
	setIf(&pi.BaseURL, in.BaseUrl)
	setIf(&pi.APIKey, in.ApiKey)
	if in.AccessMode != nil {
		pi.AccessMode = string(*in.AccessMode)
	}
	if in.Headers != nil {
		pi.Headers = *in.Headers
	}
	v, err := a.Models.CreateProvider(r.Context(), a.actor(r), pi)
	a.writeProvider(w, r, http.StatusCreated, v, err)
}

func (a *api) getModelProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "providerId")
	if !ok {
		return
	}
	v, err := a.Models.Provider(r.Context(), a.actor(r), id)
	a.writeProvider(w, r, http.StatusOK, v, err)
}

func (a *api) updateModelProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "providerId")
	if !ok {
		return
	}
	in, rev, ok := decodeRevised[apitypes.ModelProviderUpdate](w, r)
	if !ok {
		return
	}
	pu := llm.ProviderUpdate{Name: in.Name, BaseURL: in.BaseUrl, APIKey: in.ApiKey, Headers: in.Headers, Enabled: in.Enabled}
	if in.AccessMode != nil {
		mode := string(*in.AccessMode)
		pu.AccessMode = &mode
	}
	v, err := a.Models.UpdateProvider(r.Context(), a.actor(r), id, pu, rev)
	a.writeProvider(w, r, http.StatusOK, v, err)
}

func (a *api) deleteModelProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "providerId")
	if !ok {
		return
	}
	writeOK(w, r, a.Models.DeleteProvider(r.Context(), a.actor(r), id))
}

func (a *api) discoverModels(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "providerId")
	if !ok {
		return
	}
	rows, err := a.Models.Discover(r.Context(), a.actor(r), id)
	writeList(w, r, rows, err, func(d llm.Discovered) apitypes.DiscoveredModel {
		return apitypes.DiscoveredModel{Id: d.ID, Name: d.Name, ContextWindow: d.ContextWindow, Added: d.Added}
	})
}

func (a *api) listProviderModels(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "providerId")
	if !ok {
		return
	}
	rows, err := a.Models.Models(r.Context(), a.actor(r), &id)
	writeList(w, r, rows, err, toAPIModel)
}

func (a *api) listModels(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Models.Models(r.Context(), a.actor(r), nil)
	writeList(w, r, rows, err, toAPIModel)
}

func (a *api) writeModel(w http.ResponseWriter, r *http.Request, status int, v llm.ModelView, err error) {
	if failed(w, r, err) {
		return
	}
	writeRevised(w, status, v.Model.Revision, toAPIModel(v))
}

func (a *api) createModel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "providerId")
	if !ok {
		return
	}
	var in apitypes.ModelCreate
	if !httpx.Decode(w, r, &in) {
		return
	}
	mi := llm.ModelInput{UpstreamID: in.UpstreamId, ContextWindow: in.ContextWindow, MaxTokens: in.MaxTokens,
		InputPrice: in.InputPrice, OutputPrice: in.OutputPrice, Enabled: in.Enabled}
	setIf(&mi.Name, in.Name)
	setIf(&mi.Images, in.Images)
	setIf(&mi.Reasoning, in.Reasoning)
	if in.Visibility != nil {
		mi.Visibility = string(*in.Visibility)
	}
	if in.Teams != nil {
		mi.Teams = *in.Teams
	}
	v, err := a.Models.CreateModel(r.Context(), a.actor(r), id, mi)
	a.writeModel(w, r, http.StatusCreated, v, err)
}

func (a *api) updateModel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "modelId")
	if !ok {
		return
	}
	in, rev, ok := decodeRevised[apitypes.ModelUpdate](w, r)
	if !ok {
		return
	}
	mu := llm.ModelUpdate{Name: in.Name, ContextWindow: in.ContextWindow, MaxTokens: in.MaxTokens, Images: in.Images,
		Reasoning: in.Reasoning, InputPrice: in.InputPrice, OutputPrice: in.OutputPrice, Enabled: in.Enabled, Teams: in.Teams}
	if in.Visibility != nil {
		vis := string(*in.Visibility)
		mu.Visibility = &vis
	}
	v, err := a.Models.UpdateModel(r.Context(), a.actor(r), id, mu, rev)
	a.writeModel(w, r, http.StatusOK, v, err)
}

func (a *api) deleteModel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "modelId")
	if !ok {
		return
	}
	writeOK(w, r, a.Models.DeleteModel(r.Context(), a.actor(r), id))
}

func (a *api) listTeamModels(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Models.TeamModels(r.Context(), a.actor(r), r.PathValue("team"))
	writeList(w, r, rows, err, toAPIModel)
}

// toAPIAgentModel converts an agent's model.
func toAPIAgentModel(m *uuid.UUID, name, provider, source string, available bool) *apitypes.AgentModel {
	if m == nil {
		return nil
	}
	return &apitypes.AgentModel{Id: *m, Name: name, ProviderName: provider, Source: apitypes.AgentModelSource(source), Available: available}
}

// usageQuery reads ?from=&to= (dates), ?groupBy= and ?agentId=.
func usageQuery(w http.ResponseWriter, r *http.Request) (llm.UsageQuery, bool) {
	q := llm.UsageQuery{GroupBy: r.URL.Query().Get("groupBy")}
	for _, d := range []struct {
		name string
		dst  *time.Time
	}{{"from", &q.From}, {"to", &q.To}} {
		if v := r.URL.Query().Get(d.name); v != "" {
			t, err := time.Parse(time.DateOnly, v)
			if err != nil {
				failed(w, r, apperr.Invalid("invalid_range", d.name+" must be a date (YYYY-MM-DD)"))
				return q, false
			}
			*d.dst = t
		}
	}
	if v := r.URL.Query().Get("agentId"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			failed(w, r, apperr.Invalid("invalid_agent_id", "agentId must be an agent's ID"))
			return q, false
		}
		q.AgentID = &id
	}
	return q, true
}

func toAPIUsageRow(u llm.UsageRow) apitypes.UsageRow {
	out := apitypes.UsageRow{ModelId: u.ModelID, ModelName: u.ModelName, ProviderName: u.ProviderName, AgentId: u.AgentID,
		AgentName: u.AgentName, Managed: u.Managed, TeamId: u.TeamID, TeamSlug: u.TeamSlug, TeamName: u.TeamName,
		Requests: u.Requests, InputTokens: u.Input, OutputTokens: u.Output, CacheReadTokens: u.CacheRead,
		CacheWriteTokens: u.CacheWrite, CostUsd: u.CostUSD, UnpricedRequests: u.UnpricedRequests}
	if u.Day != nil {
		out.Day = &openapi_types.Date{Time: *u.Day}
	}
	return out
}

func writeUsage(w http.ResponseWriter, r *http.Request, rep llm.UsageReport, err error) {
	if failed(w, r, err) {
		return
	}
	rows := make([]apitypes.UsageRow, len(rep.Rows))
	for i, row := range rep.Rows {
		rows[i] = toAPIUsageRow(row)
	}
	httpx.JSON(w, http.StatusOK, apitypes.UsageReport{From: openapi_types.Date{Time: rep.From}, To: openapi_types.Date{Time: rep.To},
		GroupBy: apitypes.UsageReportGroupBy(rep.GroupBy), Rows: rows, Total: toAPIUsageRow(rep.Total)})
}

func (a *api) getTeamUsage(w http.ResponseWriter, r *http.Request) {
	q, ok := usageQuery(w, r)
	if !ok {
		return
	}
	rep, err := a.Models.TeamUsage(r.Context(), a.actor(r), r.PathValue("team"), q)
	writeUsage(w, r, rep, err)
}

func (a *api) adminGetUsage(w http.ResponseWriter, r *http.Request) {
	q, ok := usageQuery(w, r)
	if !ok {
		return
	}
	rep, err := a.Models.PlatformUsage(r.Context(), a.actor(r), q)
	writeUsage(w, r, rep, err)
}
