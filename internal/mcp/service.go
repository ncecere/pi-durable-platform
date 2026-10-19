package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/metrics"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teamnet"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

// Access modes.
const (
	ModeProxy  = "proxy"
	ModeDirect = "direct"
)

var (
	errServerNotFound = apperr.NotFound("mcp_server_not_found", "MCP server not found")
	errTeamNotFound   = apperr.NotFound("team_not_found", "Team not found")
	errNameTaken      = apperr.Conflict("mcp_server_name_taken", "An MCP server with that name already exists")
	errSlugTaken      = apperr.Conflict("mcp_server_name_taken",
		"An MCP server with that slug already exists (slugs name agents' tools, so they are unique across the platform and every team)")
	errInUse         = apperr.Conflict("mcp_server_in_use", "Template versions use this MCP server: disable it instead")
	errBadName       = apperr.Invalid("invalid_name", "Name must be 1-100 characters on one line")
	errBadSlug       = apperr.Invalid("invalid_slug", "The slug is 1-32 lower-case letters, digits or underscores (it names the server's tools)")
	errBadDesc       = apperr.Invalid("invalid_description", "Description must be at most 2000 characters")
	errBadURL        = apperr.Invalid("invalid_url", "The URL must be an http or https URL without credentials or fragment")
	errBadMode       = apperr.Invalid("invalid_access_mode", "Access mode is proxy or direct")
	errBadVisibility = apperr.Invalid("invalid_visibility", "Visibility is all or teams")
	errBadHeaders    = apperr.Invalid("invalid_headers", "Headers: at most 20, valid names (not host, content or session headers), values up to 4096 characters on one line")
	// ErrUnavailable: the server is disabled, or not published to the team.
	ErrUnavailable = apperr.Conflict("mcp_server_unavailable", "That MCP server is not available to this team (disabled, or not published to it)")

	slugRE       = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_]{0,30}[a-z0-9])?$`)
	headerNameRE = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_|~-]{1,100}$`)
	reserved     = map[string]bool{"host": true, "content-length": true, "content-type": true, "connection": true,
		"transfer-encoding": true, "te": true, "upgrade": true, "keep-alive": true, "trailer": true, "accept": true,
		"accept-encoding": true, "mcp-session-id": true, "mcp-protocol-version": true, "last-event-id": true,
		"proxy-authorization": true}
)

// Service manages MCP servers.
type Service struct {
	pool   *pgxpool.Pool
	q      *dbgen.Queries
	box    *secrets.Box
	client *http.Client // platform servers
	// team reaches teams' own servers, through the address guard.
	team *teamnet.Guard
	// Client lets apps give agents their own MCP servers for a thread.
	Client ClientPolicy
	// Metrics counts the MCP proxy's requests and tool calls (nil: none).
	Metrics *metrics.Metrics
}

// ClientPolicy says which MCP servers apps may give agents, and how agents
// reach them. No hosts: none.
type ClientPolicy struct {
	Hosts []HostPattern
	// Mode is proxy (through the gateway) or direct.
	Mode string
}

// New returns a Service. Teams' servers are reached through the address
// guard without the development allowance (see SetTeamNetwork).
func New(pool *pgxpool.Pool, box *secrets.Box, policy ClientPolicy) *Service {
	if policy.Mode == "" {
		policy.Mode = ModeProxy
	}
	q := dbgen.New(pool)
	return &Service{pool: pool, q: q, box: box, client: newHTTPClient(), Client: policy,
		team: teamnet.New(teamnet.SettingsNetworks(q), 5*time.Minute)}
}

func serverAD(id uuid.UUID) []byte { return []byte("mcp_server:" + id.String()) }
func threadAD(id uuid.UUID) []byte { return []byte("thread_mcp_server:" + id.String()) }

// ServerView is a server without its header values, with its teams.
type ServerView struct {
	Server     dbgen.McpServer
	HasHeaders bool
	Tools      []Tool
	Teams      []dbgen.ListMCPServerTeamsRow
	// Team owns a team's own server (nil: a platform server).
	Team *teams.Owner
}

func requireStaff(a authz.Actor) error {
	if !a.CanReadPlatform() {
		return apperr.Forbidden("Only platform admins and auditors can see MCP servers")
	}
	return nil
}

func requireAdmin(a authz.Actor) error {
	if !a.IsPlatformAdmin() {
		return apperr.Forbidden("Only platform admins can manage MCP servers")
	}
	return nil
}

