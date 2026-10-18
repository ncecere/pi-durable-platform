package authz

import (
	"errors"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
)

func TestTeamProviderRules(t *testing.T) {
	on := func(role string) TeamProviderSubject {
		return TeamProviderSubject{Role: role, Session: true, TeamsMayAdd: true}
	}
	with := func(s TeamProviderSubject, f func(*TeamProviderSubject)) TeamProviderSubject { f(&s); return s }
	cases := []struct {
		name                 string
		s                    TeamProviderSubject
		read, manage, create bool
		code                 string // create's refusal code, when it matters
	}{
		{"owner", on(RoleOwner), true, true, true, ""},
		{"admin", on(RoleAdmin), true, true, true, ""},
		{"editor", on(RoleEditor), true, false, false, "forbidden"},
		{"member", on(RoleMember), true, false, false, "forbidden"},
		{"another team", on(""), false, false, false, "model_provider_not_found"},
		{"platform admin, not on the team", with(on(""), func(s *TeamProviderSubject) { s.Staff = true }), true, false, false, "forbidden"},
		{"owner with an API key or app", with(on(RoleOwner), func(s *TeamProviderSubject) { s.Session = false }), true, false, false, "forbidden"},
		{"admin service account", with(on(RoleAdmin), func(s *TeamProviderSubject) { s.Session = false }), true, false, false, "forbidden"},
		{"owner, archived team", with(on(RoleOwner), func(s *TeamProviderSubject) { s.Archived = true }), true, false, false, "team_archived"},
		{"owner, teams may not add", with(on(RoleOwner), func(s *TeamProviderSubject) { s.TeamsMayAdd = false }), true, true, false, "team_providers_off"},
		{"personal owner, not allowed", with(on(RoleOwner), func(s *TeamProviderSubject) { s.Personal = true }), true, true, false, "personal_workspace"},
		{"personal owner, allowed", with(on(RoleOwner), func(s *TeamProviderSubject) { s.Personal, s.PersonalMayAdd = true, true }), true, true, true, ""},
		{"personal owner, allowed but teams may not add", with(on(RoleOwner), func(s *TeamProviderSubject) {
			s.Personal, s.PersonalMayAdd, s.TeamsMayAdd = true, true, false
		}), true, true, false, "team_providers_off"},
	}
	for _, c := range cases {
		check := func(what string, err error, want bool) {
			if (err == nil) != want {
				t.Errorf("%s: %s = %v, want allowed %v", c.name, what, err, want)
			}
		}
		check("read", CheckTeamProviderRead(c.s), c.read)
		check("manage", CheckTeamProviderManage(c.s), c.manage)
		err := CheckTeamProviderCreate(c.s)
		check("create", err, c.create)
		var ae *apperr.Error
		if c.code != "" && (!errors.As(err, &ae) || ae.Code != c.code) {
			t.Errorf("%s: create refused with %v, want %s", c.name, err, c.code)
		}
	}
}
