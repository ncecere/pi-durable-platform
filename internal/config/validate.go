// Validation of a loaded configuration per process mode.

package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
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
	return errors.Join(errs...)
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
	return errs
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
