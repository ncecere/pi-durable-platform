package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

const (
	stateCookie   = "pdp_oidc_state"
	loginStateTTL = 10 * time.Minute
	maxClaimsSize = 32 << 10
)

type oidcClient struct {
	provider *oidc.Provider
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// client performs OIDC discovery on first use and caches the result. Failed
// discovery is retried on the next sign-in rather than blocking startup.
func (s *Service) client(ctx context.Context) (*oidcClient, error) {
	s.oidcMu.Lock()
	defer s.oidcMu.Unlock()
	if s.oidc != nil {
		return s.oidc, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(ctx, s.cfg.OIDC.Issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery: %w", err)
	}
	s.oidc = &oidcClient{
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: s.cfg.OIDC.ClientID}),
		oauth: &oauth2.Config{
			ClientID:     s.cfg.OIDC.ClientID,
			ClientSecret: s.cfg.OIDC.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  s.cfg.AppURL + "/auth/callback",
			Scopes:       s.cfg.OIDC.Scopes,
		},
	}
	return s.oidc, nil
}

// Login starts the authorization-code flow with PKCE, state and nonce. The
// state is bound to this browser by a cookie; the verifier and nonce are kept
// server-side (login_states, by the state's digest) and used once.
func (s *Service) Login(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OIDC.Enabled() {
		httpx.Error(w, http.StatusNotFound, "oidc_disabled", "Single sign-on is not configured")
		return
	}
	if !s.admitLogin(w, r) {
		return
	}
	c, err := s.client(r.Context())
	if err != nil {
		s.log.ErrorContext(r.Context(), "oidc discovery failed", "err", err)
		httpx.Error(w, http.StatusServiceUnavailable, "idp_unavailable", "The sign-in provider is unavailable. Try again shortly.")
		return
	}
	state, nonce, verifier := httpx.NewID(32), httpx.NewID(32), oauth2.GenerateVerifier()
	if err := s.q.InsertLoginState(r.Context(), dbgen.InsertLoginStateParams{
		StateDigest: Digest(state), Verifier: verifier, Nonce: nonce,
		NextPath: safeNext(r.URL.Query().Get("next")), ExpiresAt: time.Now().Add(loginStateTTL),
	}); err != nil {
		s.log.ErrorContext(r.Context(), "store login state", "err", err)
		httpx.Error(w, http.StatusServiceUnavailable, "login_unavailable", "Sign-in is temporarily unavailable")
		return
	}
	s.setCookie(w, stateCookie, state, int(loginStateTTL.Seconds()))
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)}
	if hint := cleanLoginHint(r.URL.Query().Get("login_hint")); hint != "" {
		opts = append(opts, oauth2.SetAuthURLParam("login_hint", hint))
	}
	http.Redirect(w, r, c.oauth.AuthCodeURL(state, opts...), http.StatusFound)
}

// cleanLoginHint is a login_hint fit to pass to the identity provider: at
// most 320 characters on one line, else none.
func cleanLoginHint(h string) string {
	h = strings.TrimSpace(h)
	if len(h) > 320 || strings.IndexFunc(h, unicode.IsControl) >= 0 {
		return ""
	}
	return h
}

// Callback completes the authorization-code flow.
func (s *Service) Callback(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OIDC.Enabled() {
		httpx.Error(w, http.StatusNotFound, "oidc_disabled", "Single sign-on is not configured")
		return
	}
	saved, ok := s.takeState(w, r)
	if !ok {
		s.metrics.SignIn("oidc", "failure", "invalid")
		return
	}
	if r.URL.Query().Get("error") != "" {
		s.metrics.SignIn("oidc", "failure", "denied")
		httpx.Error(w, http.StatusUnauthorized, "login_declined", "The sign-in provider declined the request")
		return
	}
	c, err := s.client(r.Context())
	if err != nil {
		s.metrics.SignIn("oidc", "failure", "unavailable")
		httpx.Error(w, http.StatusServiceUnavailable, "idp_unavailable", "The sign-in provider is unavailable. Try again shortly.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	li, status, code, problem := s.verifyCode(ctx, c, r.URL.Query().Get("code"), saved)
	if problem != "" {
		s.metrics.SignIn("oidc", "failure", failureReason(status))
		httpx.Error(w, status, code, problem)
		return
	}
	if _, err := s.completeLogin(ctx, w, r, li); err != nil {
		if errors.Is(err, errSuspended) {
			s.metrics.SignIn("oidc", "failure", "denied")
			httpx.Error(w, http.StatusForbidden, "user_suspended", "This account is suspended")
			return
		}
		s.metrics.SignIn("oidc", "failure", "error")
		httpx.Internal(w, r, err)
		return
	}
	s.metrics.SignIn("oidc", "success", "")
	http.Redirect(w, r, saved.NextPath, http.StatusSeeOther)
}

// takeState checks the state parameter against this browser's state cookie
// and consumes the pending sign-in, writing a 400 when either fails.
func (s *Service) takeState(w http.ResponseWriter, r *http.Request) (dbgen.LoginState, bool) {
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie(stateCookie)
	if err != nil || len(state) < 32 || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		httpx.Error(w, http.StatusBadRequest, "invalid_state", "Sign-in expired or was started in another browser. Try again.")
		return dbgen.LoginState{}, false
	}
	s.setCookie(w, stateCookie, "", -1)
	saved, err := s.q.TakeLoginState(r.Context(), Digest(state))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid_state", "Sign-in expired. Try again.")
		return dbgen.LoginState{}, false
	}
	return saved, true
}

