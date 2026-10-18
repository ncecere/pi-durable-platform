// Package authz holds the authorization rules shared by every module: the
// acting principal and the platform and team roles.
//
// Rules live here as pure functions so they can be unit-tested exhaustively.
// Services call them inside the same transaction that performs the change,
// after locking the rows the decision depends on.
package authz

import (
	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/audit"
)

// Platform roles.
const (
	PlatformNone    = "none"
	PlatformAdmin   = "platform_admin"
	PlatformAuditor = "platform_auditor"
)

// Team roles, most to least privileged.
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleEditor = "editor"
	RoleMember = "member"
)

// Actor is the principal performing an operation, plus request metadata for
// the audit log: a person (browser session, personal API key, or an app they
// connected with OAuth Connect), or a team's service account (its key). A
// service account has no user ID and no platform role; it acts in its own
// team with its own team role. A connected app acts as its person with no
// platform role.
type Actor struct {
	UserID       uuid.UUID
	PlatformRole string
	RequestID    string
	ClientIP     string

	// APIKey is the personal API key the person used (zero: a session).
	APIKeyID   uuid.UUID
	APIKeyName string

	// OAuthGrantID is the connected app's grant the person acts through
	// (zero: not an app), and OAuthClientName the app's name.
	OAuthGrantID    uuid.UUID
	OAuthClientName string

	// Service is set for a service account.
	Service *ServiceActor
}

// ServiceActor is a team service account acting through one of its keys.
type ServiceActor struct {
	AccountID uuid.UUID
	Name      string
	TeamID    uuid.UUID
	Role      string // member or editor
	KeyName   string
}

func (a Actor) IsPlatformAdmin() bool { return a.Service == nil && a.PlatformRole == PlatformAdmin }

// CanReadPlatform reports whether the actor may read platform metadata
// (users, teams, audit). It never implies access to team content.
func (a Actor) CanReadPlatform() bool {
	return a.Service == nil && (a.PlatformRole == PlatformAdmin || a.PlatformRole == PlatformAuditor)
}

// Audit returns an audit entry pre-filled with the actor.
func (a Actor) Audit(action, targetType, targetID string) audit.Entry {
	e := audit.Entry{
		ActorKind: audit.ActorUser, ActorUserID: a.UserID,
		Action: action, TargetType: targetType, TargetID: targetID,
		RequestID: a.RequestID, ClientIP: a.ClientIP,
	}
	switch {
	case a.Service != nil:
		e.ActorKind, e.ActorUserID, e.ActorServiceAccountID = audit.ActorServiceAccount, uuid.Nil, a.Service.AccountID
		e.Metadata = map[string]any{"serviceAccountName": a.Service.Name, "keyName": a.Service.KeyName}
	case a.APIKeyID != uuid.Nil:
		e.ActorKind = audit.ActorAPIKey
		e.Metadata = map[string]any{"apiKeyId": a.APIKeyID.String(), "apiKeyName": a.APIKeyName}
	case a.OAuthGrantID != uuid.Nil:
		e.ActorKind = audit.ActorOAuth
		e.Metadata = map[string]any{"oauthGrantId": a.OAuthGrantID.String(), "oauthClient": a.OAuthClientName}
	}
	return e
}

var roleRank = map[string]int{RoleMember: 1, RoleEditor: 2, RoleAdmin: 3, RoleOwner: 4}

// ValidTeamRole reports whether r is a team role.
func ValidTeamRole(r string) bool { return roleRank[r] > 0 }

// RoleAtLeast reports whether role grants at least min. "" (not a member)
// never does.
func RoleAtLeast(role, min string) bool { return roleRank[role] > 0 && roleRank[role] >= roleRank[min] }

// CheckMemberChange decides whether a team member with actorRole may change
// another member's role from `from` to `to`. Use from = "" to add a member and
// to = "" to remove one. Leaving a team yourself is handled separately.
//
//   - Owners and admins manage members.
//   - Admins cannot add, change or remove owners, or make anyone an owner.
func CheckMemberChange(actorRole, from, to string) error {
	if !RoleAtLeast(actorRole, RoleAdmin) {
		return apperr.Forbidden("Only team owners and admins can manage members")
	}
	if to != "" && !ValidTeamRole(to) {
		return apperr.Invalid("invalid_role", "Role must be owner, admin, editor or member")
	}
	if actorRole != RoleOwner && (from == RoleOwner || to == RoleOwner) {
		return apperr.Forbidden("Only team owners can add, change or remove owners")
	}
	return nil
}