// view is m without its header values, owned by owner (nil: a platform
// server); withTeams adds the teams a platform server is published to.
func (s *Service) view(ctx context.Context, q *dbgen.Queries, m dbgen.McpServer, withTeams bool, owner *teams.Owner) (ServerView, error) {
	v := ServerView{Server: m, HasHeaders: len(m.HeadersEnc) > 0, Team: owner}
	v.Server.HeadersEnc = nil
	if err := json.Unmarshal(m.Tools, &v.Tools); err != nil {
		v.Tools = nil
	}
	if withTeams && m.Visibility == "teams" && !m.TeamID.Valid {
		var err error
		if v.Teams, err = q.ListMCPServerTeams(ctx, m.ID); err != nil {
			return v, err
		}
	}
	return v, nil
}

// fullView is view with the teams and the owner looked up.
func (s *Service) fullView(ctx context.Context, q *dbgen.Queries, m dbgen.McpServer) (ServerView, error) {
	owner, err := teams.OwnerByID(ctx, q, m.TeamID)
	if err != nil {
		return ServerView{}, err
	}
	return s.view(ctx, q, m, true, owner)
}

// scope is where a call comes from: the admin routes (team nil: every
// server, platform admins and auditors) or a team's routes (team: that
// team's own servers only, checked by teamAccess).
type scope struct {
	team *dbgen.Team
}

var adminScope = scope{}

// reaches reports errServerNotFound unless the scope reaches server m: a
// team's routes reach only the team's own servers.
func (sc scope) reaches(m dbgen.McpServer) error {
	if sc.team != nil && (!m.TeamID.Valid || m.TeamID.UUID != sc.team.ID) {
		return errServerNotFound
	}
	return nil
}

// getServer returns a server the scope reaches.
func getServer(ctx context.Context, q *dbgen.Queries, sc scope, id uuid.UUID, lock bool) (dbgen.McpServer, error) {
	get := q.GetMCPServer
	if lock {
		get = q.LockMCPServer
	}
	m, err := get(ctx, id)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return m, errServerNotFound
	} else if err != nil {
		return m, err
	}
	return m, sc.reaches(m)
}

// Servers lists MCP servers (platform staff): every one (owner "" or all),
// the platform's (platform), or one team's (its slug or ID).
func (s *Service) Servers(ctx context.Context, a authz.Actor, owner string) ([]ServerView, error) {
	if err := requireStaff(a); err != nil {
		return nil, err
	}
	platformOnly, team, err := teams.OwnerFilter(ctx, s.q, owner)
	if err != nil {
		return nil, err
	}
	return s.listServers(ctx, platformOnly, team)
}

func (s *Service) listServers(ctx context.Context, platformOnly bool, team uuid.NullUUID) ([]ServerView, error) {
	rows, err := s.q.ListMCPServers(ctx, dbgen.ListMCPServersParams{PlatformOnly: platformOnly, TeamID: team})
	if err != nil {
		return nil, err
	}
	out := make([]ServerView, 0, len(rows))
	for _, r := range rows {
		v, err := s.view(ctx, s.q, r.McpServer, true, teams.OwnerOf(r.McpServer.TeamID, r.TeamSlug, r.TeamName, r.TeamKind))
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Server returns any MCP server (platform staff).
func (s *Service) Server(ctx context.Context, a authz.Actor, id uuid.UUID) (ServerView, error) {
	if err := requireStaff(a); err != nil {
		return ServerView{}, err
	}
	return s.server(ctx, adminScope, id)
}

func (s *Service) server(ctx context.Context, sc scope, id uuid.UUID) (ServerView, error) {
	m, err := getServer(ctx, s.q, sc, id, false)
	if err != nil {
		return ServerView{}, err
	}
	return s.fullView(ctx, s.q, m)
}

// ServerInput is a new MCP server. Visibility, Teams and Personal publish a
// platform server; a team's server is its team's only (see
// checkTeamServerScope).
type ServerInput struct {
	Name, Slug, Description, URL, AccessMode, Visibility string
	Headers                                              map[string]string
	Teams                                                []string
	Enabled                                              *bool
	// Personal: available in personal workspaces (default: when visibility is all).
	Personal *bool
}

func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 100 || strings.ContainsAny(name, "\r\n\x00") {
		return "", errBadName
	}
	return name, nil
}

// SlugFrom makes a tool-name slug from a name ("GitHub Issues" -> "github_issues").
func SlugFrom(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "_"):
			b.WriteByte('_')
		}
	}
	slug := strings.Trim(b.String(), "_")
	if len(slug) > 32 {
		slug = strings.TrimRight(slug[:32], "_")
	}
	return slug
}

func checkURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || len(raw) > 2048 {
		return nil, errBadURL
	}
	return u, nil
}

func checkHeaders(h map[string]string) error {
	if len(h) > 20 {
		return errBadHeaders
	}
	for k, v := range h {
		if !headerNameRE.MatchString(k) || reserved[strings.ToLower(k)] || len(v) > 4096 || strings.ContainsAny(v, "\r\n\x00") {
			return errBadHeaders
		}
	}
	return nil
}

func (s *Service) seal(h map[string]string, ad []byte) ([]byte, error) {
	if len(h) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	return s.box.Seal(b, ad)
}

func (s *Service) open(enc, ad []byte) (map[string]string, error) {
	if len(enc) == 0 {
		return nil, nil
	}
	raw, err := s.box.Open(enc, ad)
	if err != nil {
		return nil, err
	}
	var h map[string]string
	return h, json.Unmarshal(raw, &h)
}

func snapshot(m dbgen.McpServer, teamSlugs []string) map[string]any {
	out := map[string]any{"name": m.Name, "slug": m.Slug, "url": m.URL, "accessMode": m.AccessMode, "enabled": m.Enabled,
		"visibility": m.Visibility, "personal": m.Personal, "hasHeaders": len(m.HeadersEnc) > 0}
	if m.Visibility == "teams" {
		out["teams"] = teamSlugs
	}
	return out
}

func setTeams(ctx context.Context, q *dbgen.Queries, serverID uuid.UUID, refs []string) ([]string, error) {
	if err := q.ClearMCPServerTeams(ctx, serverID); err != nil {
		return nil, err
	}
	slugs := []string{}
	for _, ref := range refs {
		t, err := teams.Resolve(ctx, q, ref)
		if err != nil {
			return nil, err
		}
		if err := q.AddMCPServerTeam(ctx, dbgen.AddMCPServerTeamParams{ServerID: serverID, TeamID: t.ID}); err != nil {
			return nil, err
		}
		slugs = append(slugs, t.Slug)
	}
	return slugs, nil
}

// serverAudit is an audit entry about a server, in its team's log when a
// team owns it.
func serverAudit(a authz.Actor, action string, m dbgen.McpServer) audit.Entry {
	e := a.Audit(action, "mcp_server", m.ID.String())
	if m.TeamID.Valid {
		e.TeamID = m.TeamID.UUID
	}
	return e
}

// CreateServer adds a platform MCP server and reads its tools (platform
// admins). A server that cannot be reached is still added, with the error
// recorded.
func (s *Service) CreateServer(ctx context.Context, a authz.Actor, in ServerInput) (ServerView, error) {
	if err := requireAdmin(a); err != nil {
		return ServerView{}, err
	}
	return s.createServer(ctx, a, adminScope, in)
}

