package authz

import "github.com/ncecere/pi-durable-platform/internal/apperr"

// Rules of a team's own MCP servers: those of every team service
// (providers.go), with the MCP switches (model_settings
// teams_may_add_mcp_servers, personal workspace settings own_mcp_servers).

// TeamMCPSubject is a team's MCP servers as seen by an actor.
type TeamMCPSubject = TeamServiceSubject

// ErrTeamMCPServersOff refuses a new team MCP server while platform admins
// have turned team MCP servers off.
var ErrTeamMCPServersOff = apperr.Conflict("team_mcp_servers_off",
	"Platform admins have turned off adding MCP servers for teams")

var teamMCPServers = teamService{
	notFound: apperr.NotFound("mcp_server_not_found", "MCP server not found"),
	keys:     "MCP servers are managed by people in a browser session, not with keys or apps",
	owners:   "Only the team's owners and admins can manage its MCP servers",
	off:      ErrTeamMCPServersOff,
	personal: "Platform admins have not let personal workspaces add their own MCP servers",
}

// CheckTeamMCPRead decides whether the actor may see the team's own MCP
// servers. Not allowed reads as not found.
func CheckTeamMCPRead(s TeamMCPSubject) error { return teamMCPServers.read(s) }

// CheckTeamMCPManage decides whether the actor may change or delete the
// team's MCP servers, or refresh their tools.
func CheckTeamMCPManage(s TeamMCPSubject) error { return teamMCPServers.manage(s) }

// CheckTeamMCPCreate decides whether the actor may add an MCP server for
// the team now.
func CheckTeamMCPCreate(s TeamMCPSubject) error { return teamMCPServers.create(s) }
