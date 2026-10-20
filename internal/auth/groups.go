package auth

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Bounds of the groups stored per person (users.groups). They bound what is
// kept and shown, never what OIDC_ALLOWED_GROUPS is matched against.
const (
	MaxGroups      = 200
	MaxGroupLength = 256
)

// Groups reads the person's IdP groups from claim: a list of strings, or a
// single string. Entries that are empty, too long or contain control characters are
// dropped; the rest is distinct and sorted, and not capped: the group rule is
// matched against all of it (a person in more than MaxGroups groups must not be
// refused because the allowed group sorts late). Store it through boundGroups.
// "" (no claim configured) or a missing claim is no groups.
func Groups(claims map[string]any, claim string) []string {
	if claim == "" {
		return []string{}
	}
	var raw []string
	switch v := claims[claim].(type) {
	case string:
		raw = []string{v}
	case []string:
		raw = v
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				raw = append(raw, s)
			}
		}
	}
	return cleanGroups(raw)
}

// boundGroups is what is stored and shown of a person's groups: cleanGroups,
// at most MaxGroups of them.
func boundGroups(in []string) []string {
	out := cleanGroups(in)
	if len(out) > MaxGroups {
		out = out[:MaxGroups]
	}
	return out
}

// cleanGroups keeps valid, distinct groups, sorted.
func cleanGroups(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, g := range in {
		g = strings.TrimSpace(g)
		if g == "" || len(g) > MaxGroupLength || !utf8.ValidString(g) || strings.IndexFunc(g, unicode.IsControl) >= 0 || seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, g)
	}
	slices.Sort(out)
	return out
}