// createServer adds a server: the platform's (admin scope) or the scope's
// team's (proxy only, for the team only, its URL through the address
// guard).
func (s *Service) createServer(ctx context.Context, a authz.Actor, sc scope, in ServerInput) (ServerView, error) {
	name, err := checkName(in.Name)
	if err != nil {
		return ServerView{}, err
	}
	if in.Slug == "" {
		in.Slug = SlugFrom(name)
	}
	if !slugRE.MatchString(in.Slug) {
		return ServerView{}, errBadSlug
	}
	if utf8.RuneCountInString(in.Description) > 2000 {
		return ServerView{}, errBadDesc
	}
	u, err := checkURL(in.URL)
	if err != nil {
		return ServerView{}, err
	}
	if in.AccessMode == "" {
		in.AccessMode = ModeProxy
	}
	if in.AccessMode != ModeProxy && in.AccessMode != ModeDirect {
		return ServerView{}, errBadMode
	}
	var teamID uuid.NullUUID
	personal := personalDefault(in.Personal, in.Visibility)
	if sc.team != nil {
		teamID = uuid.NullUUID{UUID: sc.team.ID, Valid: true}
		if in.AccessMode != ModeProxy {
			return ServerView{}, errTeamProxyOnly
		}
		var refs *[]string
		if in.Teams != nil {
			refs = &in.Teams
		}
		if err := checkTeamServerScope(&in.Visibility, refs, in.Personal); err != nil {
			return ServerView{}, err
		}
		in.Visibility, in.Teams, personal = "teams", nil, false
		if err := s.checkTeamAddress(ctx, u.String()); err != nil {
			return ServerView{}, err
		}
	}
	if in.Visibility == "" {
		in.Visibility = "all"
		personal = personalDefault(in.Personal, in.Visibility)
	}
	if in.Visibility != "all" && in.Visibility != "teams" {
		return ServerView{}, errBadVisibility
	}
	if err := checkHeaders(in.Headers); err != nil {
		return ServerView{}, err
	}
	id := uuid.New()
	enc, err := s.seal(in.Headers, serverAD(id))
	if err != nil {
		return ServerView{}, err
	}
	var created dbgen.McpServer
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		if sc.team != nil {
			// The rule again, in the transaction (settings may have changed).
			if _, err := s.teamAccess(ctx, q, a, sc.team.ID.String(), authz.CheckTeamMCPCreate, true); err != nil {
				return err
			}
		}
		m, err := q.InsertMCPServer(ctx, dbgen.InsertMCPServerParams{ID: id, Name: name, Slug: in.Slug, Description: in.Description,
			URL: u.String(), HeadersEnc: enc, AccessMode: in.AccessMode, Enabled: in.Enabled == nil || *in.Enabled,
			Visibility: in.Visibility, CreatedBy: uuid.NullUUID{UUID: a.UserID, Valid: a.UserID != uuid.Nil},
			Personal: personal, TeamID: teamID})
		if apperr.IsUniqueViolation(err, "mcp_servers_slug_key") {
			return errSlugTaken
		} else if apperr.IsUniqueViolation(err, "") {
			return errNameTaken
		} else if err != nil {
			return err
		}
		slugs := []string{}
		if m.Visibility == "teams" && !m.TeamID.Valid {
			if slugs, err = setTeams(ctx, q, id, in.Teams); err != nil {
				return err
			}
		}
		e := serverAudit(a, "mcp_server.create", m)
		e.After = snapshot(m, slugs)
		created = m
		return audit.Record(ctx, q, e)
	})
	if err != nil {
		return ServerView{}, err
	}
	v, _, err := s.refresh(ctx, created, in.Headers)
	return v, err
}

// ServerUpdate holds optional changes. Headers replaces every header (an
// empty map removes them); Teams replaces the teams.
type ServerUpdate struct {
	Name, Description, URL, AccessMode, Visibility *string
	Headers                                        *map[string]string
	Teams                                          *[]string
	Enabled                                        *bool
	Personal                                       *bool
}

// UpdateServer changes any server (platform admins, If-Match). The slug
// does not change: agents' tool names depend on it. A team's server stays
// proxy-only and its team's only, and a new URL goes through the address
// guard.
func (s *Service) UpdateServer(ctx context.Context, a authz.Actor, id uuid.UUID, in ServerUpdate, expectedRevision int64) (ServerView, error) {
	if err := requireAdmin(a); err != nil {
		return ServerView{}, err
	}
	return s.updateServer(ctx, a, adminScope, id, in, expectedRevision)
}

