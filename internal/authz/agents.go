package authz

import "github.com/ncecere/pi-durable-platform/internal/apperr"

// Rules of agents.
//
//   - A personal agent (a person's assistant) is used and managed only by its
//     owner. Platform admins and auditors cannot read its conversations; a
//     platform admin can turn it off or delete it.
//   - A team agent is used by every member of the team (and the team's
//     service accounts). Editors and above create and change team agents;
//     owners and admins delete them.
//
// Admin roles never imply access to conversations.

// Agent kinds.
const (
	AgentPersonal = "personal"
	AgentTeam     = "team"
)

var errAgentNotFound = apperr.NotFound("agent_not_found", "Agent not found")

// AgentSubject is an agent as seen by an actor.
type AgentSubject struct {
	Kind    string
	IsOwner bool   // personal: the actor owns it
	Role    string // team: the actor's team role ("" when not a member)
}

// CheckAgentUse decides whether the actor may talk to the agent and read its
// conversations. Not allowed reads as not found, so an agent's existence is
// not revealed.
func CheckAgentUse(s AgentSubject) error {
	switch s.Kind {
	case AgentPersonal:
		if s.IsOwner {
			return nil
		}
	case AgentTeam:
		if RoleAtLeast(s.Role, RoleMember) {
			return nil
		}
	}
	return errAgentNotFound
}

// CheckAgentEdit decides whether the actor may change the agent.
func CheckAgentEdit(s AgentSubject) error {
	if err := CheckAgentUse(s); err != nil {
		return err
	}
	if s.Kind == AgentTeam && !RoleAtLeast(s.Role, RoleEditor) {
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
	if err := CheckAgentUse(s); err != nil {
		return err
	}
	if s.Kind == AgentTeam && !RoleAtLeast(s.Role, RoleAdmin) {
		return apperr.Forbidden("Only team owners and admins can delete the team's agents")
	}
	return nil
}

// CheckTeamAgentCreate decides whether a team member with role may create a
// team agent.
func CheckTeamAgentCreate(role string) error {
	if !RoleAtLeast(role, RoleEditor) {
		return apperr.Forbidden("Only team editors, admins and owners can create agents")
	}
	return nil
}
