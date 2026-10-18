package llm

import "testing"

func TestCheckTeamModelScope(t *testing.T) {
	str := func(s string) *string { return &s }
	b := func(v bool) *bool { return &v }
	teams := func(s ...string) *[]string { return &s }
	cases := []struct {
		vis      *string
		teams    *[]string
		personal *bool
		ok       bool
	}{
		{nil, nil, nil, true},
		{str(""), teams(), b(false), true},
		{str("teams"), nil, nil, true},
		{str("all"), nil, nil, false},
		{nil, teams("other"), nil, false},
		{nil, nil, b(true), false},
	}
	for i, c := range cases {
		if got := checkTeamModelScope(c.vis, c.teams, c.personal) == nil; got != c.ok {
			t.Errorf("case %d: ok = %v, want %v", i, got, c.ok)
		}
	}
}
