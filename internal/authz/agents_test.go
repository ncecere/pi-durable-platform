package authz

import "testing"

func TestAgentRules(t *testing.T) {
	team := func(role string) AgentSubject { return AgentSubject{Kind: AgentTeam, Role: role} }
	svc := func(role string, managed bool) AgentSubject {
		return AgentSubject{Kind: AgentTeam, Role: role, IsService: true, ServiceManaged: managed}
	}
	managedFor := func(role string) AgentSubject { return AgentSubject{Kind: AgentTeam, Role: role, ServiceManaged: true} }
	cases := []struct {
		name                       string
		s                          AgentSubject
		use, edit, del, delAsAdmin bool
	}{
		{"personal owner", AgentSubject{Kind: AgentPersonal, IsOwner: true}, true, true, true, true},
		{"someone else's assistant", AgentSubject{Kind: AgentPersonal}, false, false, false, true},
		{"a service account and a personal agent", AgentSubject{Kind: AgentPersonal, IsOwner: true, IsService: true}, false, false, false, true},
		{"team member", team(RoleMember), true, false, false, true},
		{"team editor", team(RoleEditor), true, true, false, true},
		{"team admin", team(RoleAdmin), true, true, true, true},
		{"team owner", team(RoleOwner), true, true, true, true},
		{"not a member", team(""), false, false, false, true},
		{"member service account", svc(RoleMember, false), true, false, false, true},
		{"editor service account", svc(RoleEditor, false), true, true, false, true},
		{"another team's service account", svc("", false), false, false, false, true},
		{"service-managed, member service account", svc(RoleMember, true), true, false, false, true},
		{"service-managed, editor service account", svc(RoleEditor, true), true, true, true, true},
		{"service-managed, team owner (a person)", managedFor(RoleOwner), false, false, true, true},
		{"service-managed, team editor (a person)", managedFor(RoleEditor), false, false, false, true},
		{"service-managed, not on the team", managedFor(""), false, false, false, true},
	}
	for _, c := range cases {
		if got := CheckAgentUse(c.s) == nil; got != c.use {
			t.Errorf("%s: use = %v, want %v", c.name, got, c.use)
		}
		if got := CheckAgentEdit(c.s) == nil; got != c.edit {
			t.Errorf("%s: edit = %v, want %v", c.name, got, c.edit)
		}
		if got := CheckAgentDelete(c.s, false) == nil; got != c.del {
			t.Errorf("%s: delete = %v, want %v", c.name, got, c.del)
		}
		if got := CheckAgentDelete(c.s, true) == nil; got != c.delAsAdmin {
			t.Errorf("%s: delete as platform admin = %v, want %v", c.name, got, c.delAsAdmin)
		}
	}
	if CheckTeamAgentCreate(RoleMember) == nil || CheckTeamAgentCreate(RoleEditor) != nil {
		t.Error("team agent creation: editors and above only")
	}
	if CheckServiceManagedCreate(false, RoleOwner) == nil || CheckServiceManagedCreate(true, RoleMember) == nil || CheckServiceManagedCreate(true, RoleEditor) != nil {
		t.Error("service-managed agents: editor service accounts only")
	}
}
