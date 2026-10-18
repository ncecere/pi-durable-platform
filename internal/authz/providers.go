package authz

import "github.com/ncecere/pi-durable-platform/internal/apperr"

// Rules of a team's own model providers (and their models).
//
//   - A team's owners and admins add providers and models for the team,
//     while platform admins let teams do so (model_settings
//     teams_may_add_providers); a personal workspace's owner too, when
//     platform admins also let personal workspaces (own_providers). They
//     change, delete and discover their providers, and add, change and
//     delete their models, even after adding is turned off.
//   - The team's editors and members read them (keys are never returned).
//   - Platform staff read them on the team's routes; platform admins manage
//     every team's providers through the admin routes (internal/llm), not
//     these rules.
//   - Only people in a browser session manage them: never a service
//     account, a personal API key or an app acting for its person.
//   - An archived team's providers are read-only.

var errProviderNotFound = apperr.NotFound("model_provider_not_found", "Model provider not found")

// ErrTeamProvidersOff refuses a new team provider while platform admins
// have turned team providers off.
var ErrTeamProvidersOff = apperr.Conflict("team_providers_off",
	"Platform admins have turned off adding model providers for teams")

// TeamProviderSubject is a team's providers as seen by an actor.
type TeamProviderSubject struct {
	Role string // the actor's role in the team ("" when not on it)
	// Session: a person in a browser session (not a service account, a
	// personal API key or an app).
	Session bool
	Staff   bool // a platform admin or auditor
	// The team.
	Personal bool
	Archived bool
	// Platform settings: teams may add providers; personal workspaces too.
	TeamsMayAdd    bool
	PersonalMayAdd bool
}

// CheckTeamProviderRead decides whether the actor may see the team's
// providers and their models. Not allowed reads as not found.
func CheckTeamProviderRead(s TeamProviderSubject) error {
	if RoleAtLeast(s.Role, RoleMember) || s.Staff {
		return nil
	}
	return errProviderNotFound
}

// CheckTeamProviderManage decides whether the actor may change, delete or
// discover the team's providers and add, change or delete their models.
func CheckTeamProviderManage(s TeamProviderSubject) error {
	if err := CheckTeamProviderRead(s); err != nil {
		return err
	}
	if !s.Session {
		return apperr.Forbidden("Model providers are managed by people in a browser session, not with keys or apps")
	}
	if !RoleAtLeast(s.Role, RoleAdmin) {
		return apperr.Forbidden("Only the team's owners and admins can manage its model providers")
	}
	if s.Archived {
		return apperr.Conflict("team_archived", "This team is archived and read-only")
	}
	return nil
}

// CheckTeamProviderCreate decides whether the actor may add a provider for
// the team now.
func CheckTeamProviderCreate(s TeamProviderSubject) error {
	if err := CheckTeamProviderManage(s); err != nil {
		return err
	}
	if !s.TeamsMayAdd {
		return ErrTeamProvidersOff
	}
	if s.Personal && !s.PersonalMayAdd {
		return apperr.Conflict("personal_workspace", "Platform admins have not let personal workspaces add their own model providers")
	}
	return nil
}
