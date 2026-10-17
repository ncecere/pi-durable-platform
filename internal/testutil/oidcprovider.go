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
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// OIDCProvider is a minimal OpenID Connect provider for tests. It
// auto-approves every authorization request, issuing an ID token with the
// claims in Claims, and verifies PKCE (S256) at the token endpoint.
type OIDCProvider struct {
	*httptest.Server
	ClientID     string
	ClientSecret string

	mu     sync.Mutex
	claims map[string]any
	codes  map[string]pendingCode
	key    *rsa.PrivateKey
}

type pendingCode struct {
	nonce, challenge, redirectURI string
	claims                        map[string]any
}

func NewOIDCProvider(t testing.TB) *OIDCProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &OIDCProvider{
		ClientID: "pdp-test", ClientSecret: "test-secret",
		codes: map[string]pendingCode{}, key: key,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /jwks", p.jwks)
	mux.HandleFunc("GET /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

// SetClaims sets the claims issued for the next sign-in. "sub" is required.
func (p *OIDCProvider) SetClaims(c map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.claims = c
}

func (p *OIDCProvider) discovery(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                p.URL,
		"authorization_endpoint":                p.URL + "/authorize",
		"token_endpoint":                        p.URL + "/token",
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
	p.codes[code] = pendingCode{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirectURI: q.Get("redirect_uri"), claims: p.claims}
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
	p.mu.Lock()
	pc, found := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code"))
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !found || base64.RawURLEncoding.EncodeToString(sum[:]) != pc.challenge || r.PostForm.Get("redirect_uri") != pc.redirectURI {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	now := time.Now()
	payload := map[string]any{}
	for k, v := range pc.claims {
		payload[k] = v
	}
	payload["iss"], payload["aud"], payload["nonce"] = p.URL, p.ClientID, pc.nonce
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
	idToken, _ := obj.CompactSerialize()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "at-" + randomSuffix(), "token_type": "Bearer", "expires_in": 300, "id_token": idToken,
	})
}
