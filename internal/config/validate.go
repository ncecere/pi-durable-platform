// Validation of a loaded configuration per process mode.

package config

import (
	"errors"

	"fmt"
	"github.com/ncecere/pi-durable-platform/internal/mcp"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ncecere/pi-durable-platform/internal/secrets"
)

// Validate checks the configuration needed by a process mode ("serve" or
// "migrate"). It includes the production safety checks (SafetyErrors).
func (c Config) Validate(mode string) error {
	errs := c.SafetyErrors(mode)
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if mode == "migrate" {
		return errors.Join(errs...)
	}
	errs = append(errs, c.validateProcess()...)
	errs = append(errs, c.validateSecrets()...)
	errs = append(errs, c.Instance.validate()...)
	errs = append(errs, c.validateWeb()...)
	errs = append(errs, c.validateAgents()...)
	return errors.Join(errs...)
}

// validateAgents checks agent storage, the launcher and the model.
func (c Config) validateAgents() []error {
	var errs []error
	a := c.Agents
	if a.DatabaseURL == "" {
		errs = append(errs, errors.New("AGENTS_DATABASE_URL is required: a database for agent state, separate from DATABASE_URL's"))
	}
	switch a.Launcher {
	case LauncherLocal:
		if a.RuntimeDir == "" {
			errs = append(errs, errors.New("AGENTS_RUNTIME_DIR is required with AGENTS_LAUNCHER=local"))
		}
	case LauncherKubernetes:
		if a.Image == "" {
			errs = append(errs, errors.New("AGENTS_IMAGE is required with AGENTS_LAUNCHER=kubernetes"))
		}
	case LauncherAgentSandbox:
		if a.SandboxWarmPool == "" && a.Image == "" {
			errs = append(errs, errors.New("AGENTS_LAUNCHER=agent-sandbox needs AGENTS_IMAGE (a Sandbox per agent) or AGENTS_SANDBOX_WARM_POOL (claims on a warm pool)"))
		}
		switch {
		case a.SandboxWarmPool == "":
		case a.SandboxActivationToken != "" && a.SandboxActivationSecret != "":
			errs = append(errs, errors.New("set AGENTS_SANDBOX_ACTIVATION_TOKEN or AGENTS_SANDBOX_ACTIVATION_SECRET, not both"))
		case a.SandboxActivationSecret != "":
		case len(a.SandboxActivationToken) < 32:
			errs = append(errs, errors.New("AGENTS_SANDBOX_WARM_POOL needs the pool's RUNTIME_ACTIVATION_TOKEN: AGENTS_SANDBOX_ACTIVATION_SECRET "+
				"(a Secret the platform keeps it in) or AGENTS_SANDBOX_ACTIVATION_TOKEN (at least 32 characters)"))
		}
	default:
		errs = append(errs, errors.New("AGENTS_LAUNCHER must be local, kubernetes or agent-sandbox"))
	}
	if u, err := url.Parse(a.GatewayURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" && u.Path != "/" {
		errs = append(errs, errors.New("AGENTS_GATEWAY_URL must be an http(s) origin agents can reach (the platform's model proxy)"))
	}
	if a.ClientMCPMode != "proxy" && a.ClientMCPMode != "direct" {
		errs = append(errs, errors.New("AGENTS_CLIENT_MCP_MODE must be proxy or direct"))
	}
	if _, err := mcp.ParseHostPatterns(a.ClientMCPHosts); err != nil {
		errs = append(errs, fmt.Errorf("AGENTS_CLIENT_MCP_HOSTS: %w", err))
	}
	if a.GatewayAddr == "" {
		errs = append(errs, errors.New("AGENTS_GATEWAY_ADDR is required (where the platform listens for agents)"))
	}
	switch a.ModelProvider {
	case "demo":
	case "openai-compatible":
		if a.ModelBaseURL == "" || a.ModelAPIKey == "" || a.ModelID == "" {
			errs = append(errs, errors.New("AGENTS_MODEL_PROVIDER=openai-compatible needs AGENTS_MODEL_BASE_URL, AGENTS_MODEL_API_KEY and AGENTS_MODEL_ID"))
		}
	default:
		errs = append(errs, errors.New("AGENTS_MODEL_PROVIDER must be demo or openai-compatible"))
	}
	return errs
}