func (s *Service) updateServer(ctx context.Context, a authz.Actor, sc scope, id uuid.UUID, in ServerUpdate, expectedRevision int64) (ServerView, error) {
	// A team server's new URL is checked before the transaction (the check
	// resolves its host); a server's owner never changes.
	cur, err := getServer(ctx, s.q, sc, id, false)
	if err != nil {
		return ServerView{}, err
	}
	var newURL string
	if in.URL != nil {
		u, err := checkURL(*in.URL)
		if err != nil {
			return ServerView{}, err
		}
		newURL = u.String()
		if cur.TeamID.Valid {
			if err := s.checkTeamAddress(ctx, newURL); err != nil {
				return ServerView{}, err
			}
		}
	}
	var updated dbgen.McpServer
	reachChanged := false
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		m, err := getServer(ctx, q, sc, id, true)
		if err != nil {
			return err
		}
		if m.Revision != expectedRevision {
			return apperr.Stale()
		}
		if m.TeamID.Valid {
			if err := checkTeamServerScope(in.Visibility, in.Teams, in.Personal); err != nil {
				return err
			}
			in.Visibility, in.Teams, in.Personal = nil, nil, nil
		}
		before, err := q.ListMCPServerTeams(ctx, id)
		if err != nil {
			return err
		}
		beforeSlugs := make([]string, len(before))
		for i, t := range before {
			beforeSlugs[i] = t.Slug
		}
		p := dbgen.UpdateMCPServerParams{ID: id, Name: m.Name, Description: m.Description, URL: m.URL, HeadersEnc: m.HeadersEnc,
			AccessMode: m.AccessMode, Enabled: m.Enabled, Visibility: m.Visibility, Personal: m.Personal}
		if in.Personal != nil {
			p.Personal = *in.Personal
		}
		if in.Name != nil {
			if p.Name, err = checkName(*in.Name); err != nil {
				return err
			}
		}
		if in.Description != nil {
			if utf8.RuneCountInString(*in.Description) > 2000 {
				return errBadDesc
			}
			p.Description = *in.Description
		}
		if in.URL != nil {
			reachChanged = reachChanged || newURL != m.URL
			p.URL = newURL
		}
		if in.Headers != nil {
			if err := checkHeaders(*in.Headers); err != nil {
				return err
			}
			if p.HeadersEnc, err = s.seal(*in.Headers, serverAD(id)); err != nil {
				return err
			}
			reachChanged = true
		}
		if in.AccessMode != nil {
			if *in.AccessMode != ModeProxy && *in.AccessMode != ModeDirect {
				return errBadMode
			}
			if m.TeamID.Valid && *in.AccessMode != ModeProxy {
				return errTeamProxyOnly
			}
			p.AccessMode = *in.AccessMode
		}
		if in.Visibility != nil {
			if *in.Visibility != "all" && *in.Visibility != "teams" {
				return errBadVisibility
			}
			p.Visibility = *in.Visibility
		}
		if in.Enabled != nil {
			p.Enabled = *in.Enabled
		}
		if updated, err = q.UpdateMCPServer(ctx, p); apperr.IsUniqueViolation(err, "") {
			return errNameTaken
		} else if err != nil {
			return err
		}
		slugs := beforeSlugs
		switch {
		case updated.Visibility == "all":
			if err := q.ClearMCPServerTeams(ctx, id); err != nil {
				return err
			}
			slugs = nil
		case in.Teams != nil:
			if slugs, err = setTeams(ctx, q, id, *in.Teams); err != nil {
				return err
			}
		}
		e := serverAudit(a, "mcp_server.update", m)
		e.Before, e.After = snapshot(m, beforeSlugs), snapshot(updated, slugs)
		return audit.Record(ctx, q, e)
	})
	if err != nil {
		return ServerView{}, err
	}
	if reachChanged {
		headers, err := s.open(updated.HeadersEnc, serverAD(id))
		if err != nil {
			return ServerView{}, err
		}
		v, _, err := s.refresh(ctx, updated, headers)
		return v, err
	}
	return s.fullView(ctx, s.q, updated)
}

// RefreshTools reads any server's tools again (platform admins).
func (s *Service) RefreshTools(ctx context.Context, a authz.Actor, id uuid.UUID) (ServerView, error) {
	if err := requireAdmin(a); err != nil {
		return ServerView{}, err
	}
	return s.refreshTools(ctx, adminScope, id)
}

// refreshTools reads a server's tools again. A team's server whose address
// teams can't use (now) is refused with address_not_allowed (the error is
// recorded too).
func (s *Service) refreshTools(ctx context.Context, sc scope, id uuid.UUID) (ServerView, error) {
	m, err := getServer(ctx, s.q, sc, id, false)
	if err != nil {
		return ServerView{}, err
	}
	headers, err := s.open(m.HeadersEnc, serverAD(id))
	if err != nil {
		return ServerView{}, err
	}
	v, blocked, err := s.refresh(ctx, m, headers)
	if err == nil && blocked {
		return ServerView{}, errAddressNotAllowed
	}
	return v, err
}

// refresh lists the server's tools and stores them, or the error (the
// previous list stays); blocked: a team's server's address is not allowed.
// A team's server is called through the address guard, and its errors
// don't say what the platform's network looks like.
func (s *Service) refresh(ctx context.Context, m dbgen.McpServer, headers map[string]string) (ServerView, bool, error) {
	tools, err := ListTools(ctx, s.clientFor(m), m.URL, headers)
	raw := m.Tools
	var problem *string
	blocked := false
	if err != nil {
		msg := truncate(err.Error(), 500)
		if m.TeamID.Valid {
			msg, blocked = teamToolsError(err)
		}
		problem = &msg
	} else if raw, err = json.Marshal(tools); err != nil {
		return ServerView{}, false, err
	}
	if tools == nil && problem == nil {
		raw = json.RawMessage("[]")
	}
	updated, err := s.q.SetMCPServerTools(ctx, dbgen.SetMCPServerToolsParams{ID: m.ID, Tools: raw, ToolsError: problem})
	if err != nil {
		return ServerView{}, false, err
	}
	v, err := s.fullView(ctx, s.q, updated)
	return v, blocked, err
}

