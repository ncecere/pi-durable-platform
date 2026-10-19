package oauth

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/netguard"
)

// logoFetcher fetches logos from srv whatever address a host resolves to
// (table), through the real address guard. hits counts connections made.
func logoFetcher(t *testing.T, srv *httptest.Server, table map[string]string) (*logos, *atomic.Int32) {
	t.Helper()
	var dialed atomic.Int32
	g := &netguard.Dialer{
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			a, ok := table[host]
			if !ok {
				return nil, errors.New("no such host")
			}
			return []netip.Addr{netip.MustParseAddr(a)}, nil
		},
		Connect: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dialed.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
	}
	l := newLogos(g)
	l.transport.TLSClientConfig = &tls.Config{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs, ServerName: "example.com"}
	return l, &dialed
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func gifBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := gif.Encode(&b, image.NewPaletted(image.Rect(0, 0, 8, 8), color.Palette{color.Black, color.White}), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// webpBytes is the start of a lossless WebP file (a header the checks accept;
// the platform doesn't decode WebP).
func webpBytes() []byte {
	return append([]byte("RIFF\x1e\x00\x00\x00WEBPVP8L\x11\x00\x00\x00\x2f\x00\x00\x00\x00"), make([]byte, 20)...)
}

const svg = `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`

func TestLogoServedWhenVerified(t *testing.T) {
	cases := []struct {
		name, declared string
		body           []byte
	}{
		{"png", "image/png", pngBytes(t)},
		{"jpeg", "image/jpeg", jpegBytes(t)},
		{"gif", "image/gif", gifBytes(t)},
		{"webp", "image/webp", webpBytes()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var served atomic.Int32
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				served.Add(1)
				w.Header().Set("Content-Type", tc.declared+"; charset=binary")
				_, _ = w.Write(tc.body)
			}))
			t.Cleanup(srv.Close)
			l, _ := logoFetcher(t, srv, map[string]string{"cdn.example.com": "8.8.8.8"})
			got, err := l.get(context.Background(), "https://cdn.example.com/logo")
			if err != nil || got.ContentType != tc.declared || !bytes.Equal(got.Data, tc.body) {
				t.Fatalf("logo %q %v", got.ContentType, err)
			}
			// The second ask is answered from the cache.
			if _, err := l.get(context.Background(), "https://cdn.example.com/logo"); err != nil || served.Load() != 1 {
				t.Fatalf("second ask: %v, fetched %d times", err, served.Load())
			}
		})
	}
}

// Addresses the guard refuses are never connected to, and a refusal is
// remembered for a short while.
func TestLogoAddressGuard(t *testing.T) {
	var served atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes(t))
	}))
	t.Cleanup(srv.Close)
	l, dialed := logoFetcher(t, srv, map[string]string{
		"private.example.com":  "10.1.2.3",
		"loopback.example.com": "127.0.0.1",
		"metadata.example.com": "169.254.169.254",
		"cgnat.example.com":    "100.64.0.1",
		"mapped.example.com":   "::ffff:192.168.0.1",
	})
	for _, host := range []string{"private", "loopback", "metadata", "cgnat", "mapped"} {
		if _, err := l.get(context.Background(), "https://"+host+".example.com/logo.png"); !errors.Is(err, errLogoUnavailable) {
			t.Fatalf("%s: %v", host, err)
		}
		if _, _, err := l.fetch(context.Background(), "https://"+host+".example.com/logo.png"); !errors.Is(err, netguard.ErrBlockedAddress) {
			t.Fatalf("%s: fetch error %v is not a guard refusal", host, err)
		}
	}
	if dialed.Load() != 0 || served.Load() != 0 {
		t.Fatalf("connected %d times, served %d", dialed.Load(), served.Load())
	}
	// Plain http, user info and other schemes are refused before any dial.
	for _, u := range []string{"http://public.example.com/logo.png", "https://user:pw@public.example.com/logo.png", "data:image/png;base64,AAAA", "file:///etc/passwd"} {
		if _, outcome, err := l.fetch(context.Background(), u); err == nil || outcome != logoBadURL {
			t.Fatalf("%s: %q %v", u, outcome, err)
		}
	}
}