// verifyCode exchanges the code (with the PKCE verifier), verifies the ID
// token and its nonce, and checks the claims. A non-empty problem is the
// user-facing refusal, with its status and code.
func (s *Service) verifyCode(ctx context.Context, c *oidcClient, code string, saved dbgen.LoginState) (li loginIdentity, status int, errCode, problem string) {
	token, err := c.oauth.Exchange(ctx, code, oauth2.VerifierOption(saved.Verifier))
	if err != nil {
		s.log.WarnContext(ctx, "oidc code exchange failed", "err", err)
		return li, http.StatusUnauthorized, "login_failed", "Could not complete sign-in"
	}
	rawID, ok := token.Extra("id_token").(string)
	if !ok {
		return li, http.StatusUnauthorized, "login_failed", "The sign-in provider did not return an ID token"
	}
	idToken, err := c.verifier.Verify(ctx, rawID)
	if err != nil || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(saved.Nonce)) != 1 {
		return li, http.StatusUnauthorized, "login_failed", "Invalid identity token"
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return li, http.StatusUnauthorized, "login_failed", "Invalid identity token claims"
	}
	email, name, msg := s.extractProfile(claims)
	if msg != "" {
		return li, http.StatusForbidden, "email_required", msg
	}
	if !s.emailDomainAllowed(email) {
		return li, http.StatusForbidden, "domain_denied", "This account's email domain is not permitted"
	}
	li = loginIdentity{
		Issuer: idToken.Issuer, Subject: idToken.Subject, Email: email, Name: name,
		Claims: boundedClaims(claims), Method: "oidc", EmailVerified: truthy(claims["email_verified"]),
		Groups: Groups(claims, s.cfg.OIDC.GroupsClaim), RefreshToken: token.RefreshToken,
	}
	bootstrap := s.isBootstrap(idToken.Subject)
	if bootstrap {
		li.BootstrapRole = RolePlatformAdmin
	}
	// Only members of the required groups may sign in (the bootstrap admin
	// always may, so the platform can't be locked out). The same rule is
	// applied again while people are signed in (grouprecheck.go).
	if len(s.cfg.OIDC.AllowedGroups) > 0 && !bootstrap && !s.groupAllowed(li.Groups) {
		s.log.InfoContext(ctx, "sign-in refused: not in a required group", "subject", idToken.Subject)
		return li, http.StatusForbidden, "group_denied", "Your account is not in a group allowed to use this platform"
	}
	return li, 0, "", ""
}

// extractProfile returns the email and display name, or a user-facing
// problem message when the claims are unacceptable.
func (s *Service) extractProfile(claims map[string]any) (email, name, problem string) {
	email, _ = claims[s.cfg.OIDC.EmailClaim].(string)
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		return "", "", "Your sign-in provider did not supply an email address"
	}
	if s.cfg.OIDC.RequireVerifiedEmail && !truthy(claims["email_verified"]) {
		return "", "", "A verified email address is required"
	}
	name, _ = claims["name"].(string)
	if name == "" {
		given, _ := claims["given_name"].(string)
		family, _ := claims["family_name"].(string)
		name = strings.TrimSpace(given + " " + family)
	}
	if name == "" {
		name = email
	}
	return email, name, ""
}

func (s *Service) emailDomainAllowed(email string) bool {
	if len(s.cfg.OIDC.AllowedEmailDomains) == 0 {
		return true
	}
	domain := email[strings.LastIndex(email, "@")+1:]
	return slices.Contains(s.cfg.OIDC.AllowedEmailDomains, domain)
}

// truthy accepts the boolean or string forms providers use for email_verified.
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true")
	}
	return false
}

// boundedClaims stores all claims unless they are unusually large, in which
// case oversized values are dropped rather than failing the sign-in.
func boundedClaims(claims map[string]any) json.RawMessage {
	b, err := json.Marshal(claims)
	if err == nil && len(b) <= maxClaimsSize {
		return b
	}
	kept := map[string]any{"_truncated": true}
	for k, v := range claims {
		if vb, err := json.Marshal(v); err == nil && len(vb) <= 1024 {
			kept[k] = v
		}
	}
	b, _ = json.Marshal(kept)
	return b
}

// maxNext bounds a post-login path: long enough for an OAuth authorization
// request (internal/oauth bounds its query to 8000 characters).
const maxNext = 8300

// safeNext only allows same-origin relative paths as post-login redirects.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") ||
		strings.HasPrefix(next, "/\\") || strings.ContainsAny(next, "\r\n") || len(next) > maxNext {
		return "/"
	}
	return next
}

// failureReason classifies a refused sign-in by its status for the metrics:
// 403 (the address, group or domain rules), 5xx (the provider), else the
// request or the identity was invalid.
func failureReason(status int) string {
	switch {
	case status == http.StatusForbidden:
		return "denied"
	case status >= 500:
		return "unavailable"
	}
	return "invalid"
}
