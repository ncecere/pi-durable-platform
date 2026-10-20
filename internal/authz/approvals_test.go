package authz

import "testing"

func TestWhoMayDecideAToolApproval(t *testing.T) {
	shared := func(role string, service, managed bool) ApprovalSubject {
		return ApprovalSubject{Agent: AgentSubject{Role: role, IsService: service, ServiceManaged: managed}, SeesThread: true}
	}
	cases := []struct {
		name   string
		s      ApprovalSubject
		see    bool
		decide bool
	}{
		{"the run's author, a member", func() ApprovalSubject { s := shared(RoleMember, false, false); s.RunAuthor = true; return s }(), true, true},
		{"an editor below the version's role", shared(RoleEditor, false, false), true, false},
		{"an editor at the version's role", func() ApprovalSubject { s := shared(RoleEditor, false, false); s.MinRole = RoleEditor; return s }(), true, true},
		{"a member when the version lets members decide", func() ApprovalSubject { s := shared(RoleMember, false, false); s.MinRole = RoleMember; return s }(), true, true},
		{"an admin", shared(RoleAdmin, false, false), true, true},
		{"an owner", shared(RoleOwner, false, false), true, true},
		{"an unattended run: members never, whatever the role", func() ApprovalSubject {
			s := shared(RoleMember, false, false)
			s.MinRole, s.Unattended = RoleMember, true
			return s
		}(), true, false},
		{"an unattended run: an admin", func() ApprovalSubject { s := shared(RoleAdmin, false, false); s.Unattended = true; return s }(), true, true},
		{"a private thread's creator", func() ApprovalSubject { s := shared(RoleMember, false, false); s.PrivateThread = true; return s }(), true, true},
		{"another's private thread, even the owner", ApprovalSubject{Agent: AgentSubject{Role: RoleOwner}, PrivateThread: true}, false, false},
		{"not on the team (staff)", ApprovalSubject{Agent: AgentSubject{Audit: true}, SeesThread: true}, false, false},
		{"a person on a service-managed agent", shared(RoleOwner, false, true), false, false},
		{"an editor service account of a service-managed agent", shared(RoleEditor, true, true), true, true},
		{"a member service account of a service-managed agent", shared(RoleMember, true, true), true, false},
		{"an editor service account of a team agent", shared(RoleEditor, true, false), true, false},
	}
	for _, c := range cases {
		if got := CheckApprovalSee(c.s) == nil; got != c.see {
			t.Errorf("%s: see = %v, want %v", c.name, got, c.see)
		}
		if got := CheckApprovalDecide(c.s) == nil; got != c.decide {
			t.Errorf("%s: decide = %v, want %v", c.name, got, c.decide)
		}
	}
}
