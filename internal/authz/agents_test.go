package authz

import "testing"

func TestAgentRules(t *testing.T) {
	person := func(role string, managed bool) AgentSubject { return AgentSubject{Role: role, ServiceManaged: managed} }
	svc := func(role string, managed bool) AgentSubject {
		return AgentSubject{Role: role, IsService: true, ServiceManaged: managed}
	}
	cases := []struct {
		name                            string
		s                               AgentSubject
		see, use, edit, del, delAsAdmin bool
	}{
		{"member", person(RoleMember, false), true, true, false, false, true},
		{"editor", person(RoleEditor, false), true, true, true, false, true},
		{"admin", person(RoleAdmin, false), true, true, true, true, true},
		{"owner", person(RoleOwner, false), true, true, true, true, true},
		{"not on the team", person("", false), false, false, false, false, true},
		{"member service account", svc(RoleMember, false), true, true, false, false, true},
		{"editor service account", svc(RoleEditor, false), true, true, true, false, true},
		{"another team's service account", svc("", false), false, false, false, false, true},
		{"service-managed: member service account", svc(RoleMember, true), true, true, false, false, true},
		{"service-managed: editor service account", svc(RoleEditor, true), true, true, true, true, true},
		{"service-managed: owner (a person)", person(RoleOwner, true), true, false, false, true, true},
		{"service-managed: admin (a person)", person(RoleAdmin, true), true, false, false, true, true},
		{"service-managed: editor (a person)", person(RoleEditor, true), false, false, false, false, true},
		{"service-managed: not on the team", person("", true), false, false, false, false, true},
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
	}
	if CheckTeamAgentCreate(RoleMember) == nil || CheckTeamAgentCreate(RoleEditor) != nil {
		t.Error("team agent creation: editors and above only")
	}
	if CheckServiceManagedCreate(false, RoleOwner) == nil || CheckServiceManagedCreate(true, RoleMember) == nil || CheckServiceManagedCreate(true, RoleEditor) != nil {
		t.Error("service-managed agents: editor service accounts only")
	}
}
