package authz

import "github.com/ncecere/pi-durable-platform/internal/apperr"

// Rules of service accounts: a team's owners and admins create, change, disable and delete its
// service accounts and their keys; members (and platform admins and
// auditors) see the accounts; owners and admins (and platform staff) see
// their keys. A service account has the team role member or editor, never
// owner or admin.

// CheckServiceAccountEdit decides whether a team member with role may
// manage the team's service accounts and their keys.
func CheckServiceAccountEdit(role string) error {
	if !RoleAtLeast(role, RoleAdmin) {
		return apperr.Forbidden("Only team owners and admins can manage the team's service accounts")
	}
	return nil
}

// ValidServiceAccountRole reports whether r is a role a service account may
// have: member or editor.
func ValidServiceAccountRole(r string) bool { return r == RoleMember || r == RoleEditor }