// A redirect, even to a public address, is not followed; one to a private
// address certainly isn't.
func TestLogoRedirectsRefused(t *testing.T) {
	var target atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final.png" {
			target.Add(1)
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes(t))
			return
		}
		w.Header().Set("Location", r.URL.Query().Get("to"))
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	l, _ := logoFetcher(t, srv, map[string]string{"cdn.example.com": "8.8.8.8", "evil.example.com": "10.0.0.9"})
	for _, to := range []string{"https://evil.example.com/final.png", "https://cdn.example.com/final.png", "/final.png"} {
		_, outcome, err := l.fetch(context.Background(), "https://cdn.example.com/r?to="+to)
		if outcome != logoRedirect || err == nil {
			t.Fatalf("redirect to %s: %q %v", to, outcome, err)
		}
	}
	if target.Load() != 0 {
		t.Fatalf("a redirect was followed (%d)", target.Load())
	}
}

func TestLogoRefusesWhatIsNotAnAcceptableImage(t *testing.T) {
	big := append(pngBytes(t), make([]byte, maxLogoBytes)...)
	cases := []struct {
		name, declared string
		status         int
		body           []byte
		outcome        string
	}{
		{"too large", "image/png", 200, big, logoTooLarge},
		{"svg", "image/svg+xml", 200, []byte(svg), logoBadType},
		{"svg claiming png", "image/png", 200, []byte(svg), logoNotImage},
		{"html", "text/html", 200, []byte("<html>"), logoBadType},
		{"no content type", "", 200, pngBytes(t), logoBadType},
		{"png claiming jpeg", "image/jpeg", 200, pngBytes(t), logoNotImage},
		{"garbage png", "image/png", 200, []byte("not an image"), logoNotImage},
		{"garbage webp", "image/webp", 200, []byte("RIFF....WEBPjunk"), logoNotImage},
		{"not found", "image/png", 404, pngBytes(t), logoHTTPStatus},
		{"ico", "image/x-icon", 200, pngBytes(t), logoBadType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.declared != "" {
					w.Header().Set("Content-Type", tc.declared)
				} else {
					w.Header()["Content-Type"] = nil // no sniffing by the test server
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write(tc.body)
			}))
			t.Cleanup(srv.Close)
			l, _ := logoFetcher(t, srv, map[string]string{"cdn.example.com": "8.8.8.8"})
			if _, outcome, err := l.fetch(context.Background(), "https://cdn.example.com/logo"); err == nil || outcome != tc.outcome {
				t.Fatalf("outcome %q, error %v; want %q", outcome, err, tc.outcome)
			}
			// The caller learns nothing of why.
			if _, err := l.get(context.Background(), "https://cdn.example.com/logo"); !errors.Is(err, errLogoUnavailable) {
				t.Fatalf("get: %v", err)
			}
		})
	}
}

// A failure is remembered briefly (an app can't make the platform fetch
// again at the rate people ask), then tried again.
func TestLogoFailureIsCachedBriefly(t *testing.T) {
	var served atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	l, _ := logoFetcher(t, srv, map[string]string{"cdn.example.com": "8.8.8.8"})
	now := time.Now()
	l.now = func() time.Time { return now }
	for range 3 {
		if _, err := l.get(context.Background(), "https://cdn.example.com/logo"); !errors.Is(err, errLogoUnavailable) {
			t.Fatal(err)
		}
	}
	if served.Load() != 1 {
		t.Fatalf("fetched %d times", served.Load())
	}
	now = now.Add(logoFailureTTL + time.Second)
	_, _ = l.get(context.Background(), "https://cdn.example.com/logo")
	if served.Load() != 2 {
		t.Fatalf("fetched %d times after the failure expired", served.Load())
	}
}

func TestLogoCacheIsBounded(t *testing.T) {
	l := newLogos(&netguard.Dialer{})
	for i := range maxCachedLogos * 2 {
		l.store(fmt.Sprintf("https://cdn.example.com/%d", i), cachedLogo{ok: true, until: time.Now().Add(time.Hour)})
	}
	if len(l.cache) > maxCachedLogos {
		t.Fatalf("%d cached logos", len(l.cache))
	}
}
