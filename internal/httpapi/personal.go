package httpapi

import (
	"net/http"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

// Personal workspaces (api/openapi.yaml): the defaults platform admins set
// for every personal workspace, one workspace's overrides, and the limits a
// workspace's people see. internal/teams authorizes every call.

func (a *api) personalRoutes() []route {
	return []route{
		{"GET", "/v1/admin/settings/personal-workspaces", a.admin(a.getPersonalSettings)},
		{"PUT", "/v1/admin/settings/personal-workspaces", a.admin(a.updatePersonalSettings)},
		{"PUT", "/v1/admin/teams/{team}/limits", a.admin(a.setWorkspaceLimits)},
		{"GET", "/v1/teams/{team}/limits", a.sessionOrKey(a.getWorkspaceLimits)},
	}
}

func toAPIPersonalSettings(p teams.PersonalSettings) apitypes.PersonalWorkspaceSettings {
	own := p.OwnProviders
	return apitypes.PersonalWorkspaceSettings{Enabled: p.Enabled, MaxAgents: p.MaxAgents, MaxRunning: p.MaxRunning,
		MonthlyBudgetUsd: teams.Dollars(p.MonthlyBudgetUsd), OwnProviders: &own, Revision: p.Revision, UpdatedAt: p.UpdatedAt}
}

func (a *api) getPersonalSettings(w http.ResponseWriter, r *http.Request) {
	p, err := a.Teams.PersonalSettingsFor(r.Context(), a.actor(r))
	if failed(w, r, err) {
		return
	}
	writeRevised(w, http.StatusOK, p.Revision, toAPIPersonalSettings(p))
}

func (a *api) updatePersonalSettings(w http.ResponseWriter, r *http.Request) {
	in, rev, ok := decodeRevised[apitypes.PersonalWorkspaceSettingsUpdate](w, r)
	if !ok {
		return
	}
	p, err := a.Teams.UpdatePersonalSettings(r.Context(), a.actor(r), teams.PersonalSettingsInput{Enabled: in.Enabled,
		MaxAgents: in.MaxAgents, MaxRunning: in.MaxRunning, MonthlyBudgetUSD: in.MonthlyBudgetUsd, OwnProviders: in.OwnProviders}, rev)
	if failed(w, r, err) {
		return
	}
	writeRevised(w, http.StatusOK, p.Revision, toAPIPersonalSettings(p))
}

func (a *api) writeLimits(w http.ResponseWriter, r *http.Request, t dbgen.Team, l teams.Limits) {
	agents, running, err := a.Agents.TeamRunning(r.Context(), t.ID)
	if failed(w, r, err) {
		return
	}
	// Whether the caller may add a model provider here now (the UI's button).
	canAdd, err := a.Models.CanAddProviders(r.Context(), a.actor(r), t)
	if failed(w, r, err) {
		return
	}
	out := apitypes.WorkspaceLimits{Kind: apitypes.WorkspaceLimitsKind(t.Kind), MaxAgents: l.MaxAgents, MaxRunning: l.MaxRunning,
		MonthlyBudgetUsd: l.MonthlyBudgetUSD, Overridden: []string{}, AgentCount: agents, RunningCount: running, CanAddProviders: &canAdd}
	for _, k := range []string{"maxAgents", "maxRunning", "monthlyBudgetUsd"} {
		if l.Overridden[k] {
			out.Overridden = append(out.Overridden, k)
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *api) getWorkspaceLimits(w http.ResponseWriter, r *http.Request) {
	t, l, err := a.Teams.TeamLimits(r.Context(), a.actor(r), r.PathValue("team"))
	if failed(w, r, err) {
		return
	}
	a.writeLimits(w, r, t, l)
}

func (a *api) setWorkspaceLimits(w http.ResponseWriter, r *http.Request) {
	in, rev, ok := decodeRevised[apitypes.WorkspaceLimitsUpdate](w, r)
	if !ok {
		return
	}
	_, err := a.Teams.SetPersonalLimits(r.Context(), a.actor(r), r.PathValue("team"),
		teams.LimitsInput{MaxAgents: in.MaxAgents, MaxRunning: in.MaxRunning, MonthlyBudgetUSD: in.MonthlyBudgetUsd}, rev)
	if failed(w, r, err) {
		return
	}
	t, l, err := a.Teams.TeamLimits(r.Context(), a.actor(r), r.PathValue("team"))
	if failed(w, r, err) {
		return
	}
	a.writeLimits(w, r, t, l)
}
