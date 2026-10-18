package authz

import "github.com/ncecere/pi-durable-platform/internal/apperr"

// Rules of agents. Every agent belongs to a team.
//
//   - The team's members and service accounts use its agents (talk to them,
//     read and test their conversations). Editors and above (people or
//     editor service accounts) create and change agents; owners and admins
//     delete them.
//   - A service-managed agent (created by a service account with its own
//     key, such as one per user of a chat app) holds conversations of the
//     service's users: only the team's service accounts use it. The team's
//     owners and admins see that it exists and can delete it; editor service
//     accounts change and delete it.
//   - Platform admins may delete any agent. Platform admins and auditors see
//     the agents of personal workspaces (audit). Platform roles never grant
//     access to conversations.

var errAgentNotFound = apperr.NotFound("agent_not_found", "Agent not found")

// AgentSubject is an agent as seen by an actor.
type AgentSubject struct {
	Role           string // the actor's role in the agent's team ("" when not on it)
	IsService      bool   // the actor is a service account
	ServiceManaged bool   // the agent is service-managed
	// Audit: the actor is platform staff and the agent is in a personal
	// workspace (they see its settings and status, never its conversations).
	Audit bool
}

// CheckAgentSee decides whether the actor may see the agent's settings and
// status (not its conversations). Not allowed reads as not found.
func CheckAgentSee(s AgentSubject) error {
	if CheckAgentUse(s) == nil || (s.ServiceManaged && !s.IsService && RoleAtLeast(s.Role, RoleAdmin)) || s.Audit {
		return nil
	}
	return errAgentNotFound
}

// CheckAgentUse decides whether the actor may talk to the agent and read its
// conversations. Not allowed reads as not found, so an agent's existence is
// not revealed.
func CheckAgentUse(s AgentSubject) error {
	if RoleAtLeast(s.Role, RoleMember) && (!s.ServiceManaged || s.IsService) {
		return nil
	}
	return errAgentNotFound
}

// CheckAgentEdit decides whether the actor may change the agent.
func CheckAgentEdit(s AgentSubject) error {
	if err := CheckAgentUse(s); err != nil {
		return err
	}
	if !RoleAtLeast(s.Role, RoleEditor) {
		return apperr.Forbidden("Only team editors, admins and owners can change the team's agents")
	}
	return nil
}

// CheckAgentDelete decides whether the actor may delete the agent and all
// its conversations. platformAdmin may delete any agent without reading it.
func CheckAgentDelete(s AgentSubject, platformAdmin bool) error {
	if platformAdmin {
		return nil
	}
	if err := CheckAgentSee(s); err != nil {
		return err
	}
	switch {
	case !s.IsService && RoleAtLeast(s.Role, RoleAdmin):
		return nil
	case s.ServiceManaged && s.IsService && RoleAtLeast(s.Role, RoleEditor):
		return nil
	}
	return apperr.Forbidden("Only team owners and admins can delete the team's agents")
}

// CheckTeamAgentCreate decides whether a team member (or service account)
// with role may create a team agent.
func CheckTeamAgentCreate(role string) error {
	if !RoleAtLeast(role, RoleEditor) {
		return apperr.Forbidden("Only team editors, admins and owners can create agents")
	}
	return nil
}

// CheckServiceManagedCreate decides whether the actor may create
// service-managed agents: editor service accounts only.
func CheckServiceManagedCreate(isService bool, role string) error {
	if !isService {
		return apperr.Forbidden("Agents with an external key are created by service accounts (with their keys)")
	}
	if !RoleAtLeast(role, RoleEditor) {
		return apperr.Forbidden("Only editor service accounts can create agents")
	}
	return nil
}
