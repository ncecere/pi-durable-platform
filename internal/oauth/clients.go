package oauth

import (
	"context"
	"errors"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Client kinds (oauth_grants.client_kind; the API's OAuthClientInfo.kind).
const (
	// KindMetadata: the client_id is an https URL whose metadata document
	// describes the client (Client ID Metadata Documents). Preferred: its
	// host vouches for the name and redirect URIs. Only these can be trusted.
	KindMetadata = "metadata"
	// KindRegistered: the client registered itself (RFC 7591). Anyone can
	// register any name, so the consent page says so; never trusted.
	KindRegistered = "registered"
)

// Limits of client metadata.
const (
	maxRedirectURIs = 10
	maxURILen       = 500
	maxNameLen      = 200
)

// Client is an OAuth client as the consent page and the checks see it.
type Client struct {
	ID   string
	Kind string
	Name string
	// URI is the client's home page (https only; empty when none).
	URI string
	// LogoURI is its logo (https only; empty when none).
	LogoURI      string
	RedirectURIs []string
}

// Host is the host that vouches for the client: a metadata document's host,
// or (unverified) a registered client's home page host.
func (c Client) Host() string {
	raw := c.URI
	if c.Kind == KindMetadata {
		raw = c.ID
	}
	return hostOf(raw)
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// ResolveClient finds a client by its client_id: an https URL is a metadata
// document (fetched, cached); anything else must be a registered client
// used within the last 30 days. Every failure is a page error.
func (s *Service) ResolveClient(ctx context.Context, clientID string) (Client, error) {
	if clientID == "" || len(clientID) > maxURILen {
		return Client{}, pageError("invalid_client", "This app isn't known: the request doesn't name one. Connect the app again.")
	}
	if strings.HasPrefix(clientID, "https://") || strings.HasPrefix(clientID, "http://") {
		return s.docs.get(ctx, clientID)
	}
	if !strings.HasPrefix(clientID, clientPrefix) {
		return Client{}, pageError("invalid_client", "This app isn't known: its client_id must be an https URL (a metadata document) or a registered client.")
	}
	row, err := s.q.GetOAuthClient(ctx, dbgen.GetOAuthClientParams{ClientID: clientID, UnusedSince: s.now().Add(-ClientUnusedTTL)})
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return Client{}, pageError("invalid_client", "This app isn't known, or its registration expired. Connect the app again.")
	} else if err != nil {
		return Client{}, err
	}
	return Client{ID: row.ClientID, Kind: KindRegistered, Name: row.ClientName, URI: deref(row.ClientUri), LogoURI: deref(row.LogoUri),
		RedirectURIs: row.RedirectUris}, nil
}

// loopbackHost reports a loopback redirect host (RFC 8252 §7.3).
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validRedirectURI accepts an https URL, or http on a loopback host (a
// native app's local listener); never a fragment or user info.
func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > maxURILen || u.Host == "" || u.Fragment != "" || u.User != nil || strings.Contains(raw, "#") {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		return loopbackHost(u.Hostname())
	}
	return false
}

// httpsURL returns raw when it is an https URL (display fields: a home
// page, a logo), else "".
func httpsURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(raw) > maxURILen {
		return ""
	}
	return u.String()
}

// matchRedirect finds the registered redirect URI a requested one matches:
// exactly, or for a loopback http URI with any port (RFC 8252 §7.3; the
// host, path and query must still match).
func matchRedirect(registered []string, requested string) bool {
	if slices.Contains(registered, requested) {
		return true
	}
	req, err := url.Parse(requested)
	if err != nil || req.Scheme != "http" || !loopbackHost(req.Hostname()) || req.Fragment != "" || req.User != nil {
		return false
	}
	for _, r := range registered {
		reg, err := url.Parse(r)
		if err != nil || reg.Scheme != "http" {
			continue
		}
		if strings.EqualFold(reg.Hostname(), req.Hostname()) && reg.EscapedPath() == req.EscapedPath() && reg.RawQuery == req.RawQuery {
			return true
		}
	}
	return false
}

// ClientMetadata is a registration request (RFC 7591 §2), and the fields
// read from a metadata document.
type ClientMetadata struct {
	ClientID                string   `json:"client_id,omitempty"`
	ClientName              string   `json:"client_name,omitempty"`
	ClientURI               string   `json:"client_uri,omitempty"`
	LogoURI                 string   `json:"logo_uri,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	Scope                   string   `json:"scope,omitempty"`
	// ApplicationType is "web" or "native" (OpenID Connect Dynamic Client
	// Registration; some clients send it when they register). It is validated and echoed in the registration response, and
	// otherwise ignored: the platform is not an OpenID Provider, so it doesn't
	// restrict redirect URIs by it (loopback http and https are both
	// accepted for either).
	ApplicationType string `json:"application_type,omitempty"`
}

// Application types of a registration.
const (
	ApplicationWeb    = "web"
	ApplicationNative = "native"
)

func invalidMetadata(msg string) *Error { return oauthError("invalid_client_metadata", msg) }

// check validates client metadata and returns the client it describes.
func (m ClientMetadata) check() (Client, *Error) {
	if n := len(m.RedirectURIs); n == 0 || n > maxRedirectURIs {
		return Client{}, oauthError("invalid_redirect_uri", "Give 1 to 10 redirect_uris.")
	}
	for _, r := range m.RedirectURIs {
		if !validRedirectURI(r) {
			return Client{}, oauthError("invalid_redirect_uri", "Every redirect URI must be https, or http on localhost or a loopback address, without a fragment.")
		}
	}
	if m.TokenEndpointAuthMethod != "" && m.TokenEndpointAuthMethod != "none" {
		return Client{}, invalidMetadata("Only public clients are supported: token_endpoint_auth_method must be none.")
	}
	for _, g := range m.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			return Client{}, invalidMetadata("Only the authorization_code and refresh_token grant types are supported.")
		}
	}
	for _, r := range m.ResponseTypes {
		if r != "code" {
			return Client{}, invalidMetadata("Only the code response type is supported.")
		}
	}

	name := strings.Join(strings.Fields(m.ClientName), " ")
	if name == "" {
		name = "Unnamed app"
	}
	if len([]rune(name)) > maxNameLen {
		name = string([]rune(name)[:maxNameLen])
	}
	return Client{Name: name, URI: httpsURL(m.ClientURI), LogoURI: httpsURL(m.LogoURI), RedirectURIs: m.RedirectURIs}, nil
}

// Registered is a new registration (RFC 7591 §3.2.1).
type Registered struct {
	Client
	IssuedAt int64
	// ApplicationType is what the client registered ("" if it sent none).
	ApplicationType string
}

// Register registers a public client (RFC 7591). Registrations unused for
// 30 days are deleted first. Audited as oauth.client_register by the
// system (no person is signed in), with the caller's address.
func (s *Service) Register(ctx context.Context, m ClientMetadata, requestID, clientIP string) (Registered, error) {
	c, bad := m.check()
	if bad != nil {
		return Registered{}, bad
	}
	if t := m.ApplicationType; t != "" && t != ApplicationWeb && t != ApplicationNative {
		return Registered{}, invalidMetadata(`application_type must be "web" or "native".`)
	}
	c.ID, c.Kind = newToken(clientPrefix), KindRegistered
	var out Registered
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		if _, err := q.DeleteUnusedOAuthClients(ctx, s.now().Add(-ClientUnusedTTL)); err != nil {
			return err
		}
		row, err := q.InsertOAuthClient(ctx, dbgen.InsertOAuthClientParams{
			ClientID: c.ID, ClientName: c.Name, RedirectUris: c.RedirectURIs, ClientUri: ptr(c.URI), LogoUri: ptr(c.LogoURI),
		})
		if err != nil {
			return err
		}
		out = Registered{Client: c, IssuedAt: row.CreatedAt.Unix(), ApplicationType: m.ApplicationType}
		return audit.Record(ctx, q, audit.Entry{
			ActorKind: audit.ActorSystem, Action: "oauth.client_register", TargetType: "oauth_client", TargetID: row.ID.String(),
			After:    registrationAudit(c, m.ApplicationType),
			Metadata: map[string]any{"clientId": c.ID, "name": c.Name}, RequestID: requestID, ClientIP: clientIP,
		})
	})
	return out, err
}

// registrationAudit is what an audit entry records of a registration.
func registrationAudit(c Client, applicationType string) map[string]any {
	after := map[string]any{"clientName": c.Name, "clientUri": c.URI, "redirectUris": c.RedirectURIs}
	if applicationType != "" {
		after["applicationType"] = applicationType
	}
	return after
}
