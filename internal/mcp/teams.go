package mcp

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/netguard"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teamnet"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

// Team MCP servers: a team's owners and admins add MCP servers for their own
// team (authz.CheckTeamMCP*), as platform admins do in Admin › MCP servers,
// and as they add their own model providers (internal/llm/teams.go). A
// team's server is the team's only: availability ignores its visibility,
// teams and personal flag, platform templates can't attach it, and a
// template attaching one can't go to the catalog.
//
// A team server's URL is the team's choice, so it is proxy-only (direct
// mode would hand agents its URL and headers, and need egress rules a team
// can't grant) and every request to it (reading its tools, the gateway's
// proxy) goes through the address guard (internal/teamnet): https, public
// addresses, private ones only inside model_settings.private_networks (the
// one list for every kind of team service), checked when the URL is saved
// and again at every connection. Platform servers keep the unguarded client.
//
// Slugs stay unique across every owner: a slug names the server's tools in
// an agent's tool namespace (mcp__<slug>__<tool>), where a team's agents meet
// the platform's servers and the team's own.

var (
	errTeamProxyOnly = apperr.Invalid("invalid_access_mode", "A team's MCP servers are proxy-only: agents reach them through the platform")
	errTeamURL       = apperr.Invalid("invalid_url", "A team's MCP server needs an https URL without credentials or fragment")
	errTeamServerUse = apperr.Invalid("team_mcp_visibility",
		"A team's own MCP servers are for that team only: visibility, teams and personal don't apply to them")
	errAddressNotAllowed = apperr.New(http.StatusUnprocessableEntity, "address_not_allowed",
		"The MCP server's address is on a network teams can't use. Ask a platform admin to allow it.")
	errAddressUnresolved = apperr.New(http.StatusUnprocessableEntity, "address_unresolved", "The MCP server's host name could not be resolved")

	// ErrTeamServer refuses a team's own server where only platform servers
	// may go (platform templates).
	ErrTeamServer = apperr.Conflict("team_mcp_server", "Platform templates use platform MCP servers, not a team's own")
)

// SetTeamNetwork sets how teams' servers are reached (at start-up; tests
// replace the resolver, dialer and TLS configuration).
func (s *Service) SetTeamNetwork(n teamnet.Config) { s.team.Set(n) }

// CloseTeamConnections closes the connections kept to teams' servers (the
// private networks changed).
func (s *Service) CloseTeamConnections() { s.team.CloseIdle() }

// clientFor is the client that calls a server: through the address guard
// for a team's.
func (s *Service) clientFor(m dbgen.McpServer) *http.Client {
	if m.TeamID.Valid {
		return s.team.Client()
	}
	return s.client
}

// checkTeamAddress checks a team server's (normalised) URL: https (http only
// on a loopback host with the development allowance), on an address the
// guard allows now.
func (s *Service) checkTeamAddress(ctx context.Context, raw string) error {
	switch err := s.team.CheckURL(ctx, raw); {
	case errors.Is(err, teamnet.ErrScheme):
		return errTeamURL
	case errors.Is(err, teamnet.ErrNotAllowed):
		return errAddressNotAllowed
	case errors.Is(err, teamnet.ErrUnresolved):
		return errAddressUnresolved
	default:
		return err
	}
}

// teamToolsError is what a team server's tools error says: never the
// network error's text, which would show a team what the platform's network
// looks like. blocked: its address is not allowed.
func teamToolsError(err error) (msg string, blocked bool) {
	var re *reachError
	switch {
	case errors.Is(err, netguard.ErrBlockedAddress):
		return "The server's address is on a network teams can't use", true
	case errors.As(err, &re):
		return "Could not reach the server (connection or TLS failed, or it took too long)", false
	}
	return truncate(err.Error(), 500), false
}

// checkTeamServerScope refuses publishing settings on a team's server:
// absent, visibility teams with no teams and personal false are accepted
// (and ignored).
func checkTeamServerScope(visibility *string, teamRefs *[]string, personal *bool) error {
	if (visibility != nil && *visibility != "" && *visibility != "teams") || (teamRefs != nil && len(*teamRefs) > 0) ||
		(personal != nil && *personal) {
		return errTeamServerUse
	}
	return nil
}

// serverSubject is a team's servers as the actor sees them; with settings,
// the platform settings that decide whether it may add one.
func serverSubject(ctx context.Context, q *dbgen.Queries, a authz.Actor, t dbgen.Team, settings bool) (authz.TeamMCPSubject, error) {
	sub, err := teams.ServiceSubject(ctx, q, a, t)
	if err != nil {
		return sub, err
	}
	if settings {
		ms, err := q.GetModelSettings(ctx)
		if err != nil {
			return sub, err
		}
		ps, err := q.GetPersonalSettings(ctx)
		if err != nil {
			return sub, err
		}
		sub.TeamsMayAdd, sub.PersonalMayAdd = ms.TeamsMayAddMcpServers, ps.OwnMcpServers
	}
	return sub, nil
}

