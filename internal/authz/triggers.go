package authz

import "github.com/ncecere/pi-durable-platform/internal/apperr"

// Rules of an agent's triggers (schedules and webhooks that start runs),
// their limits and their runs (docs/triggers.md).
//
//   - Everyone who may use the agent reads its triggers, limits and runs
//     (people and service accounts); platform staff read them as they read
//     agents (settings and status, never conversations). An agent-made
//     trigger's message is conversation-derived: internal/triggers withholds
//     it from staff, and from those who can't read its thread.
//   - Only the agent's team's owners and admins manage them (create, change,
//     pause, delete, rotate secrets, run now, set the agent's limits), as
//     people in a browser session: never a service account, a personal API
//     key or an app acting for its person. A trigger is standing permission
//     to run the agent, unattended, with the team's credentials and at its
//     cost; like adding a model provider, that is an owner's or admin's
//     decision in the console. In a personal workspace that is its owner.
//   - Service-managed agents take none (their people may not use them).
//   - An archived team's are read-only.

// TriggerSubject is an agent's triggers as seen by an actor.
type TriggerSubject struct {
	AgentSubject
	// Session: a person in a browser session (not a service account, a
	// personal API key or an app).
	Session bool
	// Archived: the agent's team is archived.
	Archived bool
}

// CheckTriggerRead decides whether the actor may see the agent's triggers,
// trigger limits and runs. Not allowed reads as not found.
func CheckTriggerRead(s AgentSubject) error {
	if CheckAgentUse(s) == nil || s.Audit {
		return nil
	}
	return errAgentNotFound
}

// CheckTriggerManage decides whether the actor may create, change, delete
// or fire the agent's triggers and set its trigger limits.
func CheckTriggerManage(s TriggerSubject) error {
	if err := CheckAgentUse(s.AgentSubject); err != nil {
		return err
	}
	if s.IsService || !s.Session {
		return apperr.Forbidden("Triggers are managed by people in a browser session, not with keys or apps")
	}
	if !RoleAtLeast(s.Role, RoleAdmin) {
		return apperr.Forbidden("Only the team's owners and admins can manage its agents' triggers")
	}
	if s.Archived {
		return apperr.Conflict("team_archived", "This team is archived and read-only")
	}
	return nil
}
