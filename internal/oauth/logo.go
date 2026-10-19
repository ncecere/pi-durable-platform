package oauth

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"image"
	_ "image/gif" // registers the decoders DecodeConfig uses to verify a logo
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/netguard"
	"github.com/ncecere/pi-durable-platform/internal/store"
)

// Client logos. An app's logo_uri is someone else's URL, and the console's
// Content-Security-Policy allows images only from the platform itself, so
// the platform fetches the logo and serves it from its own origin (GET
// /v1/oauth/client-logo). It is not an open proxy: the URL is never taken
// from the request but from what the platform knows of the client (a
// registered client's registration, a metadata document in the cache, or a
// connection someone made), and the bytes are only served when they are a
// PNG, JPEG, GIF or WebP image: never SVG (it can carry script). The fetch
// goes through the address guard (internal/netguard) like metadata
// documents: public addresses only, pinned at dial time, https only, no
// redirects, no proxy, a short timeout and a size cap. Results (and
// failures, briefly) are cached in memory.
const (
	maxLogoBytes = 256 << 10
	// maxLogoPixels bounds each side of a decoded image's header: a small file
	// can still declare a huge canvas that a browser would allocate.
	maxLogoPixels  = 4096
	logoTimeout    = 5 * time.Second
	logoCacheTTL   = time.Hour
	logoFailureTTL = 2 * time.Minute
	maxCachedLogos = 200
)

// Logo is a verified image, as it is served.
type Logo struct {
	// ContentType is the verified type: image/png, image/jpeg, image/gif or
	// image/webp.
	ContentType string
	Data        []byte
}

var (
	errNoLogo = apperr.NotFound("logo_not_found", "This app has no logo")
	// errLogoUnavailable says nothing of why: the address is the app's choice.
	errLogoUnavailable = apperr.NotFound("logo_unavailable", "The app's logo couldn't be loaded")
)

var errLogoRedirect = errors.New("oauth: logos may not redirect")

type cachedLogo struct {
	logo  Logo
	ok    bool
	until time.Time
}

// logos fetches and caches logos.
type logos struct {
	client    *http.Client
	transport *http.Transport
	now       func() time.Time
	log       func() *slog.Logger
	mu        sync.Mutex
	cache     map[string]cachedLogo // by logo URL
}

func newLogos(guard *netguard.Dialer) *logos {
	tr := guard.Transport(logoTimeout)
	return &logos{
		client:    &http.Client{Transport: tr, Timeout: logoTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errLogoRedirect }},
		transport: tr, now: time.Now, cache: map[string]cachedLogo{},
	}
}

// SetMetadataTLS sets the TLS configuration of metadata document and logo
// fetches (tests: to trust a test server's certificate). The address guard
// stays.
func (s *Service) SetMetadataTLS(c *tls.Config) {
	s.docs.transport.TLSClientConfig = c
	s.logos.transport.TLSClientConfig = c.Clone()
}

// KnownClient finds a client the platform already knows, without fetching
// anything: a registered client, a metadata document in the cache, or (for
// an app whose document has left the cache) the client of a connection
// someone made. Anything else is not found: a caller can't make the platform
// fetch a document of their choosing.
func (s *Service) KnownClient(ctx context.Context, clientID string) (Client, error) {
	if clientID == "" || len(clientID) > maxURILen {
		return Client{}, errNoLogo
	}
	if strings.HasPrefix(clientID, clientPrefix) {
		c, err := s.ResolveClient(ctx, clientID)
		var oe *Error
		if errors.As(err, &oe) {
			return Client{}, errNoLogo
		}
		return c, err
	}
	s.docs.mu.Lock()
	hit, ok := s.docs.cache[clientID]
	s.docs.mu.Unlock()
	if ok && s.docs.now().Before(hit.until) {
		return hit.client, nil
	}
	g, err := s.q.GetLatestOAuthGrantByClient(ctx, clientID)
	if err != nil {
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return Client{}, errNoLogo
		}
		return Client{}, err
	}
	return GrantClient(g), nil
}

// ClientLogo is the verified logo of a client the platform knows. The
// address is the client's own published logo_uri, never the caller's.
func (s *Service) ClientLogo(ctx context.Context, clientID string) (Logo, error) {
	c, err := s.KnownClient(ctx, clientID)
	if err != nil {
		return Logo{}, err
	}
	if c.LogoURI == "" {
		return Logo{}, errNoLogo
	}
	return s.logos.get(ctx, c.LogoURI)
}

