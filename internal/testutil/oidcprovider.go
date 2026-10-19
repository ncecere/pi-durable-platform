package testutil

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// OIDCProvider is a minimal OpenID Connect provider for tests. It
// auto-approves every authorization request, issuing an ID token with the
// claims in Claims, and verifies PKCE (S256) at the token endpoint. With the
// offline_access scope it also issues refresh tokens, which it exchanges for
// an ID token carrying the person's current claims (UpdateClaims changes
// them after sign-in; Revoke, SetDown and OmitIDTokenOnRefresh make it
// refuse, go away and answer through userinfo only).
type OIDCProvider struct {
	*httptest.Server
	ClientID     string
	ClientSecret string

	mu        sync.Mutex
	claims    map[string]any
	codes     map[string]pendingCode
	key       *rsa.PrivateKey
	current   map[string]map[string]any // each subject's claims now
	refresh   map[string]string         // refresh token -> subject
	access    map[string]string         // access token -> subject
	down      bool
	noIDTok   bool
	refreshes map[string]int
}

type pendingCode struct {
	nonce, challenge, redirectURI string
	claims                        map[string]any
	offline                       bool
}

func NewOIDCProvider(t testing.TB) *OIDCProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &OIDCProvider{
		ClientID: "pdp-test", ClientSecret: "test-secret",
		codes: map[string]pendingCode{}, key: key, current: map[string]map[string]any{},
		refresh: map[string]string{}, access: map[string]string{}, refreshes: map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /jwks", p.jwks)
	mux.HandleFunc("GET /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	mux.HandleFunc("GET /userinfo", p.userinfo)
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

// SetClaims sets the claims issued for the next sign-in. "sub" is required.
func (p *OIDCProvider) SetClaims(c map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.claims = c
	if sub, _ := c["sub"].(string); sub != "" {
		p.current[sub] = c
	}
}

// UpdateClaims changes a signed-in person's claims (their groups, say): what
// refresh and userinfo answer from now on, and the next sign-in's.
func (p *OIDCProvider) UpdateClaims(sub string, c map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.current[sub] = c
	if cur, _ := p.claims["sub"].(string); cur == sub {
		p.claims = c
	}
}

// Revoke ends a person's refresh tokens: the next refresh is invalid_grant.
func (p *OIDCProvider) Revoke(sub string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for t, s := range p.refresh {
		if s == sub {
			delete(p.refresh, t)
		}
	}
}

// SetDown makes the token and userinfo endpoints answer 503.
func (p *OIDCProvider) SetDown(down bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = down
}

// OmitIDTokenOnRefresh makes refresh answer without an ID token (the
// platform then asks userinfo).
func (p *OIDCProvider) OmitIDTokenOnRefresh(omit bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.noIDTok = omit
}

// Refreshes counts the refresh-token exchanges made for a person.
func (p *OIDCProvider) Refreshes(sub string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refreshes[sub]
}

func (p *OIDCProvider) userinfo(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	sub, ok := p.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	claims, down := p.current[sub], p.down
	p.mu.Unlock()
	if down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if !ok {
		http.Error(w, `{"error":"invalid_token"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(claims)
}

func (p *OIDCProvider) discovery(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                p.URL,
		"authorization_endpoint":                p.URL + "/authorize",
		"token_endpoint":                        p.URL + "/token",
		"userinfo_endpoint":                     p.URL + "/userinfo",
		"jwks_uri":                              p.URL + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (p *OIDCProvider) jwks(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &p.key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"},
	}})
}

func (p *OIDCProvider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != p.ClientID || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "bad authorize request", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	code := randomSuffix() + randomSuffix()
	p.codes[code] = pendingCode{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirectURI: q.Get("redirect_uri"), claims: p.claims,
		offline: slices.Contains(strings.Fields(q.Get("scope")), "offline_access")}
	p.mu.Unlock()
	target, _ := url.Parse(q.Get("redirect_uri"))
	v := target.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	target.RawQuery = v.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func (p *OIDCProvider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.ClientID || secret != p.ClientSecret {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	if r.PostForm.Get("grant_type") == "refresh_token" {
		p.refreshToken(w, r.PostForm.Get("refresh_token"))
		return
	}
	p.mu.Lock()
	pc, found := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code"))
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !found || base64.RawURLEncoding.EncodeToString(sum[:]) != pc.challenge || r.PostForm.Get("redirect_uri") != pc.redirectURI {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	sub, _ := pc.claims["sub"].(string)
	p.reply(w, sub, pc.claims, pc.nonce, pc.offline, true)
}

// refreshToken answers a refresh-token grant from the person's current claims.
func (p *OIDCProvider) refreshToken(w http.ResponseWriter, token string) {
	p.mu.Lock()
	sub, ok := p.refresh[token]
	claims, down, noIDTok := p.current[sub], p.down, p.noIDTok
	if ok && !down {
		p.refreshes[sub]++
	}
	p.mu.Unlock()
	if down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token revoked"}`))
		return
	}
	p.reply(w, sub, claims, "", true, !noIDTok)
}

// reply writes a token response: an access token, a refresh token when offline, an ID token when withIDToken.
func (p *OIDCProvider) reply(w http.ResponseWriter, sub string, claims map[string]any, nonce string, offline, withIDToken bool) {
	now := time.Now()
	out := map[string]any{"token_type": "Bearer", "expires_in": 300}
	access := "at-" + randomSuffix()
	p.mu.Lock()
	p.access[access] = sub
	if offline {
		rt := "rt-" + randomSuffix() + randomSuffix()
		p.refresh[rt] = sub
		out["refresh_token"] = rt
	}
	p.mu.Unlock()
	out["access_token"] = access
	if withIDToken {
		payload := map[string]any{}
		for k, v := range claims {
			payload[k] = v
		}
		payload["iss"], payload["aud"] = p.URL, p.ClientID
		if nonce != "" {
			payload["nonce"] = nonce
		}
		payload["iat"], payload["exp"] = now.Unix(), now.Add(5*time.Minute).Unix()
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: p.key, KeyID: "test"}},
			(&jose.SignerOptions{}).WithType("JWT"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		body, _ := json.Marshal(payload)
		obj, err := signer.Sign(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out["id_token"], _ = obj.CompactSerialize()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
