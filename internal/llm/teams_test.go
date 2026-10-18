package llm

import (
	"slices"
	"strings"
	"testing"
)

func TestCheckNetworks(t *testing.T) {
	got, err := checkNetworks([]string{" 10.20.1.2/16 ", "10.20.0.0/16", "192.168.0.0/24", "fd00:1::/48", "100.64.0.0/10"})
	if err != nil || !slices.Equal(got, []string{"10.20.0.0/16", "192.168.0.0/24", "fd00:1::/48", "100.64.0.0/10"}) {
		t.Fatalf("checkNetworks = %v, %v", got, err)
	}
	for _, bad := range []string{"127.0.0.0/8", "169.254.0.0/16", "0.0.0.0/0", "::1/128", "fe80::/10", "8.8.8.0/24", "10.0.0.1", "nope"} {
		if _, err := checkNetworks([]string{bad}); err == nil {
			t.Errorf("checkNetworks(%s) accepted", bad)
		}
	}
	if _, err := checkNetworks(strings.Split(strings.Repeat("10.0.0.0/8,", maxPrivateNetworks+1), ",")[:maxPrivateNetworks+1]); err == nil {
		t.Error("too many networks accepted")
	}
}

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

func TestLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{"localhost": true, "LOCALHOST": true, "127.0.0.1": true, "::1": true,
		"10.0.0.1": false, "example.com": false, "localhost.example.com": false} {
		if loopbackHost(host) != want {
			t.Errorf("loopbackHost(%s) != %v", host, want)
		}
	}
}
