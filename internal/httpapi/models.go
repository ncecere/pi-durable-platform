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
// managed by platform admins; a team's own providers and models, managed by
// its owners and admins (browser sessions only: never service accounts,
// API keys or apps); the models a team may use. internal/llm authorizes
// every call. Provider keys and header values are write-only.

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
		{"GET", "/v1/admin/settings/models", a.admin(a.getModelSettings)},
		{"PUT", "/v1/admin/settings/models", a.admin(a.updateModelSettings)},
		{"GET", "/v1/teams/{team}/usage", a.sessionOrKey(a.getTeamUsage)},
		{"GET", "/v1/admin/usage", a.admin(a.adminGetUsage)},
	}
}

// teamModelRoutes: a team's own providers and models (browser sessions).
func (a *api) teamModelRoutes() []route {
	const providers = "/v1/teams/{team}/model-providers"
	const provider = providers + "/{providerId}"
	return []route{
		{"GET", "/v1/teams/{team}/model-provider-kinds", a.session(a.listTeamProviderKinds)},
		{"GET", providers, a.session(a.listTeamModelProviders)},
		{"POST", providers, a.session(a.createTeamModelProvider)},
		{"GET", provider, a.session(a.getTeamModelProvider)},
		{"PATCH", provider, a.session(a.updateTeamModelProvider)},
		{"DELETE", provider, a.session(a.deleteTeamModelProvider)},
		{"POST", provider + "/discover", a.session(a.discoverTeamModels)},
		{"GET", provider + "/models", a.session(a.listTeamProviderModels)},
		{"POST", provider + "/models", a.session(a.createTeamModel)},
		{"PATCH", "/v1/teams/{team}/models/{modelId}", a.session(a.updateTeamModel)},
		{"DELETE", "/v1/teams/{team}/models/{modelId}", a.session(a.deleteTeamModel)},
	}
}

func toAPIOwner(o *llm.Owner) *apitypes.ModelOwner {
	if o == nil {
		return nil
	}
	return &apitypes.ModelOwner{Id: o.ID, Slug: o.Slug, Name: o.Name, Kind: apitypes.ModelOwnerKind(o.Kind)}
}

func toAPIProvider(v llm.ProviderView) apitypes.ModelProvider {
	p := v.Provider
	return apitypes.ModelProvider{Id: p.ID, Name: p.Name, Kind: apitypes.ModelProviderKind(p.Kind), BaseUrl: p.BaseURL,
		AccessMode: apitypes.ModelProviderAccessMode(p.AccessMode), Enabled: p.Enabled, HasHeaders: v.HasHeaders,
		ModelCount: v.ModelCount, Revision: p.Revision, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Team: toAPIOwner(v.Team)}
}

func toAPIModel(v llm.ModelView) apitypes.Model {
	m := v.Model
	out := apitypes.Model{Id: m.ID, ProviderId: m.ProviderID, ProviderName: v.ProviderName, ProviderKind: v.ProviderKind,
		UpstreamId: m.UpstreamID, Name: m.Name, ContextWindow: m.ContextWindow, MaxTokens: m.MaxTokens, Images: m.Images,
		Reasoning: m.Reasoning, Enabled: m.Enabled, Visibility: apitypes.ModelVisibility(m.Visibility), Personal: m.Personal, Revision: m.Revision,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, ProviderTeam: toAPIOwner(v.ProviderTeam)}
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
	if m.Visibility == "teams" && v.ProviderTeam == nil {
		teams := make([]apitypes.ModelTeam, len(v.Teams))
		for i, t := range v.Teams {
			teams[i] = apitypes.ModelTeam{Id: t.ID, Slug: t.Slug, Name: t.Name}
		}
		out.Teams = &teams
	}
	return out
}

func (a *api) listProviderKinds(w http.ResponseWriter, r *http.Request) {
	writeList(w, r, llm.Kinds, nil, toAPIKind)
}

func (a *api) listTeamProviderKinds(w http.ResponseWriter, r *http.Request) {
	err := a.Models.CheckTeamKinds(r.Context(), a.actor(r), r.PathValue("team"))
	writeList(w, r, llm.Kinds, err, toAPIKind)
}

func toAPIKind(k llm.Kind) apitypes.ProviderType {
	out := apitypes.ProviderType{Id: apitypes.ProviderTypeId(k.ID), Name: k.Name, Api: k.API,
		AuthHeader: apitypes.ProviderTypeAuthHeader(k.AuthHeader)}
	if k.DefaultBaseURL != "" {
		u := k.DefaultBaseURL
		out.DefaultBaseUrl = &u
	}
	return out
}

