package httpapi_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/httpapi"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// logoHost is an https server (on this machine, which the development
// allowance permits) that serves logos and one metadata document, and counts
// what was asked of it.
type logoHost struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits map[string]int
	png  []byte
}

func newLogoHost(t *testing.T) *logoHost {
	t.Helper()
	h := &logoHost{hits: map[string]int{}}
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	h.png = b.Bytes()
	h.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.hits[r.URL.Path]++
		h.mu.Unlock()
		switch r.URL.Path {
		case "/client.json":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"client_id": h.srv.URL + "/client.json", "client_name": "Doc App",
				"redirect_uris": []string{"http://127.0.0.1/cb"}, "logo_uri": h.srv.URL + "/logo.png"})
		case "/logo.png", "/other.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(h.png)
		case "/logo.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *logoHost) count(path string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits[path]
}

func (h *logoHost) tls() *tls.Config {
	return &tls.Config{RootCAs: h.srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
}

// getLogo asks for a client's logo as s, returning the whole response.
func getLogo(t *testing.T, s *session, query string) (*http.Response, []byte) {
	t.Helper()
	res, err := s.client.Get(s.app.URL + "/v1/oauth/client-logo?" + query)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, body
}

// An app's logo is served from the platform: checked, with headers that keep
// it inert, only for a client the platform knows, and never from an address
// the caller names.
func TestOAuthClientLogo(t *testing.T) {
	host := newLogoHost(t)
	app := newTestAppWith(t, nil, testutil.Logger(), func(d *httpapi.Deps) { d.OAuth.SetMetadataTLS(host.tls()) }, false)
	user, admin := app.signIn("user"), app.signIn("admin")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := app.Pool.Exec(context.Background(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	register := func(id, logo string) {
		t.Helper()
		var l *string
		if logo != "" {
			l = &logo
		}
		exec(`INSERT INTO oauth_clients (client_id, client_name, redirect_uris, logo_uri) VALUES ($1, 'Logo app', '{http://127.0.0.1/cb}', $2)`, id, l)
	}
	register("pdpcl_logo", host.srv.URL+"/logo.png")
	register("pdpcl_nologo", "")
	register("pdpcl_svg", host.srv.URL+"/logo.svg")
	register("pdpcl_missing", host.srv.URL+"/missing.png")
	q := func(id string) string { return "client_id=" + url.QueryEscape(id) }

	// A registered client's logo, with its safe headers.
	res, body := getLogo(t, user, q("pdpcl_logo"))
	if res.StatusCode != 200 || !bytes.Equal(body, host.png) {
		t.Fatalf("logo: %d %d bytes", res.StatusCode, len(body))
	}
	for k, want := range map[string]string{"Content-Type": "image/png", "X-Content-Type-Options": "nosniff",
		"Cache-Control": "private, max-age=3600", "Content-Security-Policy": "default-src 'none'; sandbox"} {
		if got := res.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	// Asking again, as someone else, is answered from the platform's cache.
	if res, _ := getLogo(t, admin, q("pdpcl_logo")); res.StatusCode != 200 || host.count("/logo.png") != 1 {
		t.Fatalf("second ask: %d, fetched %d times", res.StatusCode, host.count("/logo.png"))
	}

	// Not an open proxy: the address is the client's, whatever the request adds.
	extra := q("pdpcl_logo") + "&url=" + url.QueryEscape(host.srv.URL+"/other.png") + "&logo_uri=" + url.QueryEscape(host.srv.URL+"/other.png")
	if res, _ := getLogo(t, user, extra); res.StatusCode != 200 || host.count("/other.png") != 0 {
		t.Fatalf("extra parameters: %d, /other.png fetched %d times", res.StatusCode, host.count("/other.png"))
	}
	// An unknown client, however it is spelled, fetches nothing: not a URL
	// someone made known, not a metadata document the platform fetches for
	// the caller, not a client id that is a logo.
	for _, id := range []string{host.srv.URL + "/other.png", host.srv.URL + "/client.json", "pdpcl_unknown", "http://127.0.0.1/x.png", "x", "https://169.254.169.254/latest"} {
		res, body := getLogo(t, user, q(id))
		if res.StatusCode != 404 || errorCode(body) != "logo_not_found" {
			t.Fatalf("unknown %q: %d %s", id, res.StatusCode, body)
		}
	}
	if host.count("/other.png") != 0 || host.count("/client.json") != 0 {
		t.Fatalf("an unknown client made the platform fetch: other %d, document %d", host.count("/other.png"), host.count("/client.json"))
	}

	// No logo, an unacceptable one (SVG), one that isn't there.
	if res, body := getLogo(t, user, q("pdpcl_nologo")); res.StatusCode != 404 || errorCode(body) != "logo_not_found" {
		t.Fatalf("no logo: %d %s", res.StatusCode, body)
	}
	for _, id := range []string{"pdpcl_svg", "pdpcl_missing"} {
		res, body := getLogo(t, user, q(id))
		if res.StatusCode != 404 || errorCode(body) != "logo_unavailable" || bytes.Contains(body, []byte("script")) {
			t.Fatalf("%s: %d %s", id, res.StatusCode, body)
		}
	}
	if res, _ := getLogo(t, user, ""); res.StatusCode != 400 {
		t.Fatalf("no client_id: %d", res.StatusCode)
	}
	if res, err := http.Get(app.URL + "/v1/oauth/client-logo?" + q("pdpcl_logo")); err != nil || res.StatusCode != 401 {
		t.Fatalf("signed out: %v %v", res, err)
	}

	// A metadata-document app: its logo is known once its document is (the
	// consent page fetched it), and the document's logo is what is served.
	docID := host.srv.URL + "/client.json"
	consent := "client_id=" + url.QueryEscape(docID) + "&redirect_uri=" + url.QueryEscape("http://127.0.0.1/cb") +
		"&response_type=code&code_challenge_method=S256&code_challenge=" + string(bytes.Repeat([]byte("A"), 43)) + "&state=s"
	var info apitypes.OAuthConsent
	if code := user.get("/v1/oauth/consent?"+consent, &info); code != 200 || info.Client.LogoUrl == nil {
		t.Fatalf("consent: %d %+v", code, info)
	}
	if res, body := getLogo(t, user, q(docID)); res.StatusCode != 200 || !bytes.Equal(body, host.png) {
		t.Fatalf("metadata app logo: %d", res.StatusCode)
	}

	// An app someone connected stays known after its document leaves the
	// cache; its logo is the one consent showed.
	exec(`INSERT INTO oauth_grants (user_id, client_id, client_kind, client_name, logo_uri, resource)
		VALUES ($1, 'https://gone.example.com/client.json', 'metadata', 'Gone app', $2, $3)`, user.me.User.Id, host.srv.URL+"/other.png", app.URL)
	if res, body := getLogo(t, admin, q("https://gone.example.com/client.json")); res.StatusCode != 200 || !bytes.Equal(body, host.png) {
		t.Fatalf("connected app logo: %d %s", res.StatusCode, body)
	}

	// A person can't make the platform fetch without limit.
	limited := false
	for range 130 {
		if res, _ := getLogo(t, admin, q("pdpcl_unknown")); res.StatusCode == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("never rate limited")
	}
}
