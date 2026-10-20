package httpapi

import (
	"net/http"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/mcp"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// The MCP catalog (api/openapi.yaml, tag mcp): servers platform admins
// manage; a team's own servers, managed by its owners and admins (browser
// sessions only: never service accounts, API keys or apps); the servers a
// team may use; and the servers an app gives a thread. internal/mcp and
// internal/agents authorize every call. Header values are write-only.

func (a *api) mcpRoutes() []route {
	const one = "/v1/admin/mcp-servers/{serverId}"
	const own = "/v1/teams/{team}/mcp-servers/own"
	const ownOne = own + "/{serverId}"
	return []route{
		{"GET", "/v1/admin/mcp-servers", a.admin(a.listMCPServers)},
		{"POST", "/v1/admin/mcp-servers", a.admin(a.createMCPServer)},
		{"GET", one, a.admin(a.getMCPServer)},
		{"PATCH", one, a.admin(a.updateMCPServer)},
		{"DELETE", one, a.admin(a.deleteMCPServer)},
		{"POST", one + "/refresh", a.admin(a.refreshMCPServer)},
		{"GET", "/v1/teams/{team}/mcp-servers", a.sessionOrKey(a.listTeamMCPServers)},
		// A team's own servers (browser sessions); /mcp-servers stays the
		// servers the team may use.
		{"GET", own, a.session(a.listTeamOwnMCPServers)},
		{"POST", own, a.session(a.createTeamMCPServer)},
		{"GET", ownOne, a.session(a.getTeamMCPServer)},
		{"PATCH", ownOne, a.session(a.updateTeamMCPServer)},
		{"DELETE", ownOne, a.session(a.deleteTeamMCPServer)},
		{"POST", ownOne + "/refresh", a.session(a.refreshTeamMCPServer)},
		{"GET", "/v1/agents/{agentId}/threads/{threadId}/mcp-servers", a.sessionOrKey(a.getThreadMCPServers)},
		{"PUT", "/v1/agents/{agentId}/threads/{threadId}/mcp-servers", a.sessionOrKey(a.setThreadMCPServers)},
	}
}

func toAPIMCPServer(v mcp.ServerView) apitypes.McpServer {
	m := v.Server
	out := apitypes.McpServer{Id: m.ID, Name: m.Name, Slug: m.Slug, Description: m.Description, Url: m.URL,
		AccessMode: apitypes.McpServerAccessMode(m.AccessMode), Enabled: m.Enabled, Visibility: apitypes.McpServerVisibility(m.Visibility),
		Personal:   m.Personal,
		HasHeaders: v.HasHeaders, Tools: []apitypes.McpTool{}, ToolsRefreshedAt: m.ToolsRefreshedAt, ToolsError: m.ToolsError,
		Revision: m.Revision, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, Team: toAPIOwner(v.Team),
		SendIdentity: m.SendIdentity, IdentityEmail: m.IdentityEmail}
	if m.IdentityAudience != "" {
		out.IdentityAudience = &m.IdentityAudience
	}
	for _, t := range v.Tools {
		tool := apitypes.McpTool{Name: t.Name}
		if t.Title != "" {
			tool.Title = &t.Title
		}
		if t.Description != "" {
			tool.Description = &t.Description
		}
		out.Tools = append(out.Tools, tool)
	}
	if m.Visibility == "teams" && v.Team == nil {
		teams := make([]apitypes.ModelTeam, len(v.Teams))
		for i, t := range v.Teams {
			teams[i] = apitypes.ModelTeam{Id: t.ID, Slug: t.Slug, Name: t.Name}
		}
		out.Teams = &teams
	}
	return out
}

func (a *api) writeMCPServer(w http.ResponseWriter, r *http.Request, status int, v mcp.ServerView, err error) {
	if failed(w, r, err) {
		return
	}
	writeRevised(w, status, v.Server.Revision, toAPIMCPServer(v))
}

func (a *api) listMCPServers(w http.ResponseWriter, r *http.Request) {
	rows, err := a.MCP.Servers(r.Context(), a.actor(r), r.URL.Query().Get("owner"))
	writeList(w, r, rows, err, toAPIMCPServer)
}

func (a *api) listTeamOwnMCPServers(w http.ResponseWriter, r *http.Request) {
	rows, err := a.MCP.TeamOwnServers(r.Context(), a.actor(r), r.PathValue("team"))
	writeList(w, r, rows, err, toAPIMCPServer)
}

func (a *api) createMCPServer(w http.ResponseWriter, r *http.Request) {
	var in apitypes.McpServerCreate
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := a.MCP.CreateServer(r.Context(), a.actor(r), mcpServerInput(in))
	a.writeMCPServer(w, r, http.StatusCreated, v, err)
}

func (a *api) createTeamMCPServer(w http.ResponseWriter, r *http.Request) {
	var in apitypes.McpServerCreate
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := a.MCP.CreateTeamServer(r.Context(), a.actor(r), r.PathValue("team"), mcpServerInput(in))
	a.writeMCPServer(w, r, http.StatusCreated, v, err)
}

func mcpServerInput(in apitypes.McpServerCreate) mcp.ServerInput {
	si := mcp.ServerInput{Name: in.Name, URL: in.Url, Enabled: in.Enabled, Personal: in.Personal,
		Identity: mcp.IdentityInput{Send: in.SendIdentity, Audience: in.IdentityAudience, Email: in.IdentityEmail}}
	setIf(&si.Slug, in.Slug)
	setIf(&si.Description, in.Description)
	if in.Headers != nil {
		si.Headers = *in.Headers
	}
	if in.AccessMode != nil {
		si.AccessMode = string(*in.AccessMode)
	}
	if in.Visibility != nil {
		si.Visibility = string(*in.Visibility)
	}
	if in.Teams != nil {
		si.Teams = *in.Teams
	}
	return si
}

func (a *api) getMCPServer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "serverId")
	if !ok {
		return
	}
	v, err := a.MCP.Server(r.Context(), a.actor(r), id)
	a.writeMCPServer(w, r, http.StatusOK, v, err)
}

