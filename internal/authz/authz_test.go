package authz

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/audit"
)

func TestCheckMemberChange(t *testing.T) {
	roles := []string{"", RoleMember, RoleEditor, RoleAdmin, RoleOwner}
	for _, actor := range roles {
		for _, from := range roles {
			for _, to := range roles {
				err := CheckMemberChange(actor, from, to)
				allowed := err == nil
				want := false
				switch actor {
				case RoleOwner:
					want = true
				case RoleAdmin:
					want = from != RoleOwner && to != RoleOwner
				}
				if allowed != want {
					t.Errorf("actor=%q from=%q to=%q: allowed=%v want %v (%v)", actor, from, to, allowed, want, err)
				}
			}
		}
	}
	if err := CheckMemberChange(RoleOwner, "", "superuser"); err == nil {
		t.Error("invalid role accepted")
	} else if e, _ := apperr.As(err); e.Status != http.StatusBadRequest {
		t.Errorf("invalid role status = %d", e.Status)
	}
	if err := CheckMemberChange(RoleMember, "", RoleMember); err == nil {
		t.Error("member manages members")
	} else if e, _ := apperr.As(err); e.Status != http.StatusForbidden {
		t.Errorf("member status = %d", e.Status)
	}
}

func TestRoleAtLeast(t *testing.T) {
	if RoleAtLeast("", RoleMember) || RoleAtLeast("bogus", RoleMember) {
		t.Error("non-member treated as member")
	}
	if !RoleAtLeast(RoleOwner, RoleAdmin) || RoleAtLeast(RoleEditor, RoleAdmin) || !RoleAtLeast(RoleEditor, RoleEditor) {
		t.Error("role ordering wrong")
	}
	for _, r := range []string{RoleOwner, RoleAdmin, RoleEditor, RoleMember} {
		if !ValidTeamRole(r) {
			t.Errorf("%s not valid", r)
		}
	}
	if ValidTeamRole("") || ValidTeamRole(PlatformAdmin) {
		t.Error("non-team role accepted")
	}
}

func TestPlatformRoles(t *testing.T) {
	cases := []struct {
		role        string
		admin, read bool
	}{
		{PlatformNone, false, false},
		{PlatformAuditor, false, true},
		{PlatformAdmin, true, true},
		{"", false, false},
	}
	for _, c := range cases {
		a := Actor{PlatformRole: c.role}
		if a.IsPlatformAdmin() != c.admin || a.CanReadPlatform() != c.read {
			t.Errorf("%q: admin=%v read=%v", c.role, a.IsPlatformAdmin(), a.CanReadPlatform())
		}
	}
}

func TestActorAudit(t *testing.T) {
	id := uuid.New()
	e := Actor{UserID: id, RequestID: "req", ClientIP: "203.0.113.1"}.Audit("team.create", "team", "t1")
	if e.ActorKind != audit.ActorUser || e.ActorUserID != id || e.Action != "team.create" || e.TargetType != "team" ||
		e.TargetID != "t1" || e.RequestID != "req" || e.ClientIP != "203.0.113.1" {
		t.Errorf("entry = %+v", e)
	}
}

func TestServiceAccountRules(t *testing.T) {
	for role, want := range map[string]bool{RoleOwner: true, RoleAdmin: true, RoleEditor: false, RoleMember: false, "": false} {
		if (CheckServiceAccountEdit(role) == nil) != want {
			t.Errorf("CheckServiceAccountEdit(%q) != %v", role, want)
		}
	}
	for role, want := range map[string]bool{RoleMember: true, RoleEditor: true, RoleAdmin: false, RoleOwner: false, "": false} {
		if ValidServiceAccountRole(role) != want {
			t.Errorf("ValidServiceAccountRole(%q) != %v", role, want)
		}
	}
}
