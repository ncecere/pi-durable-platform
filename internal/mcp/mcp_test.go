package mcp

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestHostPatterns(t *testing.T) {
	pats, err := ParseHostPatterns("mcp.example.com, *.tools.example.org, http://127.0.0.1:*, https://api.example.net:8443")
	if err != nil {
		t.Fatal(err)
	}
	policy := ClientPolicy{Hosts: pats}
	for raw, want := range map[string]bool{
		"https://mcp.example.com/mcp":         true,
		"https://mcp.example.com:443/mcp":     true,
		"https://mcp.example.com:8443/mcp":    false, // only the default port without one
		"http://mcp.example.com/mcp":          false, // https by default
		"https://evil.mcp.example.com/mcp":    false,
		"https://a.tools.example.org/mcp":     true,
		"https://tools.example.org/mcp":       false, // *. means subdomains
		"https://atools.example.org/mcp":      false,
		"http://127.0.0.1:9000/mcp":           true,
		"https://127.0.0.1:9000/mcp":          false,
		"https://api.example.net:8443/x":      true,
		"https://api.example.net/x":           false,
		"https://mcp.example.com.evil.io/mcp": false,
	} {
		u, _ := url.Parse(raw)
		if got := policy.Allowed(u); got != want {
			t.Errorf("%s: %v, want %v", raw, got, want)
		}
	}
	for _, bad := range []string{"ftp://x.com", "*.", "a/b.com", "user@host"} {
		if _, err := ParseHostPatterns(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if pats, _ := ParseHostPatterns(" , "); len(pats) != 0 || (ClientPolicy{}).Allowed(&url.URL{Scheme: "https", Host: "x"}) {
		t.Fatal("an empty allowlist allows something")
	}
}

func TestSlugFrom(t *testing.T) {
	for in, want := range map[string]string{
		"GitHub Issues":         "github_issues",
		"  Jira / Confluence ":  "jira_confluence",
		"Ünïcode Tools 2":       "n_code_tools_2",
		strings.Repeat("a", 40): strings.Repeat("a", 32),
	} {
		if got := SlugFrom(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestReadReplySSE(t *testing.T) {
	body := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"tools\":[]}}\n\n"
	resp := &http.Response{Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	r, err := readReply(resp, 3)
	if err != nil || string(r.Result) != `{"tools":[]}` {
		t.Fatalf("%s %v", r.Result, err)
	}
	resp = &http.Response{Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	if _, err := readReply(resp, 4); err == nil {
		t.Fatal("found a reply to another request")
	}
}

func TestCheckHeaders(t *testing.T) {
	if err := checkHeaders(map[string]string{"Authorization": "Bearer x", "X-Workspace": "w"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]string{{"Host": "x"}, {"Mcp-Session-Id": "s"}, {"X": "a\nb"}, {"Bad Name": "v"}} {
		if checkHeaders(bad) == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}
