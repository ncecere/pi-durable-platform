package mcp

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Client MCP servers: an app (for example a chat app whose users enable MCP
// servers) gives an agent servers for one thread with its message. The
// platform keeps them, headers sealed, until they expire; the agent reaches
// them through the gateway (proxy) or directly, as configured. Only hosts in
// the platform's allowlist (AGENTS_CLIENT_MCP_HOSTS) are accepted, and only for
// template versions that allow client servers.

// HostPattern is an allowed host: [scheme://]host[:port], where host may
// start with "*." (any subdomain) and port may be "*". The scheme defaults to
// https.
type HostPattern struct {
	Scheme, Host, Port string
	Wildcard           bool
}

// ParseHostPatterns reads a comma-separated allowlist.
func ParseHostPatterns(list string) ([]HostPattern, error) {
	var out []HostPattern
	for _, raw := range strings.Split(list, ",") {
		raw = strings.ToLower(strings.TrimSpace(raw))
		if raw == "" {
			continue
		}
		p := HostPattern{Scheme: "https"}
		if scheme, rest, ok := strings.Cut(raw, "://"); ok {
			if scheme != "http" && scheme != "https" {
				return nil, fmt.Errorf("client MCP host %q: scheme must be http or https", raw)
			}
			p.Scheme, raw = scheme, rest
		}
		if host, port, err := net.SplitHostPort(raw); err == nil {
			raw, p.Port = host, port
		}
		if rest, ok := strings.CutPrefix(raw, "*."); ok {
			p.Wildcard, raw = true, rest
		}
		if raw == "" || strings.ContainsAny(raw, "/*@ ") {
			return nil, fmt.Errorf("client MCP host %q is not a host name", raw)
		}
		p.Host = raw
		out = append(out, p)
	}
	return out, nil
}

func (p HostPattern) matches(u *url.URL) bool {
	if u.Scheme != p.Scheme {
		return false
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	if p.Port != "" && p.Port != "*" && p.Port != port {
		return false
	}
	if p.Port == "" && port != map[string]string{"http": "80", "https": "443"}[u.Scheme] {
		return false
	}
	if p.Wildcard {
		return strings.HasSuffix(host, "."+p.Host)
	}
	return host == p.Host
}

// Allowed reports whether apps may give agents an MCP server at u.
func (c ClientPolicy) Allowed(u *url.URL) bool {
	for _, p := range c.Hosts {
		if p.matches(u) {
			return true
		}
	}
	return false
}

// ClientServer is an MCP server an app gives an agent for a thread.
type ClientServer struct {
	Name    string
	URL     string
	Headers map[string]string
	// ExpiresIn defaults to a day; at most a week.
	ExpiresIn time.Duration
}

var (
	errClientMCPOff = apperr.Conflict("client_mcp_not_allowed", "This agent's template does not take MCP servers from apps")
	errClientHost   = apperr.Invalid("client_mcp_host_not_allowed", "That MCP server's host is not on the platform's allowlist")
	errTooMany      = apperr.Invalid("invalid_mcp_servers", "At most 10 MCP servers per thread, with distinct names")
)

// MaxClientServers bounds a thread's client servers.
const MaxClientServers = 10

// CheckClientServers validates servers an app gives a thread: allowed hosts,
// headers, names (their slugs must differ).
func (s *Service) CheckClientServers(servers []ClientServer) error {
	if len(servers) > MaxClientServers {
		return errTooMany
	}
	slugs := map[string]bool{}
	for i := range servers {
		cs := &servers[i]
		name, err := checkName(cs.Name)
		if err != nil {
			return err
		}
		cs.Name = name
		slug := SlugFrom(name)
		if slug == "" || slugs[slug] {
			return errTooMany
		}
		slugs[slug] = true
		u, err := checkURL(cs.URL)
		if err != nil {
			return err
		}
		if !s.Client.Allowed(u) {
			return errClientHost
		}
		cs.URL = u.String()
		if err := checkHeaders(cs.Headers); err != nil {
			return err
		}
		if cs.ExpiresIn <= 0 {
			cs.ExpiresIn = 24 * time.Hour
		}
		if cs.ExpiresIn > 7*24*time.Hour {
			cs.ExpiresIn = 7 * 24 * time.Hour
		}
	}
	return nil
}

// CheckThreadServers validates servers an app gives a thread: the agent's
// template version must allow them (clientMCP), and CheckClientServers.
func (s *Service) CheckThreadServers(clientMCP bool, servers []ClientServer) error {
	if len(servers) > 0 && !clientMCP {
		return errClientMCPOff
	}
	return s.CheckClientServers(servers)
}

// SetThreadServers replaces a thread's client servers (an empty list removes
// them). clientMCP is whether the agent's template version allows them; call
// it after authorizing the caller for the agent and after the thread's record
// exists (agent_threads: the servers go with it when it is deleted).
func (s *Service) SetThreadServers(ctx context.Context, agentID uuid.UUID, threadID string, clientMCP bool, servers []ClientServer) error {
	if err := s.CheckThreadServers(clientMCP, servers); err != nil {
		return err
	}
	return store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		keep := []string{}
		for _, cs := range servers {
			slug := SlugFrom(cs.Name)
			keep = append(keep, slug)
			// The row's ID is the associated data: seal after upserting with no headers, then set them.
			row, err := q.UpsertThreadMCPServer(ctx, dbgen.UpsertThreadMCPServerParams{AgentID: agentID, ThreadID: threadID,
				Name: cs.Name, Slug: slug, URL: cs.URL, ExpiresAt: time.Now().Add(cs.ExpiresIn)})
			if err != nil {
				return err
			}
			if len(cs.Headers) > 0 {
				enc, err := s.seal(cs.Headers, threadAD(row.ID))
				if err != nil {
					return err
				}
				if _, err := q.UpsertThreadMCPServer(ctx, dbgen.UpsertThreadMCPServerParams{AgentID: agentID, ThreadID: threadID,
					Name: cs.Name, Slug: slug, URL: cs.URL, HeadersEnc: enc, ExpiresAt: row.ExpiresAt}); err != nil {
					return err
				}
			}
		}
		return q.DeleteThreadMCPServersExcept(ctx, dbgen.DeleteThreadMCPServersExceptParams{AgentID: agentID, ThreadID: threadID, Keep: keep})
	})
}

// ThreadServers lists a thread's client servers that have not expired (all
// threads' with threadID ""), without their headers.
func (s *Service) ThreadServers(ctx context.Context, agentID uuid.UUID, threadID string) ([]dbgen.ThreadMcpServer, error) {
	var t *string
	if threadID != "" {
		t = &threadID
	}
	rows, err := s.q.ListThreadMCPServers(ctx, dbgen.ListThreadMCPServersParams{AgentID: agentID, ThreadID: t})
	for i := range rows {
		rows[i].HeadersEnc = nil
	}
	return rows, err
}

// PruneExpired removes client servers expired for more than a day.
func (s *Service) PruneExpired(ctx context.Context) (int64, error) {
	return s.q.DeleteExpiredThreadMCPServers(ctx)
}

// clientServerName bounds a name for logs.
func clientServerName(name string) string {
	if utf8.RuneCountInString(name) > 100 {
		return string([]rune(name)[:100])
	}
	return name
}
