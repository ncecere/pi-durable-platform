package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPAHandler(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":         {Data: []byte("<html>app</html>")},
		"assets/app-123.js":  {Data: []byte("console.log(1)")},
		"assets/app-123.css": {Data: []byte("body{}")},
	}
	h := spaHandler(assets, true, spaOptions{})
	cases := []struct {
		method, path string
		status       int
		body, cache  string
	}{
		{"GET", "/", 200, "<html>app</html>", "no-cache"},
		{"GET", "/admin/users", 200, "<html>app</html>", "no-cache"},
		{"GET", "/teams/registrar", 200, "<html>app</html>", "no-cache"},
		{"GET", "/assets/app-123.js", 200, "console.log(1)", "public, max-age=31536000, immutable"},
		// A missing file isn't the app (the browser's /favicon.ico probe).
		{"GET", "/favicon.ico", 404, "404 page not found", ""},
		{"GET", "/assets/gone-1.js", 404, "404 page not found", ""},
		{"GET", "/v1/unknown", 404, `"not_found"`, "no-store"},
		{"GET", "/auth/nope", 404, `"not_found"`, "no-store"},
		{"GET", "/.well-known/jwks.json", 404, `"not_found"`, "no-store"},
		{"POST", "/admin/users", 404, `"not_found"`, "no-store"},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.body) || w.Header().Get("Cache-Control") != tc.cache {
			t.Errorf("%s %s = %d %q cache=%q", tc.method, tc.path, w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
		}
		if tc.status == 200 && !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
			t.Errorf("%s: missing CSP", tc.path)
		}
	}

	w := httptest.NewRecorder()
	spaHandler(fstest.MapFS{}, false, spaOptions{}).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "make web") {
		t.Errorf("unbuilt UI = %d %q", w.Code, w.Body.String())
	}
}

// index.html gets the instance's theme and title server-side (no flash of
// the wrong theme); both are escaped.
func TestSPAIndexPersonalised(t *testing.T) {
	page := `<!doctype html>
<html lang="en" data-brand="neutral">
  <head><title>Pi Durable Platform</title><script type="module" src="/assets/app-1.js"></script></head>
  <body><div id="root"></div></body>
</html>`
	assets := fstest.MapFS{"index.html": {Data: []byte(page)}, "assets/app-1.js": {Data: []byte("x")}}
	get := func(h http.Handler, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}

	h := spaHandler(assets, true, spaOptions{Title: `Campus </title><script>alert("x")</script> & Co`, Theme: "custom"})
	for _, p := range []string{"/", "/index.html", "/teams/registrar"} {
		w := get(h, p)
		body := w.Body.String()
		if w.Code != 200 || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s = %d %v", p, w.Code, w.Header())
		}
		if !strings.Contains(body, `<html data-brand="custom" lang="en">`) || strings.Count(body, "data-brand") != 1 {
			t.Errorf("%s: theme not applied once:\n%s", p, body)
		}
		want := `<title>Campus &lt;/title&gt;&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt; &amp; Co</title>`
		if !strings.Contains(body, want) || strings.Count(body, "<title>") != 1 || strings.Contains(body, "<script>alert") {
			t.Errorf("%s: title not replaced and escaped:\n%s", p, body)
		}
		if !strings.Contains(body, `<script type="module" src="/assets/app-1.js">`) {
			t.Errorf("%s: rest of the page changed:\n%s", p, body)
		}
	}
	if w := get(h, "/assets/app-1.js"); w.Body.String() != "x" {
		t.Errorf("asset = %q", w.Body.String())
	}

	// A page without data-brand gets one; HEAD has no body.
	h = spaHandler(fstest.MapFS{"index.html": {Data: []byte("<html><head><title>x</title></head></html>")}}, true,
		spaOptions{Title: "Campus AI", Theme: "neutral"})
	if body := get(h, "/").Body.String(); body != `<html data-brand="neutral"><head><title>Campus AI</title></head></html>` {
		t.Errorf("body = %q", body)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("HEAD", "/", nil))
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Errorf("HEAD = %d %q", w.Code, w.Body.String())
	}

	// An absolute logo's origin is allowed as an image source; nothing else is.
	csp := get(spaHandler(assets, true, spaOptions{LogoOrigin: "https://cdn.example.edu"}), "/").Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src 'self' data: https://cdn.example.edu;") {
		t.Errorf("csp = %q", csp)
	}
	csp = get(spaHandler(assets, true, spaOptions{LogoOrigin: "https://x; script-src *"}), "/").Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src 'self' data:;") || strings.Contains(csp, "script-src *") {
		t.Errorf("csp = %q", csp)
	}
	// No page may be framed.
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("csp = %q", csp)
	}
}
