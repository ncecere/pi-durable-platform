package authz

import "github.com/ncecere/pi-durable-platform/internal/apperr"

// Rules of a team's own services: its model providers (and their models)
// and its MCP servers.
//
//   - A team's owners and admins add them for the team, while platform
//     admins let teams do so (model_settings teams_may_add_providers,
//     teams_may_add_mcp_servers); a personal workspace's owner too, when
//     platform admins also let personal workspaces (personal workspace
//     settings own_providers, own_mcp_servers). They change and delete them
//     (and discover a provider's models, or refresh a server's tools), even
//     after adding is turned off.
//   - The team's editors and members read them (keys and header values are
//     never returned).
//   - Platform staff read them on the team's routes; platform admins manage
//     every team's through the admin routes (internal/llm, internal/mcp),
//     not these rules.
//   - Only people in a browser session manage them: never a service
//     account, a personal API key or an app acting for its person.
//   - An archived team's are read-only.

// TeamServiceSubject is a team's own services of one kind as seen by an
// actor.
type TeamServiceSubject struct {
	Role string // the actor's role in the team ("" when not on it)
	// Session: a person in a browser session (not a service account, a
	// personal API key or an app).
	Session bool
	Staff   bool // a platform admin or auditor
	// The team.
	Personal bool
	Archived bool
	// Platform settings: teams may add this kind of service; personal
	// workspaces too.
	TeamsMayAdd    bool
	PersonalMayAdd bool
}

// TeamProviderSubject is a team's providers as seen by an actor.
type TeamProviderSubject = TeamServiceSubject

// teamService is one kind of team service's refusals.
type teamService struct {
	notFound error
	keys     string // refusal for keys and apps
	owners   string // refusal for editors and members
	off      error  // teams may not add them
	personal string // personal workspaces may not add them
}

func (k teamService) read(s TeamServiceSubject) error {
	if RoleAtLeast(s.Role, RoleMember) || s.Staff {
		return nil
	}
	return k.notFound
}

func (k teamService) manage(s TeamServiceSubject) error {
	if err := k.read(s); err != nil {
		return err
	}
	if !s.Session {
		return apperr.Forbidden(k.keys)
	}
	if !RoleAtLeast(s.Role, RoleAdmin) {
		return apperr.Forbidden(k.owners)
	}
	if s.Archived {
		return apperr.Conflict("team_archived", "This team is archived and read-only")
	}
	return nil
}

func (k teamService) create(s TeamServiceSubject) error {
	if err := k.manage(s); err != nil {
		return err
	}
	if !s.TeamsMayAdd {
		return k.off
	}
	if s.Personal && !s.PersonalMayAdd {
		return apperr.Conflict("personal_workspace", k.personal)
	}
	return nil
}

// ErrTeamProvidersOff refuses a new team provider while platform admins
// have turned team providers off.
var ErrTeamProvidersOff = apperr.Conflict("team_providers_off",
	"Platform admins have turned off adding model providers for teams")

var teamProviders = teamService{
	notFound: apperr.NotFound("model_provider_not_found", "Model provider not found"),
	keys:     "Model providers are managed by people in a browser session, not with keys or apps",
	owners:   "Only the team's owners and admins can manage its model providers",
	off:      ErrTeamProvidersOff,
	personal: "Platform admins have not let personal workspaces add their own model providers",
}

// CheckTeamProviderRead decides whether the actor may see the team's
// providers and their models. Not allowed reads as not found.
func CheckTeamProviderRead(s TeamProviderSubject) error { return teamProviders.read(s) }

// CheckTeamProviderManage decides whether the actor may change, delete or
// discover the team's providers and add, change or delete their models.
func CheckTeamProviderManage(s TeamProviderSubject) error { return teamProviders.manage(s) }

// CheckTeamProviderCreate decides whether the actor may add a provider for
// the team now.
func CheckTeamProviderCreate(s TeamProviderSubject) error { return teamProviders.create(s) }
