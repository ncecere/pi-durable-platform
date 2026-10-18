// Package teamnet is how the platform reaches services a team chose: a
// team's own model providers (internal/llm) and MCP servers (internal/mcp).
//
// A team's URL is the team's choice, so every request to it goes through the
// address guard (internal/netguard): public addresses, private ones only
// inside the networks platform admins allow (model_settings.private_networks,
// the one list for every kind of team service, read at each connection),
// loopback, link-local and metadata addresses never; resolved and pinned at
// each connection; no redirects; no proxy from the environment. The URL must
// be https (plain http only on a loopback host with the development
// allowance), checked when it is saved (CheckURL) and again at every
// connection (DNS rebinding). Platform services keep their unguarded
// clients: platform admins may point them at internal services.
package teamnet

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/netguard"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Config is how the platform reaches teams' services.
type Config struct {
	// DevAllowance accepts every address, and plain http on a loopback host
	// (DEV_AUTH on a loopback APP_URL). Never in production.
	DevAllowance bool
	// Lookup resolves a host and Connect dials an approved address (nil: the
	// system's). Tests replace them; the guard's rule stays.
	Lookup  func(ctx context.Context, host string) ([]netip.Addr, error)
	Connect func(ctx context.Context, network, address string) (net.Conn, error)
	// TLS is the TLS configuration (nil: the system's; tests trust a test
	// server).
	TLS *tls.Config
}

// Errors of CheckURL; callers say which service they are about.
var (
	// ErrScheme: not https (nor http on a loopback host with the
	// development allowance), or no host.
	ErrScheme = errors.New("teamnet: https required")
	// ErrNotAllowed: the host is, or resolves to, an address teams can't use.
	ErrNotAllowed = errors.New("teamnet: address not allowed")
	// ErrUnresolved: the host name could not be resolved.
	ErrUnresolved = errors.New("teamnet: address unresolved")
)

// ErrBadNetworks refuses a private-networks list.
var ErrBadNetworks = apperr.Invalid("invalid_private_networks",
	"Private networks are at most 50 private CIDRs (such as 10.20.0.0/16); loopback, link-local and public ranges can't be listed")

// MaxPrivateNetworks bounds model_settings.private_networks.
const MaxPrivateNetworks = 50

// Networks returns the private networks platform admins allow now.
type Networks func(ctx context.Context) ([]string, error)

// SettingsNetworks reads model_settings.private_networks: the one list of
// private networks for every kind of team service.
func SettingsNetworks(q *dbgen.Queries) Networks {
	return func(ctx context.Context) ([]string, error) {
		settings, err := q.GetModelSettings(ctx)
		if err != nil {
			return nil, fmt.Errorf("model settings: %w", err)
		}
		return settings.PrivateNetworks, nil
	}
}

// Guard reaches teams' services through the address guard.
type Guard struct {
	networks              Networks
	responseHeaderTimeout time.Duration
	cur                   atomic.Pointer[state]
}

type state struct {
	cfg    Config
	client *http.Client
}

// New returns a Guard without the development allowance (see Set).
// responseHeaderTimeout bounds the wait for a reply's headers.
func New(networks Networks, responseHeaderTimeout time.Duration) *Guard {
	g := &Guard{networks: networks, responseHeaderTimeout: responseHeaderTimeout}
	g.Set(Config{})
	return g
}

// Set sets how teams' services are reached (at start-up; tests replace the
// resolver, dialer and TLS configuration).
func (g *Guard) Set(cfg Config) {
	st := &state{cfg: cfg}
	t := &http.Transport{
		Proxy: nil, // the guard must see the real destination
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d, err := g.dialer(ctx, cfg)
			if err != nil {
				return nil, err
			}
			return d.DialContext(ctx, network, addr)
		},
		TLSClientConfig:       cfg.TLS,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: g.responseHeaderTimeout,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   8,
		// Short, so a network removed from private_networks stops being
		// reached soon on every replica (this one closes them at once).
		IdleConnTimeout: 30 * time.Second,
	}
	st.client = &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if old := g.cur.Swap(st); old != nil {
		old.client.CloseIdleConnections()
	}
}

// Client is the guarded client.
func (g *Guard) Client() *http.Client { return g.cur.Load().client }

// CloseIdle closes kept connections (the private networks changed).
func (g *Guard) CloseIdle() { g.cur.Load().client.CloseIdleConnections() }

// dialer is the address guard with the private networks platform admins
// allow now.
func (g *Guard) dialer(ctx context.Context, cfg Config) (*netguard.Dialer, error) {
	raw, err := g.networks(ctx)
	if err != nil {
		return nil, err
	}
	return &netguard.Dialer{AllowPrivate: cfg.DevAllowance, Allow: ParseNetworks(raw),
		Timeout: 10 * time.Second, Lookup: cfg.Lookup, Connect: cfg.Connect}, nil
}

// CheckURL checks a team service's (normalised) URL: https (http only on a
// loopback host with the development allowance), on an address the guard
// allows now. It returns ErrScheme, ErrNotAllowed, ErrUnresolved, or
// another error (the settings could not be read).
func (g *Guard) CheckURL(ctx context.Context, raw string) error {
	cfg := g.cur.Load().cfg
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ErrScheme
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && cfg.DevAllowance && LoopbackHost(u.Hostname())) {
		return ErrScheme
	}
	d, err := g.dialer(ctx, cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	switch err := d.Check(ctx, u.Hostname()); {
	case errors.Is(err, netguard.ErrBlockedAddress):
		return ErrNotAllowed
	case err != nil:
		return ErrUnresolved
	}
	return nil
}

// ParseNetworks parses stored private networks (skipping any that don't
// parse).
func ParseNetworks(raw []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(raw))
	for _, r := range raw {
		if p, err := netip.ParsePrefix(r); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// CheckNetworks normalises private networks: CIDRs of private addresses
// (RFC 1918, CGNAT, unique-local), masked, each once. Ranges of blocked
// addresses (loopback, link-local …) or public ones are refused: the guard
// never allows the first and always allows the second.
func CheckNetworks(in []string) ([]string, error) {
	if len(in) > MaxPrivateNetworks {
		return nil, ErrBadNetworks
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for i, raw := range in {
		p, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || netguard.Classify(p.Masked().Addr()) != netguard.Private {
			e := *ErrBadNetworks
			e.Details = map[string]any{"index": i}
			return nil, &e
		}
		p = p.Masked()
		if !seen[p.String()] {
			seen[p.String()] = true
			out = append(out, p.String())
		}
	}
	return out, nil
}

// LoopbackHost reports whether host is localhost or a loopback address.
func LoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