// validateProcess checks what every long-running mode needs: logging.
func (c Config) validateProcess() []error {
	var errs []error
	if !slices.Contains([]string{"debug", "info", "warn", "error"}, c.LogLevel) {
		errs = append(errs, errors.New("LOG_LEVEL must be debug, info, warn or error"))
	}
	if c.LogFormat != "json" && c.LogFormat != "text" {
		errs = append(errs, errors.New("LOG_FORMAT must be json or text"))
	}
	if c.ListenAddr == "" {
		errs = append(errs, errors.New("LISTEN_ADDR must not be empty"))
	}
	if c.MetricsAddr != "" {
		if _, _, err := net.SplitHostPort(c.MetricsAddr); err != nil {
			errs = append(errs, errors.New("METRICS_ADDR must be host:port or :port (empty: no metrics endpoint)"))
		}
		for name, other := range map[string]string{"LISTEN_ADDR": c.ListenAddr, "AGENTS_GATEWAY_ADDR": c.Agents.GatewayAddr} {
			if sameListener(c.MetricsAddr, other) {
				errs = append(errs, fmt.Errorf("METRICS_ADDR must be a listener of its own, not %s's", name))
			}
		}
	}
	if e := c.Tracing.Endpoint; e != "" {
		if u, err := url.Parse(e); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, errors.New("OTEL_EXPORTER_OTLP_ENDPOINT must be an http(s) URL, the OTLP/HTTP collector's base (for example http://collector:4318)"))
		}
	}
	return errs
}

// sameListener reports whether two listen addresses would bind the same
// port on an overlapping address.
func sameListener(a, b string) bool {
	ah, ap, err := net.SplitHostPort(a)
	bh, bp, err2 := net.SplitHostPort(b)
	if err != nil || err2 != nil || ap != bp {
		return false
	}
	wild := func(h string) bool { return h == "" || h == "0.0.0.0" || h == "::" }
	return ah == bh || wild(ah) || wild(bh)
}

// validateSecrets checks the encryption key and the API key pepper.
func (c Config) validateSecrets() []error {
	var errs []error
	if c.EncryptionKey == "" {
		errs = append(errs, errors.New("ENCRYPTION_KEY is required (generate one with `openssl rand -base64 32`)"))
	} else if _, err := secrets.ParseKey(c.EncryptionKey); err != nil {
		errs = append(errs, fmt.Errorf("ENCRYPTION_KEY: %w (generate one with `openssl rand -base64 32`)", err))
	}
	if c.EncryptionKeyPrevious != "" {
		if _, err := secrets.ParseKey(c.EncryptionKeyPrevious); err != nil {
			errs = append(errs, fmt.Errorf("ENCRYPTION_KEY_PREVIOUS: %w", err))
		}
	}
	if c.APIKeyPepper == "" {
		errs = append(errs, errors.New("API_KEY_PEPPER is required (generate one with `openssl rand -base64 32`)"))
	} else if _, err := secrets.ParseKey(c.APIKeyPepper); err != nil {
		errs = append(errs, fmt.Errorf("API_KEY_PEPPER: %w (generate one with `openssl rand -base64 32`)", err))
	}
	if c.APIKeyPepperPrevious != "" {
		prev, err := secrets.ParseKey(c.APIKeyPepperPrevious)
		cur, curErr := secrets.ParseKey(c.APIKeyPepper)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("API_KEY_PEPPER_PREVIOUS: %w", err))
		case curErr == nil && string(prev) == string(cur):
			errs = append(errs, errors.New("API_KEY_PEPPER_PREVIOUS must differ from API_KEY_PEPPER (remove it once the rotation is done)"))
		}
	}
	return errs
}

