package authz

import "testing"

func TestTemplateRules(t *testing.T) {
	platform := TemplateSubject{Platform: true, Status: TemplatePublished}
	team := func(status, role string) TemplateSubject { return TemplateSubject{Status: status, Role: role} }
	admin := func(s TemplateSubject) TemplateSubject { s.IsPlatformAdmin, s.IsStaff = true, true; return s }
	auditor := func(s TemplateSubject) TemplateSubject { s.IsStaff = true; return s }
	service := func(s TemplateSubject) TemplateSubject { s.IsService = true; return s }
	cases := []struct {
		name                       string
		s                          TemplateSubject
		see, edit, publish, review bool
	}{
		{"platform template, a person", platform, true, false, false, false},
		{"platform template, platform admin", admin(platform), true, true, false, true},
		{"private, member", team(TemplatePrivate, RoleMember), true, false, false, false},
		{"private, editor", team(TemplatePrivate, RoleEditor), true, true, false, false},
		{"private, admin", team(TemplatePrivate, RoleAdmin), true, true, true, false},
		{"private, editor service account", service(team(TemplatePrivate, RoleEditor)), true, true, false, false},
		{"private, another team", team(TemplatePrivate, ""), false, false, false, false},
		{"private, auditor", auditor(team(TemplatePrivate, "")), true, false, false, false},
		{"pending, platform admin", admin(team(TemplatePending, "")), true, false, false, true},
		{"published, another team", team(TemplatePublished, ""), true, false, false, false},
		{"published, owner", team(TemplatePublished, RoleOwner), true, true, true, false},
	}
	for _, c := range cases {
		check := func(what string, got, want bool) {
			if got != want {
				t.Errorf("%s: %s = %v, want %v", c.name, what, got, want)
			}
		}
		check("see", CheckTemplateSee(c.s) == nil, c.see)
		check("edit", CheckTemplateEdit(c.s) == nil, c.edit)
		check("publish", CheckTemplatePublish(c.s) == nil, c.publish)
		check("review", CheckTemplateReview(c.s) == nil, c.review)
	}
	deploy := []struct {
		name string
		s    TemplateSubject
		own  bool
		want bool
	}{
		{"platform", platform, false, true},
		{"published, another team's", team(TemplatePublished, ""), false, true},
		{"private, own team", team(TemplatePrivate, RoleMember), true, true},
		{"private, another team's", team(TemplatePrivate, ""), false, false},
		{"pending, another team's", team(TemplatePending, ""), false, false},
		{"archived platform", TemplateSubject{Platform: true, Status: TemplatePublished, Archived: true}, false, false},
	}
	for _, c := range deploy {
		if got := CheckTemplateDeploy(c.s, c.own) == nil; got != c.want {
			t.Errorf("deploy %s = %v, want %v", c.name, got, c.want)
		}
	}
}