func (a *api) listModelProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Models.Providers(r.Context(), a.actor(r), r.URL.Query().Get("owner"))
	writeList(w, r, rows, err, toAPIProvider)
}

func (a *api) listTeamModelProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Models.TeamProviders(r.Context(), a.actor(r), r.PathValue("team"))
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
	v, err := a.Models.CreateProvider(r.Context(), a.actor(r), providerInput(in))
	a.writeProvider(w, r, http.StatusCreated, v, err)
}

func (a *api) createTeamModelProvider(w http.ResponseWriter, r *http.Request) {
	var in apitypes.ModelProviderCreate
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := a.Models.CreateTeamProvider(r.Context(), a.actor(r), r.PathValue("team"), providerInput(in))
	a.writeProvider(w, r, http.StatusCreated, v, err)
}

func providerInput(in apitypes.ModelProviderCreate) llm.ProviderInput {
	pi := llm.ProviderInput{Name: in.Name, Kind: string(in.Kind), Enabled: in.Enabled}
	setIf(&pi.BaseURL, in.BaseUrl)
	setIf(&pi.APIKey, in.ApiKey)
	if in.AccessMode != nil {
		pi.AccessMode = string(*in.AccessMode)
	}
	if in.Headers != nil {
		pi.Headers = *in.Headers
	}
	return pi
}

func (a *api) getModelProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "providerId")
	if !ok {
		return
	}
	v, err := a.Models.Provider(r.Context(), a.actor(r), id)
	a.writeProvider(w, r, http.StatusOK, v, err)
}

func (a *api) getTeamModelProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "providerId")
	if !ok {
		return
	}
	v, err := a.Models.TeamProvider(r.Context(), a.actor(r), r.PathValue("team"), id)
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
	v, err := a.Models.UpdateProvider(r.Context(), a.actor(r), id, providerUpdate(in), rev)
	a.writeProvider(w, r, http.StatusOK, v, err)
}

func (a *api) updateTeamModelProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "providerId")
	if !ok {
		return
	}
	in, rev, ok := decodeRevised[apitypes.ModelProviderUpdate](w, r)
	if !ok {
		return
	}
	v, err := a.Models.UpdateTeamProvider(r.Context(), a.actor(r), r.PathValue("team"), id, providerUpdate(in), rev)
	a.writeProvider(w, r, http.StatusOK, v, err)
}

func providerUpdate(in apitypes.ModelProviderUpdate) llm.ProviderUpdate {
	pu := llm.ProviderUpdate{Name: in.Name, BaseURL: in.BaseUrl, APIKey: in.ApiKey, Headers: in.Headers, Enabled: in.Enabled}
	if in.AccessMode != nil {
		mode := string(*in.AccessMode)
		pu.AccessMode = &mode
	}
	return pu
}

func (a *api) deleteModelProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "providerId")
	if !ok {
		return
	}
	writeOK(w, r, a.Models.DeleteProvider(r.Context(), a.actor(r), id))
}

func (a *api) deleteTeamModelProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "providerId")
	if !ok {
		return
	}
	writeOK(w, r, a.Models.DeleteTeamProvider(r.Context(), a.actor(r), r.PathValue("team"), id))
}

func toAPIDiscovered(d llm.Discovered) apitypes.DiscoveredModel {
	return apitypes.DiscoveredModel{Id: d.ID, Name: d.Name, ContextWindow: d.ContextWindow, Added: d.Added}
}

func (a *api) discoverModels(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "providerId")
	if !ok {
		return
	}
	rows, err := a.Models.Discover(r.Context(), a.actor(r), id)
	writeList(w, r, rows, err, toAPIDiscovered)
}

func (a *api) discoverTeamModels(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "providerId")
	if !ok {
		return
	}
	rows, err := a.Models.DiscoverTeam(r.Context(), a.actor(r), r.PathValue("team"), id)
	writeList(w, r, rows, err, toAPIDiscovered)
}

func (a *api) listProviderModels(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "providerId")
	if !ok {
		return
	}
	rows, err := a.Models.Models(r.Context(), a.actor(r), &id, "")
	writeList(w, r, rows, err, toAPIModel)
}

