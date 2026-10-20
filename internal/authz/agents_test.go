package authz

import (
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
)

func apperrStatus(err error) int {
	e, ok := apperr.As(err)
	if !ok {
		return 0
	}
	return e.Status
}

func TestAgentRules(t *testing.T) {
	person := func(role string, managed bool) AgentSubject { return AgentSubject{Role: role, ServiceManaged: managed} }
	svc := func(role string, managed bool) AgentSubject {
		return AgentSubject{Role: role, IsService: true, ServiceManaged: managed}
	}
	cases := []struct {
		name                            string
		s                               AgentSubject
		see, use, edit, del, delAsAdmin bool
		hibernate                       bool
	}{
		{"member", person(RoleMember, false), true, true, false, false, true, true},
		{"editor", person(RoleEditor, false), true, true, true, false, true, true},
		{"admin", person(RoleAdmin, false), true, true, true, true, true, true},
		{"owner", person(RoleOwner, false), true, true, true, true, true, true},
		{"not on the team", person("", false), false, false, false, false, true, false},
		{"member service account", svc(RoleMember, false), true, true, false, false, true, true},
		{"editor service account", svc(RoleEditor, false), true, true, true, false, true, true},
		{"another team's service account", svc("", false), false, false, false, false, true, false},
		{"service-managed: member service account", svc(RoleMember, true), true, true, false, false, true, true},
		{"service-managed: editor service account", svc(RoleEditor, true), true, true, true, true, true, true},
		{"service-managed: owner (a person)", person(RoleOwner, true), true, false, false, true, true, true},
		{"service-managed: admin (a person)", person(RoleAdmin, true), true, false, false, true, true, true},
		{"service-managed: editor (a person)", person(RoleEditor, true), false, false, false, false, true, false},
		{"service-managed: not on the team", person("", true), false, false, false, false, true, false},
		// Platform staff not on the team (any team, personal or shared): they see the agent, read-only.
		{"platform staff", AgentSubject{Audit: true}, true, false, false, false, true, false},
		{"service-managed: platform staff", AgentSubject{ServiceManaged: true, Audit: true}, true, false, false, false, true, false},
	}
	for _, c := range cases {
		check := func(what string, got, want bool) {
			if got != want {
				t.Errorf("%s: %s = %v, want %v", c.name, what, got, want)
			}
		}
		check("see", CheckAgentSee(c.s) == nil, c.see)
		check("use", CheckAgentUse(c.s) == nil, c.use)
		check("edit", CheckAgentEdit(c.s) == nil, c.edit)
		check("delete", CheckAgentDelete(c.s, false) == nil, c.del)
		check("delete as platform admin", CheckAgentDelete(c.s, true) == nil, c.delAsAdmin)
		check("hibernate", CheckAgentHibernate(c.s) == nil, c.hibernate)
	}
	if CheckTeamAgentCreate(RoleMember) == nil || CheckTeamAgentCreate(RoleEditor) != nil {
		t.Error("team agent creation: editors and above only")
	}
	if CheckKeyedAgentCreate(false, RoleOwner, false) == nil || CheckKeyedAgentCreate(true, RoleMember, false) == nil ||
		CheckKeyedAgentCreate(true, RoleEditor, false) != nil {
		t.Error("keyed agents in a shared team: editor service accounts only")
	}
	if CheckKeyedAgentCreate(false, RoleOwner, true) != nil || CheckKeyedAgentCreate(false, "", true) == nil ||
		CheckKeyedAgentCreate(true, RoleEditor, true) == nil {
		t.Error("keyed agents in a personal workspace: its owner only")
	}
}

func TestAgentMoveRules(t *testing.T) {
	shared := func(role string) MoveSide { return MoveSide{Role: role} }
	personal := func(role string) MoveSide { return MoveSide{Role: role, Personal: true} }
	cases := []struct {
		name                       string
		from, to                   MoveSide
		service, padmin, staff, ok bool
		code                       int
	}{
		{"owner of both", shared(RoleOwner), shared(RoleOwner), false, false, false, true, 0},
		{"admin of both", shared(RoleAdmin), shared(RoleAdmin), false, false, false, true, 0},
		{"admin here, editor there", shared(RoleAdmin), shared(RoleEditor), false, false, false, false, 403},
		{"editor here, owner there", shared(RoleEditor), shared(RoleOwner), false, false, false, false, 403},
		{"not on the target", shared(RoleOwner), shared(""), false, false, false, false, 404},
		{"a service account", shared(RoleEditor), shared(RoleEditor), true, false, false, false, 403},
		{"into one's own workspace", shared(RoleAdmin), personal(RoleOwner), false, false, false, true, 0},
		{"out of one's own workspace", personal(RoleOwner), shared(RoleAdmin), false, false, false, true, 0},
		{"out of one's own workspace into a team as member", personal(RoleOwner), shared(RoleMember), false, false, false, false, 403},
		{"platform admin between teams they're not on", shared(""), shared(""), false, true, true, true, 0},
		{"platform admin into a team they own", shared(""), shared(RoleOwner), false, true, true, false, 403},
		{"platform admin into a team they're a member of", shared(""), shared(RoleMember), false, true, true, false, 403},
		{"platform admin out of a team they own", shared(RoleOwner), shared(""), false, true, true, true, 0},
		{"platform admin out of someone's workspace", personal(""), shared(""), false, true, true, false, 403},
		{"platform admin into someone's workspace", shared(""), personal(""), false, true, true, false, 403},
		{"platform auditor", shared(""), shared(""), false, false, true, false, 403},
	}
	for _, c := range cases {
		err := CheckAgentMove(c.from, c.to, c.service, c.padmin, c.staff)
		if (err == nil) != c.ok {
			t.Errorf("%s: %v, want ok=%v", c.name, err, c.ok)
			continue
		}
		if err != nil && apperrStatus(err) != c.code {
			t.Errorf("%s: %v, want %d", c.name, err, c.code)
		}
	}
}
