package authz

import (
	"errors"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
)

func TestTeamMCPRules(t *testing.T) {
	on := func(role string) TeamMCPSubject {
		return TeamMCPSubject{Role: role, Session: true, TeamsMayAdd: true}
	}
	with := func(s TeamMCPSubject, f func(*TeamMCPSubject)) TeamMCPSubject { f(&s); return s }
	cases := []struct {
		name                 string
		s                    TeamMCPSubject
		read, manage, create bool
		code                 string // create's refusal code, when it matters
	}{
		{"owner", on(RoleOwner), true, true, true, ""},
		{"admin", on(RoleAdmin), true, true, true, ""},
		{"editor", on(RoleEditor), true, false, false, "forbidden"},
		{"member", on(RoleMember), true, false, false, "forbidden"},
		{"another team", on(""), false, false, false, "mcp_server_not_found"},
		{"platform auditor, not on the team", with(on(""), func(s *TeamMCPSubject) { s.Staff = true }), true, false, false, "forbidden"},
		{"owner with an API key or app", with(on(RoleOwner), func(s *TeamMCPSubject) { s.Session = false }), true, false, false, "forbidden"},
		{"admin service account", with(on(RoleAdmin), func(s *TeamMCPSubject) { s.Session = false }), true, false, false, "forbidden"},
		{"owner, archived team", with(on(RoleOwner), func(s *TeamMCPSubject) { s.Archived = true }), true, false, false, "team_archived"},
		{"owner, teams may not add", with(on(RoleOwner), func(s *TeamMCPSubject) { s.TeamsMayAdd = false }), true, true, false, "team_mcp_servers_off"},
		{"personal owner, not allowed", with(on(RoleOwner), func(s *TeamMCPSubject) { s.Personal = true }), true, true, false, "personal_workspace"},
		{"personal owner, allowed", with(on(RoleOwner), func(s *TeamMCPSubject) { s.Personal, s.PersonalMayAdd = true, true }), true, true, true, ""},
		{"personal owner, allowed but teams may not add", with(on(RoleOwner), func(s *TeamMCPSubject) {
			s.Personal, s.PersonalMayAdd, s.TeamsMayAdd = true, true, false
		}), true, true, false, "team_mcp_servers_off"},
	}
	for _, c := range cases {
		check := func(what string, err error, want bool) {
			if (err == nil) != want {
				t.Errorf("%s: %s = %v, want allowed %v", c.name, what, err, want)
			}
		}
		check("read", CheckTeamMCPRead(c.s), c.read)
		check("manage", CheckTeamMCPManage(c.s), c.manage)
		err := CheckTeamMCPCreate(c.s)
		check("create", err, c.create)
		var ae *apperr.Error
		if c.code != "" && (!errors.As(err, &ae) || ae.Code != c.code) {
			t.Errorf("%s: create refused with %v, want %s", c.name, err, c.code)
		}
	}
}
