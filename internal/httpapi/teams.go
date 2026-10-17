package httpapi

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

func (a *api) listMyTeams(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Teams.ListMine(r.Context(), a.actor(r).UserID)
	if failed(w, r, err) {
		return
	}
	httpx.JSON(w, http.StatusOK, toAPIMyTeams(rows))
}

func (a *api) getTeam(w http.ResponseWriter, r *http.Request) {
	acc, err := a.Teams.Get(r.Context(), a.actor(r), r.PathValue("team"))
	if failed(w, r, err) {
		return
	}
	out := apitypes.TeamView{Team: toAPITeam(acc.Team)}
	if acc.Role != "" {
		role := apitypes.TeamRole(acc.Role)
		out.Role = &role
	}
	writeRevised(w, http.StatusOK, acc.Team.Revision, out)
}

func (a *api) listMembers(w http.ResponseWriter, r *http.Request) {
	members, err := a.Teams.ListMembers(r.Context(), a.actor(r), r.PathValue("team"))
	writeList(w, r, members, err, toAPIMember)
}

func (a *api) addMember(w http.ResponseWriter, r *http.Request) {
	var in apitypes.MemberAdd
	if !httpx.Decode(w, r, &in) {
		return
	}
	res, err := a.Teams.AddMember(r.Context(), a.actor(r), r.PathValue("team"), string(in.Email), string(in.Role))
	if failed(w, r, err) {
		return
	}
	httpx.JSON(w, http.StatusCreated, toAPIAddResult(res))
}

func (a *api) updateMember(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathUUID(w, r, "userId")
	if !ok {
		return
	}
	in, rev, ok := decodeRevised[apitypes.MemberUpdate](w, r)
	if !ok {
		return
	}
	m, err := a.Teams.UpdateMember(r.Context(), a.actor(r), r.PathValue("team"), userID, string(in.Role), rev)
	if failed(w, r, err) {
		return
	}
	writeRevised(w, http.StatusOK, m.TeamMember.Revision, toAPIMember(m))
}

func (a *api) removeMember(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathUUID(w, r, "userId")
	if !ok {
		return
	}
	writeOK(w, r, a.Teams.RemoveMember(r.Context(), a.actor(r), r.PathValue("team"), userID))
}

func (a *api) listInvites(w http.ResponseWriter, r *http.Request) {
	invites, err := a.Teams.ListInvites(r.Context(), a.actor(r), r.PathValue("team"))
	writeList(w, r, invites, err, toAPIInvite)
}

func (a *api) revokeInvite(w http.ResponseWriter, r *http.Request) {
	inviteID, ok := pathUUID(w, r, "inviteId")
	if !ok {
		return
	}
	writeOK(w, r, a.Teams.RevokeInvite(r.Context(), a.actor(r), r.PathValue("team"), inviteID))
}

func (a *api) listTeamAudit(w http.ResponseWriter, r *http.Request) {
	if scope, ok := a.teamAuditAccess(w, r); ok {
		a.writeAuditPage(w, r, scope)
	}
}

// getTeamAuditEntry serves one entry of the team's log (a linked entry in
// the app's record sheet); entries of other teams are not found.
func (a *api) getTeamAuditEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := pathEntryID(w, r)
	if !ok {
		return
	}
	scope, ok := a.teamAuditAccess(w, r)
	if !ok {
		return
	}
	a.writeAuditEntry(w, r, scope, id)
}

// teamAuditAccess resolves the team and checks the caller may read its log:
// editors and above, or platform staff. It returns the log's scope.
func (a *api) teamAuditAccess(w http.ResponseWriter, r *http.Request) (dbgen.ListAuditParams, bool) {
	actor := a.actor(r)
	acc, err := a.Teams.Get(r.Context(), actor, r.PathValue("team"))
	if failed(w, r, err) {
		return dbgen.ListAuditParams{}, false
	}
	if !authz.RoleAtLeast(acc.Role, authz.RoleEditor) && !actor.CanReadPlatform() {
		httpx.Error(w, http.StatusForbidden, "forbidden", "Only team owners, admins and editors can see the audit log")
		return dbgen.ListAuditParams{}, false
	}
	return dbgen.ListAuditParams{TeamID: uuid.NullUUID{UUID: acc.Team.ID, Valid: true}}, true
}
