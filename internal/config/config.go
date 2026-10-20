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
	AppURL     string
	ListenAddr string // HTTP listener (LISTEN_ADDR)
	// MetricsAddr is where the Prometheus endpoint (/metrics, and nothing
	// else) listens, on a listener of its own (METRICS_ADDR). Empty: off.
	MetricsAddr string
	// Tracing is the optional OpenTelemetry export (OTEL_* settings).
	Tracing        Tracing
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

	// DevAuth turns on the development sign-in personas (loopback APP_URL
	// only).
	DevAuth    bool
	OIDC       OIDC
	SessionTTL time.Duration

	// EncryptionKey (32 bytes, base64) encrypts secrets stored in the
	// database (internal/secrets): model provider keys and headers, MCP
	// server headers, and each agent's database password and runtime token.
	// EncryptionKeyPrevious is the key being rotated away from: values sealed
	// with it still decrypt, until `pi-durable-platform rekey` has re-sealed
	// them all under EncryptionKey (`doctor` reports what is left).
	EncryptionKey         string
	EncryptionKeyPrevious string

	// APIKeyPepper (32 bytes, base64) keys the HMAC under which personal API
	// keys, service account keys, and OAuth Connect tokens and codes are
	// stored; the credentials themselves never are.
	// APIKeyPepperPrevious is the pepper being rotated away from: all of
	// them digested with it are still accepted (and re-digested on use).
	APIKeyPepper         string
	APIKeyPepperPrevious string

	// TrustedProxies are the ingress CIDRs allowed to set X-Forwarded-For.
	TrustedProxies []netip.Prefix

	// RequestsPerMinute limits each signed-in person's /v1 requests;
	// LoginAttemptsPerMinute limits sign-in attempts per client address.
	// Both are counted in PostgreSQL, shared by every replica.
	RequestsPerMinute      int
	LoginAttemptsPerMinute int
	// ShutdownDelay is how long the process stays up, failing readiness,
	// before it stops accepting requests; ShutdownTimeout is how long
	// in-flight requests then get to finish.
	ShutdownDelay   time.Duration
	ShutdownTimeout time.Duration

	// Triggers bounds the runs schedules and webhooks start (TRIGGERS_*;
	// docs/triggers.md).
	Triggers Triggers
	// Agents configures where agents keep their state and how their
	// runtimes are started.
	Agents Agents
}

// Tracing configures OpenTelemetry traces, which are off without an Endpoint.
type Tracing struct {
	// Endpoint is the OTLP/HTTP collector's base URL
	// (OTEL_EXPORTER_OTLP_ENDPOINT), for example http://collector:4318.
	Endpoint string
	// ServiceName is the service.name of the spans (OTEL_SERVICE_NAME).
	ServiceName string
	// SampleRatio is the share of new traces kept, 0 to 1
	// (OTEL_TRACES_SAMPLER_ARG).
	SampleRatio float64
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
	// given as SandboxActivationToken, or kept in the Secret named by
	// SandboxActivationSecret (in Namespace), which the platform creates if
	// missing; not both. Without a pool, each agent is its own Sandbox.
	SandboxWarmPool         string
	SandboxActivationToken  string
	SandboxActivationSecret string
	// GatewayAddr is where the platform listens for agents (the model
	// proxy); GatewayURL is how agents reach it.
	GatewayAddr string
	GatewayURL  string
	// ClientMCPHosts are the hosts apps may give agents MCP servers on
	// (comma-separated [scheme://]host[:port], "*." for subdomains; empty:
	// none), reached as ClientMCPMode says: proxy or direct.
	ClientMCPHosts string
	ClientMCPMode  string
	IdleTimeout    time.Duration
	// BackgroundMax is how long an agent's background work may keep it
	// awake: older work is aborted and the agent may hibernate
	// (docs/hibernation.md).
	BackgroundMax time.Duration
	// The platform's default model, for agents whose template version and
	// team choose none from the model catalog: demo (scripted, no key) or
	// openai-compatible. ModelReasoning marks the latter as a reasoning model.
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
	GroupsClaim          string
	RequireVerifiedEmail bool
	AllowedEmailDomains  []string
	// AllowedGroups: when set, only people in one of these IdP groups (the
	// GroupsClaim) may sign in; the bootstrap admin always may.
	AllowedGroups         []string
	BootstrapAdminSubject string
	// GroupRecheckInterval: with AllowedGroups, how often the IdP is asked
	// whether each recently active person is still in an allowed group (0
	// turns the re-check off: groups apply at sign-in only). It needs the
	// IdP to issue refresh tokens (scope offline_access).
	GroupRecheckInterval time.Duration
	// GroupRecheckGrace: how long people keep access while the IdP can't be
	// asked (unreachable, or a claim it should give is missing), counted
	// from the last answer.
	GroupRecheckGrace time.Duration
}

// Triggers are the platform's ceilings for triggered runs.
type Triggers struct {
	// Interval is how often each replica's trigger loop runs (one replica at
	// a time does the work): due schedules, deliveries, runs in progress and
	// callbacks.
	Interval time.Duration
	// MaxRunTime is the longest an agent's triggered run may take (an
	// agent's own limit may be lower); then it is stopped.
	MaxRunTime time.Duration
	// MaxConcurrentRuns is the most triggered runs an agent may have at once
	// (an agent's own limit may be lower; its default is 1).
	MaxConcurrentRuns int
	// PauseAfterFailures pauses a trigger after this many failed runs in a row.
	PauseAfterFailures int
	// AgentMax is how many schedules an agent may make itself (its schedule
	// tool; within the 50 triggers an agent may have).
	AgentMax int
	// AgentMinInterval is the shortest time between two fires of a recurring
	// schedule an agent makes itself.
	AgentMinInterval time.Duration
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
		Tracing:    Tracing{ServiceName: "pi-durable-platform", SampleRatio: 1},
		Instance:   Instance{Name: DefaultInstanceName, Theme: "neutral"},
		OIDC: OIDC{
			Scopes:               []string{"openid", "profile", "email"},
			EmailClaim:           "email",
			GroupsClaim:          "groups",
			RequireVerifiedEmail: true,
			GroupRecheckInterval: 15 * time.Minute,
			GroupRecheckGrace:    24 * time.Hour,
		},
		SessionTTL:             12 * time.Hour,
		RequestsPerMinute:      600,
		LoginAttemptsPerMinute: 20,
		ShutdownDelay:          5 * time.Second,
		ShutdownTimeout:        60 * time.Second,
		Triggers: Triggers{Interval: 10 * time.Second, MaxRunTime: 30 * time.Minute, MaxConcurrentRuns: 5, PauseAfterFailures: 5,
			AgentMax: 20, AgentMinInterval: 5 * time.Minute},
		Agents: Agents{
			Launcher:      LauncherLocal,
			RuntimeDir:    "runtime",
			IdleTimeout:   10 * time.Minute,
			BackgroundMax: 2 * time.Hour,
			ModelProvider: "demo",
			GatewayAddr:   "127.0.0.1:8099",
			GatewayURL:    "http://127.0.0.1:8099",
			ClientMCPMode: "proxy",
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
