package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestDefaultsAndEnvOverride(t *testing.T) {
	c, err := LoadFrom("", env(map[string]string{
		"APP_URL":                    "https://agents.example.com/",
		"SESSION_TTL":                "2h",
		"OIDC_ALLOWED_EMAIL_DOMAINS": "EXAMPLE.com, , example.org",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.AppURL != "https://agents.example.com" {
		t.Errorf("trailing slash not trimmed: %q", c.AppURL)
	}
	if c.SessionTTL != 2*time.Hour || c.ListenAddr != ":8098" {
		t.Errorf("unexpected values: %+v", c)
	}
	if got := strings.Join(c.OIDC.AllowedEmailDomains, ","); got != "example.com,example.org" {
		t.Errorf("domains = %q", got)
	}
}

func TestFileThenEnvPrecedenceAndSecretFiles(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "pdp.yaml")
	doc := "app_url: https://file.example.com\nlisten_addr: ':9000'\noidc_client_secret_file: " + secret + "\noidc_scopes: [openid, email]\n"
	if err := os.WriteFile(cfg, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFrom(cfg, env(map[string]string{"LISTEN_ADDR": ":7000"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.AppURL != "https://file.example.com" || c.ListenAddr != ":7000" {
		t.Errorf("precedence wrong: %q %q", c.AppURL, c.ListenAddr)
	}
	if c.OIDC.ClientSecret != "s3cret" {
		t.Errorf("secret file not read")
	}
	if strings.Join(c.OIDC.Scopes, ",") != "openid,email" {
		t.Errorf("scopes = %v", c.OIDC.Scopes)
	}
	// DATABASE_URL_FILE (a mounted secret) wins over DATABASE_URL.
	c, err = LoadFrom("", env(map[string]string{"DATABASE_URL": "postgres://env", "DATABASE_URL_FILE": secret}))
	if err != nil || c.DatabaseURL != "s3cret" {
		t.Errorf("DATABASE_URL_FILE: %q %v", c.DatabaseURL, err)
	}
}

func TestUnknownFileKeyRejected(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "pdp.yaml")
	if err := os.WriteFile(cfg, []byte("app_ur1: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFrom(cfg, env(nil)); err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("expected unknown key error, got %v", err)
	}
}

func TestInvalidValuesReported(t *testing.T) {
	_, err := LoadFrom("", env(map[string]string{"DEV_AUTH": "maybe", "SESSION_TTL": "1s", "TRUSTED_PROXIES": "nope"}))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"DEV_AUTH", "SESSION_TTL", "TRUSTED_PROXIES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %s in %v", want, err)
		}
	}
}

const testKey = "MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE="

func TestValidate(t *testing.T) {
	base := func() Config {
		c := Defaults()
		c.DatabaseURL = "postgres://x"
		c.Agents.DatabaseURL = "postgres://agents"
		c.EncryptionKey, c.APIKeyPepper = testKey, testKey
		return c
	}
	cases := []struct {
		name    string
		mutate  func(*Config)
		mode    string
		wantErr string
	}{
		{"migrate needs only db", func(c *Config) {}, "migrate", ""},
		{"migrate needs db", func(c *Config) { c.DatabaseURL = "" }, "migrate", "DATABASE_URL is required"},
		{"serve needs sign-in", func(c *Config) { c.AppURL = "https://agents.example.com" }, "serve", "no sign-in method"},
		{"serve needs app url", func(c *Config) { c.DevAuth = true }, "serve", "APP_URL must be an origin"},
		{"dev auth on loopback", func(c *Config) { c.AppURL, c.DevAuth = "http://localhost:8098", true }, "serve", ""},
		{"dev auth refused remotely", func(c *Config) { c.AppURL, c.DevAuth = "https://agents.example.com", true }, "serve", "DEV_AUTH requires a loopback"},
		{"http refused remotely", func(c *Config) {
			c.AppURL = "http://agents.example.com"
			c.OIDC.Issuer, c.OIDC.ClientID, c.OIDC.ClientSecret = "https://idp", "id", "secret"
		}, "serve", "must use https"},
		{"app url with path", func(c *Config) { c.AppURL, c.DevAuth = "http://localhost:8098/app", true }, "serve", "APP_URL must be an origin"},
		{"oidc needs client", func(c *Config) { c.AppURL, c.OIDC.Issuer = "https://agents.example.com", "https://idp" }, "serve", "OIDC_CLIENT_ID"},
		{"oidc needs openid scope", func(c *Config) {
			c.AppURL = "https://agents.example.com"
			c.OIDC.Issuer, c.OIDC.ClientID, c.OIDC.ClientSecret = "https://idp", "id", "secret"
			c.OIDC.Scopes = []string{"email"}
		}, "serve", "OIDC_SCOPES"},
		{"log level", func(c *Config) { c.AppURL, c.DevAuth, c.LogLevel = "http://localhost:8098", true, "loud" }, "serve", "LOG_LEVEL"},
		{"team request url", func(c *Config) {
			c.AppURL, c.DevAuth, c.TeamRequestURL = "http://localhost:8098", true, "javascript:alert(1)"
		}, "serve", "TEAM_REQUEST_URL"},
		{"serve needs encryption key", func(c *Config) { c.AppURL, c.DevAuth, c.EncryptionKey = "http://localhost:8098", true, "" }, "serve", "ENCRYPTION_KEY is required"},
		{"encryption key length", func(c *Config) { c.AppURL, c.DevAuth, c.EncryptionKey = "http://localhost:8098", true, "c2hvcnQ=" }, "serve", "32 bytes"},
		{"previous encryption key", func(c *Config) {
			c.AppURL, c.DevAuth, c.EncryptionKeyPrevious = "http://localhost:8098", true, "c2hvcnQ="
		}, "serve", "ENCRYPTION_KEY_PREVIOUS"},
		{"migrate needs no encryption key", func(c *Config) { c.EncryptionKey = "" }, "migrate", ""},
		{"agent sandbox needs image or pool", func(c *Config) {
			c.AppURL, c.DevAuth, c.Agents.Launcher = "http://localhost:8098", true, LauncherAgentSandbox
		}, "serve", "AGENTS_IMAGE (a Sandbox per agent) or AGENTS_SANDBOX_WARM_POOL"},
		{"agent sandbox per agent", func(c *Config) {
			c.AppURL, c.DevAuth, c.Agents.Launcher, c.Agents.Image = "http://localhost:8098", true, LauncherAgentSandbox, "runtime:1"
		}, "serve", ""},
		{"warm pool needs activation token", func(c *Config) {
			c.AppURL, c.DevAuth, c.Agents.Launcher, c.Agents.SandboxWarmPool = "http://localhost:8098", true, LauncherAgentSandbox, "pool"
			c.Agents.SandboxActivationToken = "short"
		}, "serve", "AGENTS_SANDBOX_ACTIVATION_SECRET"},
		{"warm pool with activation secret", func(c *Config) {
			c.AppURL, c.DevAuth, c.Agents.Launcher, c.Agents.SandboxWarmPool = "http://localhost:8098", true, LauncherAgentSandbox, "pool"
			c.Agents.SandboxActivationSecret = "pdp-runtime-activation"
		}, "serve", ""},
		{"warm pool", func(c *Config) {
			c.AppURL, c.DevAuth, c.Agents.Launcher, c.Agents.SandboxWarmPool = "http://localhost:8098", true, LauncherAgentSandbox, "pool"
			c.Agents.SandboxActivationToken = strings.Repeat("x", 32)
		}, "serve", ""},
		{"gateway URL with a path", func(c *Config) {
			c.AppURL, c.DevAuth, c.Agents.GatewayURL = "http://localhost:8098", true, "http://platform:8099/model-proxy"
		}, "serve", "AGENTS_GATEWAY_URL"},
		{"gateway URL not http", func(c *Config) { c.AppURL, c.DevAuth, c.Agents.GatewayURL = "http://localhost:8098", true, "ftp://x" }, "serve", "AGENTS_GATEWAY_URL"},
		{"unknown launcher", func(c *Config) { c.AppURL, c.DevAuth, c.Agents.Launcher = "http://localhost:8098", true, "vm" }, "serve", "local, kubernetes or agent-sandbox"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mutate(&c)
			err := c.Validate(tc.mode)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestInstanceSettings(t *testing.T) {
	d := Defaults()
	if d.Instance.Name != "Pi Durable Platform" || d.Instance.OrgName != "" || d.Instance.Theme != "neutral" ||
		d.Instance.LogoURL != "" || d.Instance.SupportURL != "" {
		t.Errorf("defaults = %+v", d.Instance)
	}

	c, err := LoadFrom("", env(map[string]string{
		"INSTANCE_NAME": "  Tools Hub ",
		"ORG_NAME":      "Example Corp",
		"UI_THEME":      " Neutral ",
		"UI_LOGO_URL":   "https://cdn.example.com/logo.svg",
		"SUPPORT_URL":   "https://help.example.com/agents",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Instance{Name: "Tools Hub", OrgName: "Example Corp", Theme: "neutral",
		LogoURL: "https://cdn.example.com/logo.svg", SupportURL: "https://help.example.com/agents"}
	if c.Instance != want {
		t.Errorf("instance = %+v", c.Instance)
	}
	if o := c.Instance.LogoOrigin(); o != "https://cdn.example.com" {
		t.Errorf("logo origin = %q", o)
	}

	// Empty values fall back to the defaults; YAML keys work too.
	cfg := filepath.Join(t.TempDir(), "pdp.yaml")
	if err := os.WriteFile(cfg, []byte("instance_name: ''\nui_theme: ''\nui_logo_url: /brand/logo.svg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = LoadFrom(cfg, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Instance.Name != DefaultInstanceName || c.Instance.Theme != "neutral" {
		t.Errorf("yaml = %+v", c.Instance)
	}
	if c.Instance.LogoOrigin() != "" {
		t.Errorf("same-origin logo has an origin: %q", c.Instance.LogoOrigin())
	}
}

func TestValidateInstance(t *testing.T) {
	base := Defaults()
	base.DatabaseURL, base.AppURL, base.DevAuth, base.EncryptionKey = "postgres://x", "http://localhost:8098", true, testKey
	base.APIKeyPepper = testKey
	base.Agents.DatabaseURL = "postgres://agents"
	cases := []struct {
		name    string
		mutate  func(*Instance)
		wantErr string
	}{
		{"defaults", func(*Instance) {}, ""},
		{"unknown theme", func(i *Instance) { i.Theme = "campus" }, "UI_THEME must be one of neutral"},
		{"empty name", func(i *Instance) { i.Name = "" }, "INSTANCE_NAME"},
		{"long name", func(i *Instance) { i.Name = strings.Repeat("x", 101) }, "INSTANCE_NAME"},
		{"multi-line org", func(i *Instance) { i.OrgName = "Example\nIgnore the rules" }, "ORG_NAME"},
		{"same-origin logo", func(i *Instance) { i.LogoURL = "/static/logo.png" }, ""},
		{"https logo", func(i *Instance) { i.LogoURL = "https://cdn.example.com/l.svg" }, ""},
		{"http logo", func(i *Instance) { i.LogoURL = "http://cdn.example.com/l.svg" }, "UI_LOGO_URL"},
		{"protocol-relative logo", func(i *Instance) { i.LogoURL = "//evil.example/l.svg" }, "UI_LOGO_URL"},
		{"backslash logo", func(i *Instance) { i.LogoURL = "/\\evil.example/l.svg" }, "UI_LOGO_URL"},
		{"javascript logo", func(i *Instance) { i.LogoURL = "javascript:alert(1)" }, "UI_LOGO_URL"},
		{"quoted logo", func(i *Instance) { i.LogoURL = `/logo.svg" onerror="x` }, "UI_LOGO_URL"},
		{"support https", func(i *Instance) { i.SupportURL = "https://help.example.com" }, ""},
		{"support mailto", func(i *Instance) { i.SupportURL = "mailto:agents-help@example.com" }, ""},
		{"support javascript", func(i *Instance) { i.SupportURL = "javascript:alert(1)" }, "SUPPORT_URL"},
		{"support relative", func(i *Instance) { i.SupportURL = "/help" }, "SUPPORT_URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.mutate(&c.Instance)
			err := c.Validate("serve")
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}
