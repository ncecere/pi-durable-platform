package oauth

import (
	"slices"
	"strings"
)

// Scopes. agents includes agents:read.
const (
	// ScopeAgents: act as the person on the agent routes (and read who they
	// are and their teams).
	ScopeAgents = "agents"
	// ScopeAgentsRead: the same routes, reads only: list agents and threads,
	// read conversations, follow events. No messages, no changes (every
	// write method is refused with 403 insufficient_scope).
	ScopeAgentsRead = "agents:read"
)

// Scopes are the scopes the server supports (metadata, token responses).
var Scopes = []string{ScopeAgents, ScopeAgentsRead}

// parseScope checks a requested scope (space-separated, RFC 6749 §3.3) and
// normalizes it: omitted means agents; agents (with or without agents:read)
// is [agents]; agents:read alone is [agents:read].
func parseScope(raw string) ([]string, bool) {
	read := false
	full := strings.TrimSpace(raw) == ""
	for _, s := range strings.Split(raw, " ") {
		switch s {
		case "":
		case ScopeAgents:
			full = true
		case ScopeAgentsRead:
			read = true
		default:
			return nil, false
		}
	}
	if full || !read {
		return []string{ScopeAgents}, true
	}
	return []string{ScopeAgentsRead}, true
}

// covers reports whether have (a grant's or a token's scopes) includes
// every scope in want.
func covers(have, want []string) bool {
	if slices.Contains(have, ScopeAgents) {
		return true
	}
	for _, w := range want {
		if w != ScopeAgentsRead || !slices.Contains(have, ScopeAgentsRead) {
			return false
		}
	}
	return true
}

// narrowest is want when have covers it, else have: what a token may carry
// of a grant (have) when the app asks for want.
func narrowest(have, want []string) []string {
	if len(want) > 0 && covers(have, want) {
		return want
	}
	return have
}

// ReadOnly reports whether scopes allow reads only.
func ReadOnly(scopes []string) bool { return !slices.Contains(scopes, ScopeAgents) }

func scopeString(scopes []string) string { return strings.Join(scopes, " ") }

const errScopeText = "The scopes are agents and agents:read."
