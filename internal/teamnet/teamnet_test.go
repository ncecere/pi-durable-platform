package teamnet

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func TestCheckNetworks(t *testing.T) {
	got, err := CheckNetworks([]string{" 10.20.1.2/16 ", "10.20.0.0/16", "192.168.0.0/24", "fd00:1::/48", "100.64.0.0/10"})
	if err != nil || !slices.Equal(got, []string{"10.20.0.0/16", "192.168.0.0/24", "fd00:1::/48", "100.64.0.0/10"}) {
		t.Fatalf("CheckNetworks = %v, %v", got, err)
	}
	for _, bad := range []string{"127.0.0.0/8", "169.254.0.0/16", "0.0.0.0/0", "::1/128", "fe80::/10", "8.8.8.0/24", "10.0.0.1", "nope"} {
		if _, err := CheckNetworks([]string{bad}); err == nil {
			t.Errorf("CheckNetworks(%s) accepted", bad)
		}
	}
	if _, err := CheckNetworks(strings.Split(strings.Repeat("10.0.0.0/8,", MaxPrivateNetworks+1), ",")[:MaxPrivateNetworks+1]); err == nil {
		t.Error("too many networks accepted")
	}
}

func TestLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{"localhost": true, "LOCALHOST": true, "127.0.0.1": true, "::1": true,
		"10.0.0.1": false, "example.com": false, "localhost.example.com": false} {
		if LoopbackHost(host) != want {
			t.Errorf("LoopbackHost(%s) != %v", host, want)
		}
	}
}

// CheckURL: https only (http on loopback with the development allowance),
// on an address the guard allows with the networks read now.
func TestCheckURL(t *testing.T) {
	nets := []string{}
	g := New(func(context.Context) ([]string, error) { return nets, nil }, 0)
	table := map[string]string{"public.test": "93.184.216.34", "corp.test": "10.20.0.5", "meta.test": "169.254.169.254",
		"localhost": "127.0.0.1"}
	lookup := func(_ context.Context, host string) ([]netip.Addr, error) {
		a, ok := table[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		return []netip.Addr{netip.MustParseAddr(a)}, nil
	}
	g.Set(Config{Lookup: lookup})
	cases := []struct {
		url  string
		want error
	}{
		{"https://public.test/mcp", nil},
		{"http://public.test/mcp", ErrScheme},
		{"http://localhost/mcp", ErrScheme},
		{"https:///mcp", ErrScheme},
		{"https://corp.test/mcp", ErrNotAllowed},
		{"https://meta.test/mcp", ErrNotAllowed},
		{"https://127.0.0.1/mcp", ErrNotAllowed},
		{"https://nowhere.test/mcp", ErrUnresolved},
	}
	for _, c := range cases {
		if err := g.CheckURL(context.Background(), c.url); !errors.Is(err, c.want) && err != c.want {
			t.Errorf("CheckURL(%s) = %v, want %v", c.url, err, c.want)
		}
	}
	nets = []string{"10.20.0.0/16"}
	if err := g.CheckURL(context.Background(), "https://corp.test/mcp"); err != nil {
		t.Errorf("an allowed private network: %v", err)
	}
	g.Set(Config{DevAllowance: true, Lookup: lookup})
	if err := g.CheckURL(context.Background(), "http://localhost/mcp"); err != nil {
		t.Errorf("http on loopback with the development allowance: %v", err)
	}
	if err := g.CheckURL(context.Background(), "http://public.test/mcp"); !errors.Is(err, ErrScheme) {
		t.Errorf("http elsewhere with the development allowance: %v", err)
	}
	if g.Client().CheckRedirect == nil {
		t.Error("the guarded client follows redirects")
	}
}
