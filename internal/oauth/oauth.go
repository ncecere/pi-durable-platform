// Package oauth is OAuth Connect: the platform as a small OAuth 2.1
// authorization server, so an app (such as a chat app) can act as the person
// who connected it, with the platform's own sign-in (OIDC, or the
// development login) and the platform's own tokens. It works whatever the
// identity provider issues (opaque tokens, SAML behind a proxy) because apps
// never see the provider's tokens. Ported from Open MCP Portal's
// internal/oauth, without its gateways: the one protected resource is the
// platform itself (APP_URL).
//
// What it does, and nothing more:
//
//   - Clients identify themselves with a Client ID Metadata Document (an
//     https URL whose JSON document is fetched through the address guard,
//     internal/netguard; metadata.go) or register with Dynamic Client Registration
//     (RFC 7591, clients.go). All clients are public: no client secrets.
//   - The authorization code flow with PKCE S256 (required), an optional
//     resource indicator (RFC 8707) that must be APP_URL, one scope
//     (agents), and the iss parameter in authorization responses (RFC 9207),
//     authorize.go.
//   - Consent is a person's grant for a client, remembered until revoked.
//     Clients on the platform's trusted list skip the consent screen, never
//     the sign-in (trusted.go).
//   - Opaque access tokens (one hour) and rotating refresh tokens (30 days,
//     renewed on each use); presenting a rotated refresh token again after a
//     30-second grace revokes the grant. Tokens and codes are stored as
//     HMAC-SHA256 digests under API_KEY_PEPPER, like API keys (token.go).
//     An access token acts as its person with no platform role, and only on
//     the routes that take keys; every request re-checks the token, its
//     grant and its person.
//
// The consent page is a page of the web app (/connect/consent), behind its
// session and CSRF token.
package oauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/metrics"
	"github.com/ncecere/pi-durable-platform/internal/netguard"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Lifetimes.
const (
	// AccessTokenTTL is an access token's life: short, since a token is only
	// checked against its grant and its person when used.
	AccessTokenTTL = time.Hour
	// RefreshTokenTTL is a refresh token's life. Each use returns a new one
	// with a new 30 days, so an app used at least monthly stays connected
	// until the grant is revoked.
	RefreshTokenTTL = 30 * 24 * time.Hour
	// RefreshReuseGrace: a refresh token presented again this soon after its
	// rotation is a client refreshing twice at once, not a stolen token, so
	// it gets a new pair instead of revoking the grant.
	RefreshReuseGrace = 30 * time.Second
	// CodeTTL is an authorization code's life (single use).
	CodeTTL = time.Minute
	// ClientUnusedTTL: a registered client unused this long is deleted.
	ClientUnusedTTL = 30 * 24 * time.Hour
)

// Token prefixes: they tell tokens from API keys (pdp_, pdp_sa_) at a glance
// and in secret scanners. None starts with "pdp_", so a key check never
// mistakes one for a key.
const (
	AccessPrefix  = "pdpat_"
	RefreshPrefix = "pdprt_"
	codePrefix    = "pdpac_"
	clientPrefix  = "pdpcl_"
	// tokenBytes of randomness in every token and code (256 bits).
	tokenBytes = 32
)

// ScopeAgents is the one scope: act as the person on the agent routes (and
// read who they are and their teams).
const ScopeAgents = "agents"

// Scopes are the scopes the server supports (metadata, token responses).
var Scopes = []string{ScopeAgents}

// Token kinds (oauth_tokens.kind).
const (
	kindAccess  = "access"
	kindRefresh = "refresh"
)

// Config configures the service.
type Config struct {
	// Issuer is the authorization server's identifier: APP_URL. It is also
	// the protected resource.
	Issuer string
	// Pepper is API_KEY_PEPPER (decoded).
	Pepper []byte
	// PepperPrevious is API_KEY_PEPPER_PREVIOUS (decoded, nil when unset):
	// tokens and codes digested with it are still accepted. Access tokens
	// and codes expire soon; refresh tokens are replaced by their rotation.
	PepperPrevious []byte
	// DevAllowance lets metadata documents live on any address, and at a
	// plain http URL on a loopback host (DEV_AUTH on a loopback APP_URL).
	DevAllowance bool
}

