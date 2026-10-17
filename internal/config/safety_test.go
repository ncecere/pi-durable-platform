package config

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// exampleEnv reads the KEY=value lines of the repository's .env.example.
func exampleEnv(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			out[k] = v
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// The compiled-in hashes must match the keys .env.example ships with; if
// the example keys change, update exampleKeyHashes.
func TestExampleKeyHashesMatchEnvExample(t *testing.T) {
	ex := exampleEnv(t)
	for setting := range exampleKeyHashes {
		v := ex[setting]
		if v == "" {
			t.Fatalf(".env.example has no %s", setting)
		}
		if !isExampleKey(setting, v) {
			t.Errorf("exampleKeyHashes[%s] does not match .env.example; update it to the SHA-256 of the decoded key", setting)
		}
	}
}

func TestSafetyChecks(t *testing.T) {
	ex := exampleEnv(t)
	base := func() Config {
		c := Defaults()
		c.DatabaseURL = "postgres://x"
		c.EncryptionKey, c.APIKeyPepper = testKey, testKey
		c.AppURL = "https://agents.example.com"
		c.OIDC.Issuer, c.OIDC.ClientID, c.OIDC.ClientSecret = "https://idp.example.com", "id", "secret"
		return c
	}
	cases := []struct {
		name   string
		mutate func(*Config)
		mode   string
		want   []string // substrings; none means valid
	}{
		{"production ok", func(*Config) {}, "serve", nil},
		{"example encryption key", func(c *Config) { c.EncryptionKey = ex["ENCRYPTION_KEY"] }, "serve",
			[]string{"ENCRYPTION_KEY is the public example value", "openssl rand -base64 32"}},
		{"example key in another encoding", func(c *Config) { c.EncryptionKey = strings.TrimRight(ex["ENCRYPTION_KEY"], "=") }, "serve",
			[]string{"ENCRYPTION_KEY is the public example value"}},
		{"example api key pepper", func(c *Config) { c.APIKeyPepper = ex["API_KEY_PEPPER"] }, "serve",
			[]string{"API_KEY_PEPPER is the public example value"}},
		{"missing api key pepper", func(c *Config) { c.APIKeyPepper = "" }, "serve", []string{"API_KEY_PEPPER is required"}},
		{"example key as previous is fine", func(c *Config) { c.EncryptionKeyPrevious = ex["ENCRYPTION_KEY"] }, "serve", nil},
		{"example pepper as previous", func(c *Config) { c.APIKeyPepperPrevious = ex["API_KEY_PEPPER"] }, "serve",
			[]string{"API_KEY_PEPPER_PREVIOUS is the public example value"}},
		{"previous pepper equal to the current one", func(c *Config) { c.APIKeyPepperPrevious = c.APIKeyPepper }, "serve",
			[]string{"API_KEY_PEPPER_PREVIOUS must differ"}},
		{"invalid previous pepper", func(c *Config) { c.APIKeyPepperPrevious = "short" }, "serve", []string{"API_KEY_PEPPER_PREVIOUS"}},
		{"migrate example key", func(c *Config) { c.EncryptionKey = ex["ENCRYPTION_KEY"] }, "migrate", []string{"ENCRYPTION_KEY"}},
		{"example key on loopback", func(c *Config) {
			c.AppURL, c.DevAuth, c.EncryptionKey = "http://localhost:8098", true, ex["ENCRYPTION_KEY"]
		}, "serve", nil},
		{"dev auth", func(c *Config) { c.DevAuth = true }, "serve", []string{"DEV_AUTH requires a loopback APP_URL", "OIDC_ISSUER"}},
		{"http app url", func(c *Config) { c.AppURL = "http://agents.example.com" }, "serve", []string{"APP_URL must use https", "APP_URL=https://agents.example.com"}},
		{"migrate checks a set app url", func(c *Config) { c.DevAuth = true }, "migrate", []string{"DEV_AUTH"}},
		{"migrate without app url", func(c *Config) { c.AppURL, c.DevAuth = "", true }, "migrate", nil},
		{"loopback dev install", func(c *Config) { c.AppURL, c.DevAuth = "http://localhost:8098", true }, "serve", nil},
		{"loopback ip", func(c *Config) { c.AppURL, c.DevAuth = "http://127.0.0.1:8098", true }, "serve", nil},
		{"ipv6 loopback", func(c *Config) { c.AppURL, c.DevAuth = "http://[::1]:8098", true }, "serve", nil},
		{"lookalike host", func(c *Config) { c.AppURL, c.DevAuth = "http://localhost.example.com", true }, "serve",
			[]string{"DEV_AUTH", "must use https"}},
		{"all at once", func(c *Config) {
			c.AppURL, c.DevAuth, c.EncryptionKey = "http://pdp.example.com", true, ex["ENCRYPTION_KEY"]
		}, "serve", []string{"ENCRYPTION_KEY", "DEV_AUTH", "must use https"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mutate(&c)
			err := c.Validate(tc.mode)
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want %q, got nil", tc.want)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("missing %q in %v", w, err)
				}
			}
		})
	}
}

// The development .env.example itself must start (APP_URL is loopback).
func TestEnvExampleIsAllowedLocally(t *testing.T) {
	ex := exampleEnv(t)
	c, err := LoadFrom("", func(k string) (string, bool) { v, ok := ex[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate("serve"); err != nil {
		t.Fatalf(".env.example refused: %v", err)
	}
	if !c.LoopbackAppURL() || !c.DevAuth {
		t.Fatal(".env.example APP_URL is not loopback or DEV_AUTH is off")
	}
	if c.AppURL != "http://localhost:8098" || c.ListenAddr != "127.0.0.1:8098" {
		t.Fatalf(".env.example ports: %s %s", c.AppURL, c.ListenAddr)
	}
}