func (a *api) listTeamProviderModels(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "providerId")
	if !ok {
		return
	}
	rows, err := a.Models.TeamProviderModels(r.Context(), a.actor(r), r.PathValue("team"), id)
	writeList(w, r, rows, err, toAPIModel)
}

func (a *api) listModels(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Models.Models(r.Context(), a.actor(r), nil, r.URL.Query().Get("owner"))
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
	v, err := a.Models.CreateModel(r.Context(), a.actor(r), id, modelInput(in))
	a.writeModel(w, r, http.StatusCreated, v, err)
}

func (a *api) createTeamModel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "providerId")
	if !ok {
		return
	}
	var in apitypes.ModelCreate
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := a.Models.CreateTeamModel(r.Context(), a.actor(r), r.PathValue("team"), id, modelInput(in))
	a.writeModel(w, r, http.StatusCreated, v, err)
}

func modelInput(in apitypes.ModelCreate) llm.ModelInput {
	mi := llm.ModelInput{UpstreamID: in.UpstreamId, ContextWindow: in.ContextWindow, MaxTokens: in.MaxTokens,
		InputPrice: in.InputPrice, OutputPrice: in.OutputPrice, Enabled: in.Enabled, Personal: in.Personal}
	setIf(&mi.Name, in.Name)
	setIf(&mi.Images, in.Images)
	setIf(&mi.Reasoning, in.Reasoning)
	if in.Visibility != nil {
		mi.Visibility = string(*in.Visibility)
	}
	if in.Teams != nil {
		mi.Teams = *in.Teams
	}
	return mi
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
	v, err := a.Models.UpdateModel(r.Context(), a.actor(r), id, modelUpdate(in), rev)
	a.writeModel(w, r, http.StatusOK, v, err)
}

func (a *api) updateTeamModel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "modelId")
	if !ok {
		return
	}
	in, rev, ok := decodeRevised[apitypes.ModelUpdate](w, r)
	if !ok {
		return
	}
	v, err := a.Models.UpdateTeamModel(r.Context(), a.actor(r), r.PathValue("team"), id, modelUpdate(in), rev)
	a.writeModel(w, r, http.StatusOK, v, err)
}

func modelUpdate(in apitypes.ModelUpdate) llm.ModelUpdate {
	mu := llm.ModelUpdate{Name: in.Name, ContextWindow: in.ContextWindow, MaxTokens: in.MaxTokens, Images: in.Images,
		Reasoning: in.Reasoning, InputPrice: in.InputPrice, OutputPrice: in.OutputPrice, Enabled: in.Enabled, Teams: in.Teams,
		Personal: in.Personal}
	if in.Visibility != nil {
		vis := string(*in.Visibility)
		mu.Visibility = &vis
	}
	return mu
}

func (a *api) deleteModel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "modelId")
	if !ok {
		return
	}
	writeOK(w, r, a.Models.DeleteModel(r.Context(), a.actor(r), id))
}

func (a *api) deleteTeamModel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "modelId")
	if !ok {
		return
	}
	writeOK(w, r, a.Models.DeleteTeamModel(r.Context(), a.actor(r), r.PathValue("team"), id))
}

func toAPIModelSettings(m llm.Settings) apitypes.ModelSettings {
	nets := m.PrivateNetworks
	if nets == nil {
		nets = []string{}
	}
	return apitypes.ModelSettings{TeamsMayAddProviders: m.TeamsMayAddProviders, PrivateNetworks: nets, Revision: m.Revision,
		UpdatedAt: m.UpdatedAt}
}

func (a *api) getModelSettings(w http.ResponseWriter, r *http.Request) {
	m, err := a.Models.Settings(r.Context(), a.actor(r))
	if failed(w, r, err) {
		return
	}
	writeRevised(w, http.StatusOK, m.Revision, toAPIModelSettings(m))
}

func (a *api) updateModelSettings(w http.ResponseWriter, r *http.Request) {
	in, rev, ok := decodeRevised[apitypes.ModelSettingsUpdate](w, r)
	if !ok {
		return
	}
	m, err := a.Models.UpdateSettings(r.Context(), a.actor(r), llm.SettingsInput{TeamsMayAddProviders: in.TeamsMayAddProviders,
		PrivateNetworks: in.PrivateNetworks}, rev)
	if failed(w, r, err) {
		return
	}
	writeRevised(w, http.StatusOK, m.Revision, toAPIModelSettings(m))
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
