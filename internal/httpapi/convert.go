package httpapi

import (
	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

// Conversions from database rows to API types. Handlers never serialise
// database rows directly, so internal columns (claims, OIDC subjects,
// session digests) cannot leak by accident.

func toAPIUser(u dbgen.User) apitypes.User {
	return apitypes.User{
		Id:           u.ID,
		Email:        u.Email,
		DisplayName:  u.DisplayName,
		PlatformRole: apitypes.PlatformRole(u.PlatformRole),
		Status:       apitypes.UserStatus(u.Status),
		Revision:     u.Revision,
		CreatedAt:    u.CreatedAt,
		LastLoginAt:  u.LastLoginAt,
	}
}

func toAPITeam(t dbgen.Team) apitypes.Team {
	return apitypes.Team{
		Id:          t.ID,
		Slug:        t.Slug,
		Name:        t.Name,
		Description: t.Description,
		Status:      apitypes.TeamStatus(t.Status),
		Kind:        ptrTeamKind(t.Kind),
		Revision:    t.Revision,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
		ArchivedAt:  t.ArchivedAt,
	}
}

func toAPISummary(s teams.Summary) apitypes.TeamSummary {
	out := apitypes.TeamSummary{
		Team: toAPITeam(s.Team), MemberCount: s.MemberCount, OwnerCount: s.OwnerCount, OwnerInvites: s.OwnerInvites,
	}
	if u := s.PersonalOwner; u != nil {
		out.PersonalOwner = &apitypes.MemberUser{Id: u.ID, Email: u.Email, DisplayName: u.DisplayName, Status: apitypes.UserStatus(u.Status)}
	}
	return out
}

func toAPIMyTeams(rows []dbgen.ListTeamsForUserRow) []apitypes.MyTeam {
	out := make([]apitypes.MyTeam, len(rows))
	for i, r := range rows {
		out[i] = apitypes.MyTeam{
			Id: r.ID, Slug: r.Slug, Name: r.Name, Status: apitypes.TeamStatus(r.Status), Role: apitypes.TeamRole(r.MemberRole),
			Kind: ptrMyTeamKind(r.Kind),
		}
	}
	return out
}

func toAPIMember(m teams.Member) apitypes.Member {
	return apitypes.Member{
		User: apitypes.MemberUser{
			Id: m.User.ID, Email: m.User.Email, DisplayName: m.User.DisplayName, Status: apitypes.UserStatus(m.User.Status),
		},
		Role:      apitypes.TeamRole(m.TeamMember.Role),
		Revision:  m.TeamMember.Revision,
		CreatedAt: m.TeamMember.CreatedAt,
	}
}

func toAPIInvite(inv dbgen.TeamInvite) apitypes.Invite {
	out := apitypes.Invite{
		Id: inv.ID, Email: inv.Email, Role: apitypes.TeamRole(inv.Role),
		CreatedAt: inv.CreatedAt, ExpiresAt: inv.ExpiresAt,
	}
	if inv.InvitedBy.Valid {
		out.InvitedBy = &inv.InvitedBy.UUID
	}
	return out
}

func toAPIAddResult(r teams.AddResult) apitypes.MemberAddResult {
	out := apitypes.MemberAddResult{Status: apitypes.MemberAddResultStatus(r.Status)}
	if r.Member != nil {
		m := toAPIMember(*r.Member)
		out.Member = &m
	}
	if r.Invite != nil {
		inv := toAPIInvite(*r.Invite)
		out.Invite = &inv
	}
	return out
}

func nullUUID(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	return &n.UUID
}

func ptrTeamKind(k string) *apitypes.TeamKind {
	v := apitypes.TeamKind(k)
	return &v
}

func ptrMyTeamKind(k string) *apitypes.MyTeamKind {
	v := apitypes.MyTeamKind(k)
	return &v
}
