package authz

import "testing"

func TestAgentRules(t *testing.T) {
	personalOwner := AgentSubject{Kind: AgentPersonal, IsOwner: true}
	personalOther := AgentSubject{Kind: AgentPersonal}
	team := func(role string) AgentSubject { return AgentSubject{Kind: AgentTeam, Role: role} }
	cases := []struct {
		name                       string
		s                          AgentSubject
		use, edit, del, delAsAdmin bool
	}{
		{"personal owner", personalOwner, true, true, true, true},
		{"someone else's assistant", personalOther, false, false, false, true},
		{"team member", team(RoleMember), true, false, false, true},
		{"team editor", team(RoleEditor), true, true, false, true},
		{"team admin", team(RoleAdmin), true, true, true, true},
		{"team owner", team(RoleOwner), true, true, true, true},
		{"not a member", team(""), false, false, false, true},
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
}
