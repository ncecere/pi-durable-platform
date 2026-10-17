package auth

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/config"
)

func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"":                                 "/",
		"/admin/teams":                     "/admin/teams",
		"/teams/a?tab=members":             "/teams/a?tab=members",
		"//evil.example":                   "/",
		"/\\evil.example":                  "/",
		"https://evil.example":             "/",
		"relative":                         "/",
		"/x\r\nSet-Cookie: a":              "/",
		"/" + strings.Repeat("a", maxNext): "/",
	}
	for in, want := range cases {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruthyAndClaims(t *testing.T) {
	if !truthy(true) || !truthy("TRUE") || truthy("yes") || truthy(1) || truthy(nil) {
		t.Error("truthy wrong")
	}
	big := map[string]any{"sub": "x", "blob": strings.Repeat("a", maxClaimsSize)}
	if got := string(boundedClaims(big)); !strings.Contains(got, `"_truncated":true`) || strings.Contains(got, "blob") {
		t.Errorf("oversized claims kept: %.80s", got)
	}
}

func newTestService(cfg config.Config) *Service {
	return NewService(cfg, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestOriginAllowed(t *testing.T) {
	cfg := config.Defaults()
	cfg.AppURL, cfg.DevAuth = "http://localhost:8098", true
	s := newTestService(cfg)
	for origin, want := range map[string]bool{
		"":                                   true, // non-browser client: the CSRF token still applies
		"http://localhost:8098":              true,
		"http://localhost:" + ViteDevPort:    true,
		"http://127.0.0.1:" + ViteDevPort:    true,
		"http://localhost:5173":              false,
		"https://evil.example":               false,
		"http://localhost:8098.evil.example": false,
	} {
		r := httptest.NewRequest("POST", "/auth/logout", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if got := s.originAllowed(r); got != want {
			t.Errorf("origin %q allowed=%v want %v", origin, got, want)
		}
	}
	// The Vite origin is a development convenience only.
	cfg.AppURL, cfg.DevAuth = "https://agents.example.com", false
	s = newTestService(cfg)
	r := httptest.NewRequest("POST", "/auth/logout", nil)
	r.Header.Set("Origin", "http://localhost:"+ViteDevPort)
	if s.originAllowed(r) {
		t.Error("Vite origin allowed in production")
	}
}

func TestSessionCookieName(t *testing.T) {
	if SessionCookieName(true) != "__Host-pdp_session" || SessionCookieName(false) != "pdp_session" {
		t.Error("cookie names changed")
	}
	cfg := config.Defaults()
	cfg.AppURL = "https://agents.example.com"
	s := newTestService(cfg)
	w := httptest.NewRecorder()
	s.setSessionCookie(w, "secret")
	c := w.Result().Cookies()[0]
	if c.Name != "__Host-pdp_session" || !c.Secure || !c.HttpOnly || c.Path != "/" || c.SameSite != http.SameSiteLaxMode || c.Domain != "" {
		t.Errorf("cookie = %+v", c)
	}
}

func TestDevLoginRefusals(t *testing.T) {
	cfg := config.Defaults()
	cfg.AppURL, cfg.DevAuth = "http://localhost:8098", true
	s := newTestService(cfg)
	cases := []struct {
		name, host, origin string
		code               int
	}{
		{"other host", "evil.example", "", http.StatusForbidden},
		{"cross-site origin", "localhost:8098", "https://evil.example", http.StatusForbidden},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("POST", "/auth/dev", strings.NewReader(`{"account":"admin"}`))
		r.Host = tc.host
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		s.DevLogin(w, r)
		if w.Code != tc.code {
			t.Errorf("%s: %d %s", tc.name, w.Code, w.Body)
		}
	}
	cfg.DevAuth = false
	w := httptest.NewRecorder()
	newTestService(cfg).DevLogin(w, httptest.NewRequest("POST", "/auth/dev", strings.NewReader(`{}`)))
	if w.Code != http.StatusNotFound {
		t.Errorf("without DEV_AUTH: %d", w.Code)
	}
}

func TestRetryAfter(t *testing.T) {
	if retryAfter(1500*time.Millisecond) != "2" || retryAfter(0) != "1" {
		t.Error("retryAfter rounding")
	}
}