// teamAccess resolves a team route's team and checks rule (create: with the
// platform settings). Someone who may not read the team's servers gets team
// not found.
func (s *Service) teamAccess(ctx context.Context, q *dbgen.Queries, a authz.Actor, teamRef string,
	rule func(authz.TeamMCPSubject) error, create bool) (scope, error) {
	t, err := teams.Resolve(ctx, q, teamRef)
	if err != nil {
		return scope{}, err
	}
	sub, err := serverSubject(ctx, q, a, t, create)
	if err != nil {
		return scope{}, err
	}
	if authz.CheckTeamMCPRead(sub) != nil {
		return scope{}, errTeamNotFound
	}
	if err := rule(sub); err != nil {
		return scope{}, err
	}
	return scope{team: &t}, nil
}

// CanAddServers reports whether the actor may add an MCP server for team t
// now (the team's limits, for the UI).
func (s *Service) CanAddServers(ctx context.Context, a authz.Actor, t dbgen.Team) (bool, error) {
	sub, err := serverSubject(ctx, s.q, a, t, true)
	if err != nil {
		return false, err
	}
	return authz.CheckTeamMCPCreate(sub) == nil, nil
}

// TeamOwnServers lists a team's own servers (its members, platform staff).
func (s *Service) TeamOwnServers(ctx context.Context, a authz.Actor, teamRef string) ([]ServerView, error) {
	sc, err := s.teamAccess(ctx, s.q, a, teamRef, authz.CheckTeamMCPRead, false)
	if err != nil {
		return nil, err
	}
	return s.listServers(ctx, false, uuid.NullUUID{UUID: sc.team.ID, Valid: true})
}

// TeamOwnServer returns one of a team's own servers (its members, platform
// staff); another team's or the platform's is not found.
func (s *Service) TeamOwnServer(ctx context.Context, a authz.Actor, teamRef string, id uuid.UUID) (ServerView, error) {
	sc, err := s.teamAccess(ctx, s.q, a, teamRef, authz.CheckTeamMCPRead, false)
	if err != nil {
		return ServerView{}, err
	}
	return s.server(ctx, sc, id)
}

// CreateTeamServer adds an MCP server for the team and reads its tools (its
// owners and admins, while platform admins let teams): proxy only, its URL
// through the address guard.
func (s *Service) CreateTeamServer(ctx context.Context, a authz.Actor, teamRef string, in ServerInput) (ServerView, error) {
	sc, err := s.teamAccess(ctx, s.q, a, teamRef, authz.CheckTeamMCPCreate, true)
	if err != nil {
		return ServerView{}, err
	}
	return s.createServer(ctx, a, sc, in)
}

// UpdateTeamServer changes one of the team's servers (its owners and
// admins, If-Match).
func (s *Service) UpdateTeamServer(ctx context.Context, a authz.Actor, teamRef string, id uuid.UUID, in ServerUpdate, expectedRevision int64) (ServerView, error) {
	sc, err := s.teamAccess(ctx, s.q, a, teamRef, authz.CheckTeamMCPManage, false)
	if err != nil {
		return ServerView{}, err
	}
	return s.updateServer(ctx, a, sc, id, in, expectedRevision)
}

// DeleteTeamServer removes one of the team's servers (its owners and
// admins); refused while template versions use it.
func (s *Service) DeleteTeamServer(ctx context.Context, a authz.Actor, teamRef string, id uuid.UUID) error {
	sc, err := s.teamAccess(ctx, s.q, a, teamRef, authz.CheckTeamMCPManage, false)
	if err != nil {
		return err
	}
	return s.deleteServer(ctx, a, sc, id)
}

// RefreshTeamServer reads one of the team's servers' tools again (its
// owners and admins).
func (s *Service) RefreshTeamServer(ctx context.Context, a authz.Actor, teamRef string, id uuid.UUID) (ServerView, error) {
	sc, err := s.teamAccess(ctx, s.q, a, teamRef, authz.CheckTeamMCPManage, false)
	if err != nil {
		return ServerView{}, err
	}
	return s.refreshTools(ctx, sc, id)
}

// AttachesTeamServer reports whether attach gives agents a team's own server
// (which keeps a template out of the catalog).
func AttachesTeamServer(ctx context.Context, q *dbgen.Queries, attach []Attach) (bool, error) {
	for _, at := range attach {
		m, err := q.GetMCPServer(ctx, at.ServerID)
		if err != nil {
			return false, err
		}
		if m.TeamID.Valid {
			return true, nil
		}
	}
	return false, nil
}

// UsesTeamServer reports whether a template version gives its agents a
// team's own server.
func UsesTeamServer(ctx context.Context, q *dbgen.Queries, templateID uuid.UUID, version int32) (bool, error) {
	return q.TemplateVersionUsesTeamMCP(ctx, dbgen.TemplateVersionUsesTeamMCPParams{TemplateID: templateID, Version: version})
}
