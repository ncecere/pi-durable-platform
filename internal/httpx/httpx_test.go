package httpx

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	cases := []struct {
		name, remote, xff, want string
	}{
		{"direct client ignores header", "203.0.113.5:1234", "1.2.3.4", "203.0.113.5"},
		{"trusted proxy uses header", "10.1.2.3:80", "198.51.100.7", "198.51.100.7"},
		{"spoofed left-most hop ignored", "10.1.2.3:80", "1.1.1.1, 198.51.100.7", "198.51.100.7"},
		{"chain of trusted proxies", "10.1.2.3:80", "198.51.100.7, 10.9.9.9", "198.51.100.7"},
		{"garbage header falls back to peer", "10.1.2.3:80", "not-an-ip", "10.1.2.3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remote
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := ClientIP(r, trusted); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestDecode(t *testing.T) {
	type body struct {
		Name string `json:"name"`
	}
	cases := []struct {
		name, in string
		status   int
	}{
		{"ok", `{"name":"a"}`, 0},
		{"unknown field", `{"name":"a","x":1}`, http.StatusBadRequest},
		{"two objects", `{"name":"a"}{"name":"b"}`, http.StatusBadRequest},
		{"too large", `{"name":"` + strings.Repeat("a", MaxBodyBytes) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/", strings.NewReader(tc.in))
			r.Header.Set("Content-Type", "application/json")
			var b body
			ok := Decode(w, r, &b)
			if tc.status == 0 && !ok {
				t.Fatalf("unexpected failure: %s", w.Body)
			}
			if tc.status != 0 && (ok || w.Code != tc.status) {
				t.Fatalf("want %d, got ok=%v code=%d", tc.status, ok, w.Code)
			}
		})
	}
}

// A request whose client went away is logged at info as a cancellation and
// recorded as 499, not as a 500 internal error.
func TestInternalClientGone(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	Internal(w, httptest.NewRequest("GET", "/v1/notifications", nil).WithContext(ctx), context.Canceled)
	if w.Code != StatusClientClosedRequest {
		t.Fatalf("status %d, want 499", w.Code)
	}
	if out := buf.String(); !strings.Contains(out, "level=INFO") || !strings.Contains(out, "client closed request") {
		t.Fatalf("log %q, want an info client cancellation", out)
	}

	buf.Reset()
	w = httptest.NewRecorder()
	Internal(w, httptest.NewRequest("GET", "/v1/notifications", nil), errors.New("boom"))
	if w.Code != http.StatusInternalServerError || !strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("status %d log %q, want a 500 logged as an error", w.Code, buf.String())
	}
}
