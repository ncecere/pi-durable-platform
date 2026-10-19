package store

import "github.com/ncecere/pi-durable-platform/internal/store/dbgen"

// User statuses (users.status): set by platform admins.
const (
	UserActive    = "active"
	UserSuspended = "suspended"
)

// IsUserActive reports whether a person may use the platform now: not
// suspended by an admin (status) and not denied by the IdP group rule
// (group_denied_at: see internal/auth). The two are separate so that neither
// overwrites the other; every check that a person is allowed in goes
// through here.
func IsUserActive(u dbgen.User) bool { return u.Status == UserActive && u.GroupDeniedAt == nil }