// Service is the authorization server.
type Service struct {
	pool     *pgxpool.Pool
	q        *dbgen.Queries
	pepper   []byte
	previous []byte
	issuer   string
	docs     *metadataDocs
	logos    *logos
	Log      *slog.Logger
	// Metrics counts tokens issued and refused (nil: none).
	Metrics *metrics.Metrics
	now     func() time.Time
}

// New returns the service.
func New(pool *pgxpool.Pool, cfg Config) *Service {
	var q *dbgen.Queries
	if pool != nil {
		q = dbgen.New(pool)
	}
	s := &Service{
		pool: pool, q: q, pepper: cfg.Pepper, previous: cfg.PepperPrevious, issuer: strings.TrimRight(cfg.Issuer, "/"),
		docs:  newMetadataDocs(&netguard.Dialer{AllowPrivate: cfg.DevAllowance, Timeout: docTimeout}, cfg.DevAllowance),
		logos: newLogos(&netguard.Dialer{AllowPrivate: cfg.DevAllowance, Timeout: logoTimeout}),
		Log:   slog.Default(), now: time.Now,
	}
	s.docs.log = func() *slog.Logger { return s.Log }
	s.logos.log = func() *slog.Logger { return s.Log }
	return s
}

// Issuer is the authorization server's identifier (APP_URL).
func (s *Service) Issuer() string { return s.issuer }

// Resource is the protected resource: the platform, APP_URL.
func (s *Service) Resource() string { return s.issuer }

// newToken returns a random token with a prefix.
func newToken(prefix string) string {
	b := make([]byte, tokenBytes)
	_, _ = rand.Read(b) // crypto/rand never fails on supported platforms
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// validTokenShape reports whether a presented value can be one of ours, so
// junk never reaches the database.
func validTokenShape(token, prefix string) bool {
	body, ok := strings.CutPrefix(token, prefix)
	if !ok || len(body) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(body)
	return err == nil
}

// IsAccessToken reports whether a bearer value looks like one of our access
// tokens (rather than an API key).
func IsAccessToken(token string) bool { return validTokenShape(token, AccessPrefix) }

// LooksLikeToken reports whether a bearer value starts like one of our
// tokens (an access or refresh token, well-formed or not), for the
// WWW-Authenticate challenge of a refused one.
func LooksLikeToken(token string) bool {
	return strings.HasPrefix(token, AccessPrefix) || strings.HasPrefix(token, RefreshPrefix)
}

// digest is a token's stored form: hex HMAC-SHA256(pepper, "oauth:" +
// token). The label keeps it apart from API-key digests.
func (s *Service) digest(token string) string { return digestWith(s.pepper, token) }

// digests are a token's possible stored forms: with the current pepper,
// then the previous one.
func (s *Service) digests(token string) []string {
	out := []string{s.digest(token)}
	if s.previous != nil {
		out = append(out, digestWith(s.previous, token))
	}
	return out
}

// pepperID is the pepper_id stored with new token digests.
func (s *Service) pepperID() []byte {
	sum := sha256.Sum256(s.pepper)
	return sum[:8]
}

func digestWith(pepper []byte, token string) string {
	m := hmac.New(sha256.New, pepper)
	m.Write([]byte("oauth:"))
	m.Write([]byte(token))
	return hex.EncodeToString(m.Sum(nil))
}

// Error is an OAuth error (RFC 6749 §5.2, §4.1.2.1): its code, a
// description for people, and the HTTP status for the token endpoint.
type Error struct {
	Code        string
	Description string
	Status      int
	// page: the error comes before the client and its redirect URI are
	// known to be good, so it is shown as a page and never redirected.
	page bool
}

func (e *Error) Error() string { return e.Code + ": " + e.Description }

// ShowPage reports an authorization error that must be shown to the person
// rather than sent to the client's redirect URI (it could be anyone's).
func (e *Error) ShowPage() bool { return e.page }

// PageError is an error shown to the person as a page (the authorization
// endpoint's own checks, such as a suspended account).
func PageError(code, description string) *Error { return pageError(code, description) }

func oauthError(code, description string) *Error {
	return &Error{Code: code, Description: description, Status: http.StatusBadRequest}
}

func pageError(code, description string) *Error {
	e := oauthError(code, description)
	e.page = true
	return e
}

// errInvalidGrant is the token endpoint's answer to any bad code or
// refresh token: which check failed isn't told.
func errInvalidGrant() *Error {
	return oauthError("invalid_grant", "The authorization code or refresh token is invalid, expired or revoked.")
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
