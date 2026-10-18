package oauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/apikeys"
	"github.com/ncecere/pi-durable-platform/internal/netguard"
	"github.com/ncecere/pi-durable-platform/internal/serviceaccounts"
)

// docFetcher fetches documents from srv whatever address a host resolves
// to (table), through the real address guard.
func docFetcher(t *testing.T, srv *httptest.Server, table map[string]string, dev bool) *metadataDocs {
	t.Helper()
	g := &netguard.Dialer{
		AllowPrivate: dev,
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			a, ok := table[host]
			if !ok {
				return nil, errors.New("no such host")
			}
			return []netip.Addr{netip.MustParseAddr(a)}, nil
		},
		// Every approved dial reaches the test server.
		Connect: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
	}
	d := newMetadataDocs(g, dev)
	d.transport.TLSClientConfig = &tls.Config{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs, ServerName: "example.com"}
	return d
}

func clientDocServer(t *testing.T) *httptest.Server {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=120")
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "https://" + r.Host + r.URL.Path, "client_name": "  Corp   Assistant ",
			"redirect_uris": []string{"http://127.0.0.1/cb"}, "logo_uri": "http://insecure.example/logo.png"})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The documents' hosts go through the address guard: public addresses
// only; loopback, private, CGNAT and metadata addresses never.
func TestMetadataDocumentAddressGuard(t *testing.T) {
	srv := clientDocServer(t)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	table := map[string]string{
		"clients.corp.example.com": "10.20.0.5",
		"public.example.com":       "8.8.8.8",
		"rebind.example.com":       "169.254.169.254",
		"local.example.com":        "127.0.0.1",
		"cgnat.example.com":        "100.64.1.1",
		"ula.example.com":          "fd12::1",
		"mapped.example.com":       "::ffff:192.168.1.1",
		"zero.example.com":         "0.0.0.0",
	}
	id := func(host string) string { return "https://" + host + ":" + port + "/client.json" }
	cases := []struct {
		host string
		ok   bool
	}{
		{"public.example.com", true},
		{"clients.corp.example.com", false},
		{"rebind.example.com", false},
		{"local.example.com", false},
		{"cgnat.example.com", false},
		{"ula.example.com", false},
		{"mapped.example.com", false},
		{"zero.example.com", false},
		{"127.0.0.1", false},
		{"10.20.0.9", false},
		{"[::1]", false},
	}
	for _, c := range cases {
		t.Run(c.host, func(t *testing.T) {
			d := docFetcher(t, srv, table, false)
			got, err := d.get(context.Background(), id(c.host))
			if c.ok {
				if err != nil || got.Name != "Corp Assistant" || got.Kind != KindMetadata || got.LogoURI != "" || got.Host() != c.host+":"+port {
					t.Fatalf("get = %+v %v", got, err)
				}
				return
			}
			var oe *Error
			if !errors.As(err, &oe) || !oe.ShowPage() || oe.Code != "invalid_client" || !strings.Contains(oe.Description, "address") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	// The development allowance reaches loopback, and plain http there.
	d := docFetcher(t, srv, table, true)
	if _, err := d.get(context.Background(), id("127.0.0.1")); err != nil {
		t.Errorf("development loopback: %v", err)
	}
	if !validMetadataURL("http://127.0.0.1:9/c.json", true) || validMetadataURL("http://127.0.0.1:9/c.json", false) ||
		validMetadataURL("http://10.0.0.1/c.json", true) {
		t.Error("plain http rule")
	}
}

func TestMetadataDocumentCache(t *testing.T) {
	srv := clientDocServer(t)
	d := docFetcher(t, srv, map[string]string{"public.example.com": "8.8.8.8"}, false)
	now := time.Now()
	d.now = func() time.Time { return now }
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	id := "https://public.example.com:" + port + "/c.json"
	if _, err := d.get(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if hit, ok := d.cache[id]; !ok || hit.until.Sub(now) != 2*time.Minute {
		t.Fatalf("cached until %v", hit.until.Sub(now))
	}
	srv.Close() // served from the cache now
	if _, err := d.get(context.Background(), id); err != nil {
		t.Fatalf("cached get: %v", err)
	}
	now = now.Add(3 * time.Minute)
	if _, err := d.get(context.Background(), id); err == nil {
		t.Fatal("expired entry served")
	}
	for cc, want := range map[string]time.Duration{
		"": docCacheDefault, "max-age=5": docCacheMin, "max-age=600": 10 * time.Minute, "max-age=99999": docCacheMax,
		"no-store": docCacheMin, "public, max-age=1800": 30 * time.Minute, "max-age=junk": docCacheDefault,
	} {
		if got := cacheFor(cc); got != want {
			t.Errorf("cacheFor(%q) = %v, want %v", cc, got, want)
		}
	}
}

// Every document fetch is logged at info level with the host, the HTTP
// status and the outcome, never the document or its path.
func TestMetadataDocumentFetchLogging(t *testing.T) {
	const sentinel = "Sentinel Assistant 7f3a"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "https://" + r.Host + r.URL.Path, "client_name": sentinel,
			"redirect_uris": []string{"http://127.0.0.1/cb"}})
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	d := docFetcher(t, srv, map[string]string{"public.example.com": "8.8.8.8", "local.example.com": "127.0.0.1"}, false)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	d.log = func() *slog.Logger { return logger }
	host := "public.example.com:" + port
	cases := []struct{ id, want string }{
		{"https://" + host + "/secret-path/client.json", "status=200 outcome=ok"},
		{"https://" + host + "/missing.json", "status=404 outcome=http_status"},
		{"https://local.example.com:" + port + "/client.json", "status=0 outcome=blocked_address"},
	}
	for _, c := range cases {
		buf.Reset()
		_, _ = d.get(context.Background(), c.id)
		line := buf.String()
		if !strings.Contains(line, "level=INFO") || !strings.Contains(line, "oauth client metadata document fetched") ||
			!strings.Contains(line, "host="+hostOf(c.id)) || !strings.Contains(line, c.want) {
			t.Errorf("%s logged %q, want %q", c.id, line, c.want)
		}
		if strings.Contains(line, sentinel) || strings.Contains(line, "secret-path") || strings.Contains(line, "client.json") {
			t.Errorf("%s: the log has the document or its path: %q", c.id, line)
		}
	}
	// A cached document isn't fetched (or logged) again.
	buf.Reset()
	if _, err := d.get(context.Background(), cases[0].id); err != nil || buf.Len() != 0 {
		t.Errorf("cached get = %v, logged %q", err, buf.String())
	}
}

func TestPKCE(t *testing.T) {
	v := strings.Repeat("a", 43)
	sum := sha256.Sum256([]byte(v))
	ch := base64.RawURLEncoding.EncodeToString(sum[:])
	if !verifyPKCE(v, ch) || verifyPKCE(v+"b", ch) || verifyPKCE("short", ch) || verifyPKCE(v, v) {
		t.Error("verifyPKCE")
	}
	if !pkceChallenge.MatchString(ch) || pkceChallenge.MatchString(v+"=") {
		t.Error("challenge shape")
	}
}

func TestRedirectURIs(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://app.example.com/cb": true, "http://127.0.0.1:4567/cb": true, "http://localhost/cb": true, "http://[::1]:9/cb": true,
		"http://app.example.com/cb": false, "myapp://cb": false, "https://app.example.com/cb#x": false, "https://u:p@app.example.com/cb": false,
	} {
		if validRedirectURI(raw) != want {
			t.Errorf("validRedirectURI(%q) != %v", raw, want)
		}
	}
	reg := []string{"http://127.0.0.1/callback", "https://app.example.com/cb"}
	for raw, want := range map[string]bool{
		"http://127.0.0.1:5555/callback": true, "http://127.0.0.1/callback": true, "https://app.example.com/cb": true,
		"https://app.example.com:444/cb": false, "http://127.0.0.1:5555/other": false, "http://localhost:5555/callback": false,
		"http://127.0.0.1:5555/callback?x=1": false,
	} {
		if matchRedirect(reg, raw) != want {
			t.Errorf("matchRedirect(%q) != %v", raw, want)
		}
	}
}

func TestScope(t *testing.T) {
	for raw, want := range map[string]bool{
		"": true, "agents": true, "agents agents": true, " agents ": true,
		"openid": false, "agents openid": false, "Agents": false, "admin": false,
	} {
		got, ok := parseScope(raw)
		if ok != want || (ok && (len(got) != 1 || got[0] != ScopeAgents)) {
			t.Errorf("parseScope(%q) = %v %v, want %v", raw, got, ok, want)
		}
	}
}

func TestRequestShapes(t *testing.T) {
	q, _ := url.ParseQuery("client_id=a&client_id=b")
	if _, err := RequestFromQuery(q); err == nil {
		t.Error("repeated parameter accepted")
	} else if e := err.(*Error); !e.ShowPage() {
		t.Error("repeated parameter not a page")
	}
	s := &Service{issuer: "https://agents.example.com"}
	if s.Resource() != "https://agents.example.com" {
		t.Errorf("resource = %q", s.Resource())
	}
	tok := newToken(AccessPrefix)
	if !IsAccessToken(tok) || IsAccessToken(tok+"x") || IsAccessToken(newToken(RefreshPrefix)) || IsAccessToken("pdpat_"+strings.Repeat("!", 43)) {
		t.Error("token shapes")
	}
	if !LooksLikeToken(tok) || !LooksLikeToken("pdprt_x") || LooksLikeToken("pdp_x") {
		t.Error("LooksLikeToken")
	}
	s.pepper = []byte("pepper")
	if d := s.digest(tok); len(d) != 64 || strings.Contains(d, tok) || d == s.digest(newToken(AccessPrefix)) {
		t.Error("digest")
	}
	// The label keeps token digests apart from API-key digests.
	if s.digest(tok) == apikeys.Digest(s.pepper, tok) {
		t.Error("digest without the oauth label")
	}
}

// Tokens, codes and client IDs never look like API keys (pdp_, pdp_sa_), so
// the key checks never send them to the key tables.
func TestPrefixesDontCollideWithKeys(t *testing.T) {
	for _, p := range []string{AccessPrefix, RefreshPrefix, codePrefix, clientPrefix} {
		tok := newToken(p)
		if strings.HasPrefix(p, apikeys.Prefix) || apikeys.LooksLikeKey(tok) || serviceaccounts.LooksLikeKey(tok) {
			t.Errorf("%s tokens look like keys", p)
		}
	}
}

func TestValidateTrusted(t *testing.T) {
	ok := []TrustedClient{{ClientID: " https://chat.example.com/oauth/client-metadata.json ", Label: " Chat "}}
	got, err := validateTrusted(ok)
	if err != nil || got[0].ClientID != "https://chat.example.com/oauth/client-metadata.json" || got[0].Label != "Chat" {
		t.Fatalf("validateTrusted = %+v %v", got, err)
	}
	for _, bad := range [][]TrustedClient{
		{{ClientID: "pdpcl_abc"}}, {{ClientID: "https://chat.example.com"}}, {{ClientID: "https://chat.example.com/"}},
		{{ClientID: "http://chat.example.com/x"}},
		{{ClientID: "https://chat.example.com/x"}, {ClientID: "https://chat.example.com/x"}}, {{ClientID: "https://chat.example.com/x?y"}},
		{{ClientID: "https://chat.example.com/x", Label: "a\nb"}}, {{ClientID: "https://chat.example.com/x", Label: strings.Repeat("a", 101)}},
		make([]TrustedClient, MaxTrustedClients+1),
	} {
		if _, err := validateTrusted(bad); err == nil {
			t.Errorf("validateTrusted(%+v) accepted", bad)
		}
	}
}
