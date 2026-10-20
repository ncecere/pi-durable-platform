package authz

import "github.com/ncecere/pi-durable-platform/internal/apperr"

// Rules of tool approvals (docs/approvals.md). Whoever may read the thread
// sees its approvals: the agent's users (CheckAgentUse) who can see the thread
// (a private one: its creator). Of them, who may decide:
//
//   - a private thread: its creator;
//   - a shared thread: the person or service account whose message started
//     the run, the team's owners and admins, and members whose role is at
//     least the version's approval role;
//   - an unattended run (no author, such as a trigger): the team's owners and
//     admins.
//
// A service-managed agent's people never read its conversations, so its
// editor service accounts act as its admins. Platform roles never decide:
// staff not on the team can't use the agent at all.

var (
	errApprovalNotFound  = apperr.NotFound("approval_not_found", "Approval not found")
	errApprovalForbidden = apperr.New(403, "approval_forbidden", "You may not decide this approval")
)

// ApprovalSubject is an approval as seen by an actor.
type ApprovalSubject struct {
	Agent AgentSubject
	// SeesThread: the thread is shared, or the actor's own private one.
	SeesThread bool
	// PrivateThread: the thread is private (then SeesThread means the actor
	// created it).
	PrivateThread bool
	// RunAuthor: the actor sent the message that started the run.
	RunAuthor bool
	// Unattended: the run has no author.
	Unattended bool
	// MinRole is the version's approval role: the lowest team role that may
	// decide calls in shared threads (RoleAdmin when unset).
	MinRole string
}

// CheckApprovalSee decides whether the actor may see the approval (and its
// redacted arguments). Not allowed reads as not found.
func CheckApprovalSee(s ApprovalSubject) error {
	if CheckAgentUse(s.Agent) != nil || !s.SeesThread {
		return errApprovalNotFound
	}
	return nil
}

// CheckApprovalDecide decides whether the actor may approve or deny the call.
func CheckApprovalDecide(s ApprovalSubject) error {
	if err := CheckApprovalSee(s); err != nil {
		return err
	}
	if s.PrivateThread {
		return nil // SeesThread: the actor created it
	}
	if s.RunAuthor {
		return nil
	}
	a := s.Agent
	admin := (!a.IsService && RoleAtLeast(a.Role, RoleAdmin)) || (a.IsService && a.ServiceManaged && RoleAtLeast(a.Role, RoleEditor))
	if admin {
		return nil
	}
	minRole := s.MinRole
	if minRole == "" {
		minRole = RoleAdmin
	}
	if !s.Unattended && RoleAtLeast(a.Role, minRole) {
		return nil
	}
	return errApprovalForbidden
}
