// Package netguard dials URLs that someone else chose (an OAuth client's
// metadata document, a team-owned model provider) without letting them
// reach the platform's own network. It is adapted from Open MCP Portal's
// internal/upstream and internal/netpolicy address guard.
//
// The rule: every address a host is or resolves to must be public. Private
// addresses (RFC 1918, CGNAT, IPv6 unique-local) are allowed only inside an
// Allow prefix. Loopback, link-local (including cloud metadata such as
// 169.254.169.254), unspecified, multicast and broadcast addresses, and the
// cloud metadata endpoints outside link-local, are never allowed, whatever
// Allow says. (Allow may come from settings read at dial time: the caller
// builds a Dialer per connection, as internal/llm does for team providers.) The host is resolved once per connection and the connection is
// pinned to the addresses that were checked, so a DNS answer can't change
// between the check and the dial (DNS rebinding). AllowPrivate (the
// development allowance) accepts every address.
//
// Callers must also refuse redirects (or check each hop) and must not use a
// proxy from the environment, which would make the dialer see the proxy
// instead of the real destination; Transport does the latter.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// ErrBlockedAddress: the host is (or resolves to) an address the guard
// refuses.
var ErrBlockedAddress = errors.New("netguard: address not allowed")

// Class is an address's class.
type Class int

const (
	// Blocked addresses are never allowed.
	Blocked Class = iota
	// Public addresses are always allowed.
	Public
	// Private addresses are allowed inside an Allow prefix.
	Private
)

func prefixes(ranges ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ranges))
	for i, r := range ranges {
		out[i] = netip.MustParsePrefix(r)
	}
	return out
}

// blockedNetworks are never allowed: loopback, link-local, unspecified,
// multicast, broadcast, "this network", and cloud metadata endpoints outside
// link-local (Azure's wire server, Alibaba's metadata service and AWS's IPv6
// IMDS).
var blockedNetworks = prefixes(
	"0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4", "255.255.255.255/32",
	"168.63.129.16/32", "100.100.100.200/32",
	"::/128", "::1/128", "fe80::/10", "ff00::/8", "fd00:ec2::254/128",
)

// Embedded-IPv4 forms: an address here carries an IPv4 address in its last
// 32 bits (IPv4-compatible, and the NAT64 well-known prefix), which is
// checked as well.
var embedsIPv4 = prefixes("::/96", "64:ff9b::/96")

// notPublic are ranges that are not public even though Go considers them
// global unicast: private, CGNAT, documentation, benchmarking, reserved, and
// IPv6 unique-local, special assignments, documentation, 6to4/Teredo and
// translation. Only allocated global unicast (2000::/3) counts as public
// IPv6.
var notPublic = prefixes(
	"10.0.0.0/8", "100.64.0.0/10", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16",
	"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
	"fc00::/7", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20",
)

var globalUnicast6 = netip.MustParsePrefix("2000::/3")

// Classify returns an address's class. IPv4-mapped IPv6 addresses are
// classified as their IPv4 address; an embedded IPv4 address that is not
// public makes the IPv6 address blocked.
func Classify(ip netip.Addr) Class {
	if !ip.IsValid() || ip.Zone() != "" {
		return Blocked
	}
	ip = ip.Unmap()
	if blockedAddr(ip) {
		return Blocked
	}
	if publicAddr(ip) {
		return Public
	}
	return Private
}

// PublicAddr reports whether ip is a public unicast address.
func PublicAddr(ip netip.Addr) bool { return Classify(ip) == Public }

func blockedAddr(ip netip.Addr) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, p := range blockedNetworks {
		if p.Contains(ip) {
			return true
		}
	}
	if ip.Is6() {
		for _, p := range embedsIPv4 {
			if p.Contains(ip) {
				b := ip.As16()
				if v4 := netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}); Classify(v4) != Public {
					return true
				}
			}
		}
	}
	return false
}

func publicAddr(ip netip.Addr) bool {
	if !ip.IsGlobalUnicast() || (ip.Is6() && !globalUnicast6.Contains(ip)) {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// Dialer dials only addresses the rule allows. The zero value allows public
// addresses only, resolving with the system resolver.
type Dialer struct {
	// AllowPrivate accepts every address: the development allowance
	// (DEV_AUTH on a loopback APP_URL). Never set it in production.
	AllowPrivate bool
	// Allow lists private networks that are allowed (internal services a
	// platform admin or the deployment allows). Blocked addresses stay
	// blocked even inside a prefix.
	Allow []netip.Prefix
	// Timeout bounds one connection attempt (0: 10 seconds).
	Timeout time.Duration

	// Lookup resolves a host (nil: the system resolver). Tests replace it.
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	// Connect dials an approved address (nil: net.Dialer). Tests replace it.
	Connect func(ctx context.Context, network, address string) (net.Conn, error)
}

// Allowed reports whether the rule allows ip.
func (d *Dialer) Allowed(ip netip.Addr) bool {
	if d.AllowPrivate {
		return true
	}
	switch Classify(ip) {
	case Public:
		return true
	case Private:
		ip = ip.Unmap()
		for _, p := range d.Allow {
			if p.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// DialContext resolves address's host once, checks every address it
// resolves to, and connects to the first that answers. A name that resolves
// to any refused address is refused as a whole (not dialled at its allowed
// addresses), so a split answer can't be used to probe. A refused address
// fails with ErrBlockedAddress.
func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := d.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	connect := d.Connect
	if connect == nil {
		timeout := d.Timeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		connect = (&net.Dialer{Timeout: timeout}).DialContext
	}
	var errs []error
	for _, ip := range ips {
		conn, err := connect(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("netguard: dial %s: %w", host, errors.Join(errs...))
}

// Check resolves host (a name or an IP address, without a port) the way
// DialContext does and reports whether it would be dialled: nil, an error
// wrapping ErrBlockedAddress, or a resolution error. It is for checking a
// URL when it is saved; DialContext checks again at every connection.
func (d *Dialer) Check(ctx context.Context, host string) error {
	_, err := d.resolve(ctx, host)
	return err
}

// resolve returns host's addresses if every one is allowed.
func (d *Dialer) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")); err == nil {
		ips = []netip.Addr{ip}
	} else {
		lookup := d.Lookup
		if lookup == nil {
			lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			}
		}
		if ips, err = lookup(ctx, host); err != nil {
			return nil, fmt.Errorf("netguard: resolve %s: %w", host, err)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("netguard: %s has no addresses", host)
	}
	for _, ip := range ips {
		if !d.Allowed(ip) {
			return nil, fmt.Errorf("%w: %s resolved to %s", ErrBlockedAddress, host, ip)
		}
	}
	return ips, nil
}

// Transport returns an HTTP transport that dials through d and never uses a
// proxy from the environment (the dialer must see the real destination).
// timeout bounds the TLS handshake and the wait for response headers. Set
// CheckRedirect on the client to refuse redirects.
func (d *Dialer) Transport(timeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
}
