package netguard

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClassify(t *testing.T) {
	for addr, want := range map[string]Class{
		"8.8.8.8": Public, "1.1.1.1": Public, "2606:4700::1111": Public, "::ffff:8.8.8.8": Public,
		"10.0.0.1": Private, "172.16.5.4": Private, "192.168.0.1": Private, "100.64.0.1": Private, "fc00::1": Private,
		"fd12:3456::1": Private, "192.0.2.1": Private, "2001:db8::1": Private, "::ffff:192.168.1.1": Private,
		"127.0.0.1": Blocked, "127.8.9.10": Blocked, "::1": Blocked, "::": Blocked, "0.0.0.0": Blocked,
		"169.254.169.254": Blocked, "fe80::1": Blocked, "224.0.0.1": Blocked, "ff02::1": Blocked, "255.255.255.255": Blocked,
		"168.63.129.16": Blocked, "100.100.100.200": Blocked, "fd00:ec2::254": Blocked, "::ffff:127.0.0.1": Blocked,
		"::ffff:169.254.169.254": Blocked, "64:ff9b::a9fe:a9fe": Blocked, "64:ff9b::a00:1": Blocked, "::7f00:1": Blocked,
	} {
		if got := Classify(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Classify(%s) = %v, want %v", addr, got, want)
		}
	}
	if Classify(netip.Addr{}) != Blocked || Classify(netip.MustParseAddr("fe80::1%eth0")) != Blocked {
		t.Error("invalid and zoned addresses must be blocked")
	}
}

func TestAllowed(t *testing.T) {
	d := &Dialer{Allow: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16"), netip.MustParsePrefix("0.0.0.0/0")}}
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "10.20.3.4": true, "::ffff:10.20.3.4": true, "192.168.1.1": true,
		// Blocked stays blocked whatever Allow says.
		"127.0.0.1": false, "169.254.169.254": false, "0.0.0.0": false,
	} {
		if got := d.Allowed(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Allowed(%s) = %v, want %v", addr, got, want)
		}
	}
	if (&Dialer{}).Allowed(netip.MustParseAddr("10.20.3.4")) {
		t.Error("private allowed without a prefix")
	}
	if !(&Dialer{AllowPrivate: true}).Allowed(netip.MustParseAddr("127.0.0.1")) {
		t.Error("the development allowance must accept loopback")
	}
}

// fakeDialer resolves names from table and sends every approved dial to
// target, recording the addresses it was asked to dial.
func fakeDialer(table map[string][]string, target string, dialed *[]string) *Dialer {
	return &Dialer{
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			addrs, ok := table[host]
			if !ok {
				return nil, errors.New("no such host")
			}
			out := make([]netip.Addr, len(addrs))
			for i, a := range addrs {
				out[i] = netip.MustParseAddr(a)
			}
			return out, nil
		},
		Connect: func(ctx context.Context, network, address string) (net.Conn, error) {
			*dialed = append(*dialed, address)
			return (&net.Dialer{}).DialContext(ctx, network, target)
		},
	}
}

func TestDialContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	t.Cleanup(srv.Close)
	table := map[string][]string{
		"public.example.com": {"8.8.8.8"},
		"rebind.example.com": {"8.8.8.8", "169.254.169.254"}, // one bad answer refuses the name
		"corp.example.com":   {"10.20.0.5"},
		"local.example.com":  {"127.0.0.1"},
		"empty.example.com":  {},
	}
	cases := []struct {
		addr    string
		allow   []netip.Prefix
		ok      bool
		blocked bool
	}{
		{"public.example.com:443", nil, true, false},
		{"8.8.4.4:443", nil, true, false},
		{"rebind.example.com:443", nil, false, true},
		{"corp.example.com:443", nil, false, true},
		{"corp.example.com:443", []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}, true, false},
		{"local.example.com:443", []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, false, true},
		{"127.0.0.1:443", nil, false, true},
		{"[::1]:443", nil, false, true},
		{"empty.example.com:443", nil, false, false},
		{"unknown.example.com:443", nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.addr, func(t *testing.T) {
			var dialed []string
			d := fakeDialer(table, srv.Listener.Addr().String(), &dialed)
			d.Allow = c.allow
			conn, err := d.DialContext(context.Background(), "tcp", c.addr)
			if conn != nil {
				conn.Close()
			}
			if c.ok != (err == nil) || errors.Is(err, ErrBlockedAddress) != c.blocked {
				t.Fatalf("DialContext(%s) = %v", c.addr, err)
			}
			if !c.ok && len(dialed) != 0 {
				t.Fatalf("a refused name was dialled: %v", dialed)
			}
			// Pinned: the connection goes to the checked address, not a new lookup.
			if c.ok && (len(dialed) != 1 || strings.HasPrefix(dialed[0], "[")) {
				t.Fatalf("dialled %v", dialed)
			}
		})
	}
	var dialed []string
	d := fakeDialer(table, srv.Listener.Addr().String(), &dialed)
	d.AllowPrivate = true
	if conn, err := d.DialContext(context.Background(), "tcp", "local.example.com:443"); err != nil {
		t.Fatalf("development allowance: %v", err)
	} else {
		conn.Close()
	}
}

// The transport dials through the guard and ignores proxies from the
// environment.
func TestTransport(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	var dialed []string
	d := fakeDialer(map[string][]string{"public.example.com": {"8.8.8.8"}, "local.example.com": {"127.0.0.1"}},
		srv.Listener.Addr().String(), &dialed)
	tr := d.Transport(0)
	if tr.Proxy != nil {
		t.Fatal("the transport uses a proxy")
	}
	client := &http.Client{Transport: tr}
	res, err := client.Get("http://public.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if hits.Load() != 1 || len(dialed) != 1 || dialed[0] != "8.8.8.8:80" {
		t.Fatalf("hits %d, dialled %v", hits.Load(), dialed)
	}
	if _, err := client.Get("http://local.example.com/"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("loopback fetched: %v", err)
	}
}
