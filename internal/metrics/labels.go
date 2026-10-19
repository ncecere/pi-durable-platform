package metrics

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// otherLabel is the value of a label that is not one of the allowed ones.
const otherLabel = "other"

// StatusClass is "2xx", "4xx" and so on for an HTTP status ("other" for one
// outside 100-599).
func StatusClass(status int) string {
	if status < 100 || status > 599 {
		return otherLabel
	}
	return strconv.Itoa(status/100) + "xx"
}

// Method is an HTTP method as a label: the standard ones, else "other".
func Method(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m
	}
	return otherLabel
}

// RoutePattern turns a ServeMux pattern ("GET /v1/teams/{team}") into the
// route label ("/v1/teams/{team}"). A request nothing matched (the web app's
// files, 404s) has the fallback pattern "/" or none: both are "unmatched",
// so paths never reach a label.
func RoutePattern(pattern string) string {
	if _, rest, ok := strings.Cut(pattern, " "); ok {
		pattern = rest
	}
	if pattern == "" || pattern == "/" {
		return "unmatched"
	}
	return pattern
}

// LauncherMode is the launcher's mode ("local", "kubernetes",
// "agent-sandbox") from its Kind(), which for a warm pool also names the pool.
func LauncherMode(kind string) string {
	mode, _, _ := strings.Cut(kind, " ")
	if mode == "" {
		return unknown
	}
	return mode
}

// oneOf is v when it is one of allowed, else "other".
func oneOf(v string, allowed ...string) string {
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return otherLabel
}

// labelGuard keeps a label's values bounded: a value is accepted when it is
// short and made of plain characters and fewer than max distinct values have
// been accepted before; anything else is "other". It is the backstop for
// labels whose values an administrator chooses (model names, MCP server
// slugs): a flood of new names cannot grow the series without bound.
type labelGuard struct {
	mu     sync.Mutex
	seen   map[string]struct{}
	max    int // distinct values
	maxLen int // bytes per value
}

func newLabelGuard(max, maxLen int) *labelGuard {
	return &labelGuard{seen: map[string]struct{}{}, max: max, maxLen: maxLen}
}

func (g *labelGuard) clean(v string) string {
	if v == "" || len(v) > g.maxLen || !plain(v) {
		return otherLabel
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.seen[v]; ok {
		return v
	}
	if len(g.seen) >= g.max {
		return otherLabel
	}
	g.seen[v] = struct{}{}
	return v
}

// plain: letters, digits and . _ - : / { } ~ @ (what model names, slugs and
// route patterns are made of).
func plain(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("._-:/{}~@", c) >= 0:
		default:
			return false
		}
	}
	return true
}