func (in Instance) validate() []error {
	var errs []error
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 100 || hasControl(in.Name) {
		errs = append(errs, errors.New("INSTANCE_NAME must be 1 to 100 characters on one line"))
	}
	if utf8.RuneCountInString(in.OrgName) > 200 || hasControl(in.OrgName) {
		errs = append(errs, errors.New("ORG_NAME must be at most 200 characters on one line"))
	}
	if !slices.Contains(Themes, in.Theme) {
		errs = append(errs, fmt.Errorf("UI_THEME must be one of %s", strings.Join(Themes, ", ")))
	}
	if in.LogoURL != "" && !validLogoURL(in.LogoURL) {
		errs = append(errs, errors.New("UI_LOGO_URL must be an https URL or a same-origin path such as /logo.svg"))
	}
	if in.SupportURL != "" {
		u, err := url.Parse(in.SupportURL)
		ok := err == nil && !hasControl(in.SupportURL) && u.User == nil &&
			(((u.Scheme == "https" || u.Scheme == "http") && u.Host != "") || (u.Scheme == "mailto" && u.Opaque != ""))
		if !ok {
			errs = append(errs, errors.New("SUPPORT_URL must be an http(s) URL or a mailto: address"))
		}
	}
	return errs
}

// validLogoURL accepts https://host/... or a same-origin absolute path
// ("/logo.svg", not "//host" or "/\host", which browsers treat as hosts).
func validLogoURL(s string) bool {
	if hasControl(s) || strings.ContainsAny(s, " \\\"'<>") {
		return false
	}
	if strings.HasPrefix(s, "/") {
		return !strings.HasPrefix(s, "//")
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// validateWeb checks what serving the web app and API needs: the public
// origin and a sign-in method.
func (c Config) validateWeb() []error {
	var errs []error
	u, err := url.Parse(c.AppURL)
	if c.AppURL == "" || err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return append(errs, errors.New("APP_URL must be an origin such as https://agents.example.com"))
	}
	// https and DEV_AUTH on a reachable host: SafetyErrors.
	if c.TeamRequestURL != "" {
		if t, err := url.Parse(c.TeamRequestURL); err != nil || (t.Scheme != "https" && t.Scheme != "http") || t.Host == "" {
			errs = append(errs, errors.New("TEAM_REQUEST_URL must be an http(s) URL"))
		}
	}
	return append(errs, c.validateSignIn()...)
}

// validateSignIn checks that a sign-in method is configured, and OIDC's
// settings when it is enabled.
func (c Config) validateSignIn() []error {
	var errs []error
	if !c.DevAuth && !c.OIDC.Enabled() {
		errs = append(errs, errors.New("no sign-in method: set OIDC_ISSUER (or DEV_AUTH=true for local development)"))
	}
	if c.OIDC.Enabled() {
		if c.OIDC.ClientID == "" || c.OIDC.ClientSecret == "" {
			errs = append(errs, errors.New("OIDC_CLIENT_ID and OIDC_CLIENT_SECRET are required with OIDC_ISSUER"))
		}
		if !slices.Contains(c.OIDC.Scopes, "openid") {
			errs = append(errs, errors.New("OIDC_SCOPES must include openid"))
		}
		if c.OIDC.EmailClaim == "" {
			errs = append(errs, errors.New("OIDC_EMAIL_CLAIM must not be empty"))
		}
		if len(c.OIDC.AllowedGroups) > 0 && c.OIDC.GroupsClaim == "" {
			errs = append(errs, errors.New("OIDC_ALLOWED_GROUPS needs OIDC_GROUPS_CLAIM (the claim holding people's groups)"))
		}
		if i := c.OIDC.GroupRecheckInterval; i != 0 && i < time.Second {
			errs = append(errs, errors.New("OIDC_GROUP_RECHECK_INTERVAL must be 0 (off) or at least 1s"))
		} else if i > 0 && c.OIDC.GroupRecheckGrace < i {
			errs = append(errs, errors.New("OIDC_GROUP_RECHECK_GRACE must be at least OIDC_GROUP_RECHECK_INTERVAL"))
		}
	}
	return errs
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