func (l *logos) get(ctx context.Context, logoURL string) (Logo, error) {
	l.mu.Lock()
	hit, ok := l.cache[logoURL]
	l.mu.Unlock()
	if ok && l.now().Before(hit.until) {
		if !hit.ok {
			return Logo{}, errLogoUnavailable
		}
		return hit.logo, nil
	}
	start := l.now()
	logo, outcome, err := l.fetch(ctx, logoURL)
	l.logger().InfoContext(ctx, "oauth client logo fetched", "host", hostOf(logoURL), "outcome", outcome,
		"duration_ms", l.now().Sub(start).Milliseconds())
	if err != nil {
		if ctx.Err() == nil { // not the caller giving up
			l.store(logoURL, cachedLogo{until: l.now().Add(logoFailureTTL)})
		}
		return Logo{}, errLogoUnavailable
	}
	l.store(logoURL, cachedLogo{logo: logo, ok: true, until: l.now().Add(logoCacheTTL)})
	return logo, nil
}

func (l *logos) logger() *slog.Logger {
	if l.log != nil {
		if lg := l.log(); lg != nil {
			return lg
		}
	}
	return slog.Default()
}

// Logo fetch outcomes, as logged (never the address's path or the fetch
// error's text).
const (
	logoOK          = "ok"
	logoBadURL      = "bad_url"
	logoBlocked     = "blocked_address"
	logoRedirect    = "redirect"
	logoUnreachable = "unreachable"
	logoHTTPStatus  = "http_status"
	logoBadType     = "content_type"
	logoTooLarge    = "too_large"
	logoUnreadable  = "unreadable"
	logoNotImage    = "not_an_image"
)

var logoTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

func (l *logos) fetch(ctx context.Context, logoURL string) (Logo, string, error) {
	u, err := url.Parse(logoURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return Logo{}, logoBadURL, errors.New("bad url")
	}
	ctx, cancel := context.WithTimeout(ctx, logoTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Logo{}, logoBadURL, err
	}
	req.Header.Set("Accept", "image/png, image/jpeg, image/gif, image/webp")
	res, err := l.client.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, netguard.ErrBlockedAddress):
			return Logo{}, logoBlocked, err
		case errors.Is(err, errLogoRedirect):
			return Logo{}, logoRedirect, err
		}
		return Logo{}, logoUnreachable, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Logo{}, logoHTTPStatus, errors.New("status")
	}
	declared, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || !logoTypes[declared] {
		return Logo{}, logoBadType, errors.New("content type")
	}
	if res.ContentLength > maxLogoBytes {
		return Logo{}, logoTooLarge, errors.New("too large")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxLogoBytes+1))
	if err != nil {
		return Logo{}, logoUnreadable, err
	}
	if len(data) > maxLogoBytes {
		return Logo{}, logoTooLarge, errors.New("too large")
	}
	if got := sniffImage(data); got == "" || got != declared {
		return Logo{}, logoNotImage, errors.New("not the declared image")
	}
	return Logo{ContentType: declared, Data: data}, logoOK, nil
}

// sniffImage is the type of an image's bytes when they are a well-formed
// header of PNG, JPEG, GIF (decoded as far as its size) or WebP; "" for
// anything else, SVG included.
func sniffImage(data []byte) string {
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		if k := string(data[12:16]); len(data) > 20 && (k == "VP8 " || k == "VP8L" || k == "VP8X") {
			return "image/webp"
		}
		return ""
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxLogoPixels || cfg.Height > maxLogoPixels {
		return ""
	}
	switch format {
	case "png", "jpeg", "gif":
		return "image/" + format
	}
	return ""
}

// store caches a result, dropping expired entries (and, when still full,
// others) to stay bounded.
func (l *logos) store(logoURL string, c cachedLogo) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if _, ok := l.cache[logoURL]; !ok && len(l.cache) >= maxCachedLogos {
		for k, v := range l.cache {
			if now.After(v.until) {
				delete(l.cache, k)
			}
		}
		for k := range l.cache {
			if len(l.cache) < maxCachedLogos {
				break
			}
			delete(l.cache, k)
		}
	}
	l.cache[logoURL] = c
}