func (a *api) getTeamMCPServer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "serverId")
	if !ok {
		return
	}
	v, err := a.MCP.TeamOwnServer(r.Context(), a.actor(r), r.PathValue("team"), id)
	a.writeMCPServer(w, r, http.StatusOK, v, err)
}

func (a *api) updateMCPServer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "serverId")
	if !ok {
		return
	}
	in, rev, ok := decodeRevised[apitypes.McpServerUpdate](w, r)
	if !ok {
		return
	}
	v, err := a.MCP.UpdateServer(r.Context(), a.actor(r), id, mcpServerUpdate(in), rev)
	a.writeMCPServer(w, r, http.StatusOK, v, err)
}

func (a *api) updateTeamMCPServer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "serverId")
	if !ok {
		return
	}
	in, rev, ok := decodeRevised[apitypes.McpServerUpdate](w, r)
	if !ok {
		return
	}
	v, err := a.MCP.UpdateTeamServer(r.Context(), a.actor(r), r.PathValue("team"), id, mcpServerUpdate(in), rev)
	a.writeMCPServer(w, r, http.StatusOK, v, err)
}

func mcpServerUpdate(in apitypes.McpServerUpdate) mcp.ServerUpdate {
	su := mcp.ServerUpdate{Name: in.Name, Description: in.Description, URL: in.Url, Headers: in.Headers, Teams: in.Teams, Enabled: in.Enabled,
		Personal: in.Personal, Identity: mcp.IdentityInput{Send: in.SendIdentity, Audience: in.IdentityAudience, Email: in.IdentityEmail}}
	if in.AccessMode != nil {
		mode := string(*in.AccessMode)
		su.AccessMode = &mode
	}
	if in.Visibility != nil {
		vis := string(*in.Visibility)
		su.Visibility = &vis
	}
	return su
}

func (a *api) deleteMCPServer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "serverId")
	if !ok {
		return
	}
	writeOK(w, r, a.MCP.DeleteServer(r.Context(), a.actor(r), id))
}

func (a *api) deleteTeamMCPServer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "serverId")
	if !ok {
		return
	}
	writeOK(w, r, a.MCP.DeleteTeamServer(r.Context(), a.actor(r), r.PathValue("team"), id))
}

func (a *api) refreshMCPServer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "serverId")
	if !ok {
		return
	}
	v, err := a.MCP.RefreshTools(r.Context(), a.actor(r), id)
	a.writeMCPServer(w, r, http.StatusOK, v, err)
}

func (a *api) refreshTeamMCPServer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "serverId")
	if !ok {
		return
	}
	v, err := a.MCP.RefreshTeamServer(r.Context(), a.actor(r), r.PathValue("team"), id)
	a.writeMCPServer(w, r, http.StatusOK, v, err)
}

func (a *api) listTeamMCPServers(w http.ResponseWriter, r *http.Request) {
	rows, err := a.MCP.TeamServers(r.Context(), a.actor(r), r.PathValue("team"))
	writeList(w, r, rows, err, toAPIMCPServer)
}

func toAPIThreadMCPServer(m dbgen.ThreadMcpServer) apitypes.ThreadMcpServer {
	return apitypes.ThreadMcpServer{Name: m.Name, Slug: m.Slug, Url: m.URL, ExpiresAt: m.ExpiresAt}
}

func (a *api) getThreadMCPServers(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	thread := r.PathValue("threadId")
	if !threadIDRE.MatchString(thread) {
		httpx.Error(w, http.StatusBadRequest, "invalid_thread_id", "Thread IDs are 1-64 letters, digits, dashes or underscores")
		return
	}
	rows, err := a.Agents.ThreadMCP(r.Context(), a.actor(r), id, thread)
	writeList(w, r, rows, err, toAPIThreadMCPServer)
}

func clientServers(in []apitypes.ClientMcpServer) []mcp.ClientServer {
	out := make([]mcp.ClientServer, len(in))
	for i, s := range in {
		out[i] = mcp.ClientServer{Name: s.Name, URL: s.Url}
		if s.Headers != nil {
			out[i].Headers = *s.Headers
		}
		if s.ExpiresInSeconds != nil {
			out[i].ExpiresIn = time.Duration(*s.ExpiresInSeconds) * time.Second
		}
	}
	return out
}

func (a *api) setThreadMCPServers(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	thread := r.PathValue("threadId")
	if !threadIDRE.MatchString(thread) {
		httpx.Error(w, http.StatusBadRequest, "invalid_thread_id", "Thread IDs are 1-64 letters, digits, dashes or underscores")
		return
	}
	var in struct {
		Servers    []apitypes.ClientMcpServer `json:"servers"`
		Visibility string                     `json:"visibility"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	if failed(w, r, a.Agents.SetThreadMCP(r.Context(), a.actor(r), id, thread, in.Visibility, clientServers(in.Servers))) {
		return
	}
	rows, err := a.Agents.ThreadMCP(r.Context(), a.actor(r), id, thread)
	writeList(w, r, rows, err, toAPIThreadMCPServer)
}
