// Package config resolves runtime configuration.
//
// Precedence: built-in defaults, then the optional YAML file named by
// PDP_CONFIG_FILE, then environment variables. Secret-bearing settings can
// also be read from a file named by the matching *_FILE variable (or
// *_file YAML key), which is how Kubernetes Secrets are mounted.
//
// The YAML file is flat: keys are the lower-case environment variable names,
// for example `app_url: https://agents.example.com`. Unknown keys are rejected.
package config

import (
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	// AppURL is the public origin (scheme://host[:port]) used for cookies,
	// OIDC redirects and Origin checks.
	AppURL         string
	ListenAddr     string // HTTP listener (LISTEN_ADDR)
	DatabaseURL    string
	MigrateOnStart bool

	LogLevel  string
	LogFormat string

	// Instance is this deployment's identity: names, theme and links shown
	// in the UI.
	Instance Instance

	// TeamRequestURL links users to the team request form (for example a
	// service desk request). Optional.
	TeamRequestURL string

	DevAuth    bool
	OIDC       OIDC
	SessionTTL time.Duration

	// EncryptionKey (32 bytes, base64) encrypts secrets stored in the
	// database (internal/secrets). EncryptionKeyPrevious is
	// the key being rotated away from: values sealed with it still decrypt.
	EncryptionKey         string
	EncryptionKeyPrevious string

	// APIKeyPepper (32 bytes, base64) keys the HMAC under which personal
	// API keys are stored; the keys themselves never are.
	// APIKeyPepperPrevious is the pepper being rotated away from: keys and
	// service account keys digested with it are still accepted (keys are
	// re-digested on use).
	APIKeyPepper         string
	APIKeyPepperPrevious string

	// TrustedProxies are the ingress CIDRs allowed to set X-Forwarded-For.
	TrustedProxies []netip.Prefix

	// RequestsPerMinute limits each signed-in person's /v1 requests;
	// LoginAttemptsPerMinute limits sign-in attempts per client address.
	// Both are counted in PostgreSQL, shared by every replica.
	RequestsPerMinute      int
	LoginAttemptsPerMinute int
	ShutdownDelay          time.Duration
	ShutdownTimeout        time.Duration

	// Agents configures where agents keep their state and how their
	// runtimes are started.
	Agents Agents
}

// Agent launchers.
const (
	LauncherLocal      = "local"      // child processes on this host (development)
	LauncherKubernetes = "kubernetes" // one pod per agent
	// LauncherAgentSandbox runs agents as Kubernetes Agent Sandbox resources:
	// a Sandbox per agent (suspended when idle), or claims on a warm pool of
	// standby runtimes (AGENTS_SANDBOX_WARM_POOL).
	LauncherAgentSandbox = "agent-sandbox"
)

// Agents configures agent storage, runtimes and their model.
type Agents struct {
	// DatabaseURL connects to the agents database with a role that has
	// CREATEROLE and CREATE on it. Each agent gets its own schema and role
	// there; this role is never given to agents.
	DatabaseURL string
	// CreateDatabase creates the agents database through DATABASE_URL at
	// start-up if it is missing (development).
	CreateDatabase bool
	// RuntimeDatabaseHost overrides host:port in the URLs given to agent
	// runtimes, when they reach PostgreSQL differently from the platform.
	RuntimeDatabaseHost string
	Launcher            string
	// RuntimeDir is the runtime package for the local launcher.
	RuntimeDir string
	// Kubernetes launcher: the namespace for agent pods (default: the
	// platform's own), the runtime image, an optional sandboxing
	// RuntimeClass and image pull Secret.
	Namespace       string
	Image           string
	RuntimeClass    string
	ImagePullSecret string
	// Agent Sandbox launcher: claim runtimes from this SandboxWarmPool (in
	// Namespace) and activate them with the pool's RUNTIME_ACTIVATION_TOKEN:
	// given here, or kept in a Secret in Namespace that the platform creates
	// if missing. Without a pool, each agent is its own Sandbox.
	SandboxWarmPool         string
	SandboxActivationToken  string
	SandboxActivationSecret string
	// GatewayAddr is where the platform listens for agents (the model
	// proxy); GatewayURL is how agents reach it.
	GatewayAddr string
	GatewayURL  string
	IdleTimeout time.Duration
	// Model every agent uses: demo (scripted, no key) or openai-compatible.
	ModelProvider  string
	ModelBaseURL   string
	ModelAPIKey    string
	ModelID        string
	ModelReasoning bool
}

// Themes are the UI themes. Only the neutral theme ships; branding comes
// from INSTANCE_NAME, ORG_NAME and UI_LOGO_URL.
var Themes = []string{"neutral"}

// Instance is the identity of one deployment. Nothing in the product names
// an organisation; each install configures its own.
type Instance struct {
	Name       string // product name in the UI and page titles
	OrgName    string // optional organisation name
	Theme      string // one of Themes; applied as <html data-brand>
	LogoURL    string // optional https URL or same-origin path
	SupportURL string // optional help link (http(s) or mailto:)
}

// DefaultInstanceName is the product name used when INSTANCE_NAME is unset.
const DefaultInstanceName = "Pi Durable Platform"

type OIDC struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       []string
	EmailClaim   string
	// GroupsClaim names the claim holding the person's IdP groups; ""
	// stores none.
	GroupsClaim           string
	RequireVerifiedEmail  bool
	AllowedEmailDomains   []string
	BootstrapAdminSubject string
}

// Enabled reports whether an OIDC provider is configured.
func (o OIDC) Enabled() bool { return o.Issuer != "" }

// SecureCookies reports whether the app is served over HTTPS.
func (c Config) SecureCookies() bool { return strings.HasPrefix(c.AppURL, "https://") }

// Defaults returns the built-in configuration.
func Defaults() Config {
	return Config{
		ListenAddr: ":8098",
		LogLevel:   "info",
		LogFormat:  "json",
		Instance:   Instance{Name: DefaultInstanceName, Theme: "neutral"},
		OIDC: OIDC{
			Scopes:               []string{"openid", "profile", "email"},
			EmailClaim:           "email",
			GroupsClaim:          "groups",
			RequireVerifiedEmail: true,
		},
		SessionTTL:             12 * time.Hour,
		RequestsPerMinute:      600,
		LoginAttemptsPerMinute: 20,
		ShutdownDelay:          5 * time.Second,
		ShutdownTimeout:        60 * time.Second,
		Agents: Agents{
			Launcher:      LauncherLocal,
			RuntimeDir:    "runtime",
			IdleTimeout:   10 * time.Minute,
			ModelProvider: "demo",
			GatewayAddr:   "127.0.0.1:8099",
			GatewayURL:    "http://127.0.0.1:8099",
		},
	}
}

// LogoOrigin is the origin of an absolute UI_LOGO_URL (for the Content
// Security Policy's img-src), or "" for a same-origin path or no logo.
func (in Instance) LogoOrigin() string {
	if in.LogoURL == "" || strings.HasPrefix(in.LogoURL, "/") {
		return ""
	}
	u, err := url.Parse(in.LogoURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