// DeleteServer removes any server (platform admins); refused while
// template versions use it.
func (s *Service) DeleteServer(ctx context.Context, a authz.Actor, id uuid.UUID) error {
	if err := requireAdmin(a); err != nil {
		return err
	}
	return s.deleteServer(ctx, a, adminScope, id)
}

func (s *Service) deleteServer(ctx context.Context, a authz.Actor, sc scope, id uuid.UUID) error {
	return store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		m, err := getServer(ctx, q, sc, id, true)
		if err != nil {
			return err
		}
		if err := q.DeleteMCPServer(ctx, id); apperr.IsForeignKeyViolation(err, "") {
			return errInUse
		} else if err != nil {
			return err
		}
		e := serverAudit(a, "mcp_server.delete", m)
		e.Before = snapshot(m, nil)
		return audit.Record(ctx, q, e)
	})
}

// TeamServers lists the MCP servers a team may use (its members and service
// accounts, platform staff): its own and the platform's published to it.
func (s *Service) TeamServers(ctx context.Context, a authz.Actor, teamRef string) ([]ServerView, error) {
	t, err := teams.Resolve(ctx, s.q, teamRef)
	if err != nil {
		return nil, err
	}
	allowed := a.CanReadPlatform()
	if a.Service != nil {
		allowed = a.Service.TeamID == t.ID
	} else if !allowed {
		role, err := teams.MemberRole(ctx, s.q, t.ID, a.UserID)
		if err != nil {
			return nil, err
		}
		allowed = role != ""
	}
	if !allowed {
		return nil, errTeamNotFound
	}
	rows, err := s.q.ListAvailableMCPServers(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	out := make([]ServerView, 0, len(rows))
	for _, r := range rows {
		var owner *teams.Owner
		if r.TeamID.Valid {
			owner = teams.OwnerFromTeam(t) // only the team's own are available to it
		}
		v, err := s.view(ctx, s.q, r, false, owner)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Attach is a server a template version gives its agents: all its tools, or
// only Tools.
type Attach struct {
	ServerID uuid.UUID
	Tools    []string
}

// CheckAttach checks a template version's servers: a team template's must be
// usable by the team (its own, or the platform's published to it); a
// platform template's enabled platform servers (ErrTeamServer for a team's
// own). Tools must be the server's.
func CheckAttach(ctx context.Context, q *dbgen.Queries, teamID uuid.NullUUID, attach []Attach) error {
	seen := map[uuid.UUID]bool{}
	for _, at := range attach {
		if seen[at.ServerID] {
			return apperr.Invalid("invalid_mcp", "An MCP server is attached twice")
		}
		seen[at.ServerID] = true
		m, err := q.GetMCPServer(ctx, at.ServerID)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errServerNotFound
		} else if err != nil {
			return err
		}
		if teamID.Valid {
			ok, err := q.MCPServerAvailable(ctx, dbgen.MCPServerAvailableParams{ServerID: at.ServerID, TeamID: teamID.UUID})
			if err != nil {
				return err
			}
			if !ok {
				return ErrUnavailable
			}
		} else if m.TeamID.Valid {
			return ErrTeamServer
		} else if !m.Enabled {
			return ErrUnavailable
		}
		if at.Tools != nil {
			var tools []Tool
			_ = json.Unmarshal(m.Tools, &tools)
			names := map[string]bool{}
			for _, t := range tools {
				names[t.Name] = true
			}
			for _, t := range at.Tools {
				if !names[t] {
					return apperr.Invalid("invalid_mcp_tool", "The MCP server "+m.Name+" has no tool "+truncate(t, 128))
				}
			}
		}
	}
	return nil
}

// SaveAttach records a version's servers.
func SaveAttach(ctx context.Context, q *dbgen.Queries, templateID uuid.UUID, version int32, attach []Attach) error {
	for _, at := range attach {
		if err := q.InsertTemplateVersionMCP(ctx, dbgen.InsertTemplateVersionMCPParams{TemplateID: templateID, Version: version,
			ServerID: at.ServerID, Tools: at.Tools}); err != nil {
			return err
		}
	}
	return nil
}

func personalDefault(p *bool, visibility string) bool {
	if p != nil {
		return *p
	}
	return visibility == "all"
}
