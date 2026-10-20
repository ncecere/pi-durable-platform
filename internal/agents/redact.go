package agents

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// The arguments of a tool call that asks for approval, as the people who
// decide see them: the runtime sends a redacted view, and the platform
// applies the same rules again (runtime/src/lib/redact.ts, which must agree),
// since the runtime is not trusted to. A value is redacted when its key
// clearly names a credential (isSecretKey) or when it looks like one
// (isSecretValue), and reads "[redacted, N characters]" (N: the string's
// length, or its JSON's for another value); a value already redacted so is
// kept. Strings are cut at 2,000 characters, arrays and objects at 100 items,
// nesting at 6 levels; a view whose JSON is still over 16 KiB becomes
// {"[truncated]": "<its first 2,000 characters>"}.

const (
	maxArgString      = 2000
	maxArgItems       = 100
	maxArgDepth       = 6
	maxArgumentsBytes = 16 << 10
)

var (
	// Keys (lower case, letters and digits only) that name a credential,
	// whole or as their end. Names that only might (key, auth, headers,
	// signature) are left to the value checks: an S3 object's key is not a
	// secret.
	secretExact = map[string]bool{
		"authorization": true, "proxyauthorization": true, "cookie": true, "setcookie": true, "credentials": true,
		"credential": true, "bearer": true, "sessionid": true, "xapikey": true,
	}
	secretSuffix = []string{"password", "passwd", "passphrase", "secret", "apikey", "secretkey", "privatekey",
		"secretaccesskey", "token"}
	// Names ending in "token" that are not credentials (pagination).
	notSecret = map[string]bool{"pagetoken": true, "nextpagetoken": true, "prevpagetoken": true, "previouspagetoken": true,
		"continuationtoken": true, "synctoken": true, "nexttoken": true, "cursortoken": true}

	// Values that look like credentials: known token prefixes (the
	// platform's own pdp…_ and pdpm. ones, OpenAI and Stripe sk-/sk_, GitHub,
	// GitLab, Slack, AWS key IDs, Google API keys, Hugging Face, npm), an
	// Authorization header's value, a JWT, a PEM private key, and a long
	// random-looking string (32-512 characters of base64 or base64url with
	// upper and lower case letters and digits, and at least 4 bits of
	// entropy per character).
	secretPrefixRE = regexp.MustCompile(`^(?:pdp[a-z]{0,3}[_.]|sk-|sk_(?:live|test)_|rk_(?:live|test)_|gh[pousr]_|github_pat_|glpat-|xox[abeprs]-|AKIA|ASIA|AIza|hf_|npm_)[A-Za-z0-9_.\-]{16,}$`)
	authValueRE    = regexp.MustCompile(`(?i)^(?:bearer|basic|token)\s+\S{8,}$`)
	jwtRE          = regexp.MustCompile(`^eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*$`)
	randomRE       = regexp.MustCompile(`^[A-Za-z0-9+/=_-]{32,512}$`)
	redactedRE     = regexp.MustCompile(`^\[redacted(?:, [0-9]{1,9} characters?)?\]$`)
)

// isSecretKey reports whether an argument's key names a credential
// (Authorization, api_key, clientSecret, refresh-token, password, ...).
func isSecretKey(key string) bool {
	var b strings.Builder
	for _, r := range strings.ToLower(key) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	k := b.String()
	if secretExact[k] {
		return true
	}
	if notSecret[k] {
		return false
	}
	for _, s := range secretSuffix {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}

// isSecretValue reports whether a string looks like a credential (see the
// patterns above). It is conservative: identifiers, paths, hashes in one
// case, UUIDs and prose are shown.
func isSecretValue(v string) bool {
	v = strings.TrimSpace(v)
	if secretPrefixRE.MatchString(v) || authValueRE.MatchString(v) || jwtRE.MatchString(v) {
		return true
	}
	if strings.Contains(v, "-----BEGIN") && strings.Contains(v, "PRIVATE KEY") {
		return true
	}
	return randomRE.MatchString(v) && strings.ContainsAny(v, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") &&
		strings.ContainsAny(v, "abcdefghijklmnopqrstuvwxyz") && strings.ContainsAny(v, "0123456789") && entropy(v) >= 4
}

// entropy is a string's Shannon entropy in bits per character.
func entropy(s string) float64 {
	counts := map[rune]int{}
	n := 0
	for _, r := range s {
		counts[r]++
		n++
	}
	h := 0.0
	for _, c := range counts {
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}

// redactedMark is what a redacted value reads: its length in characters
// (a string's, or its JSON's), never any of it. One already redacted is kept.
func redactedMark(v any) string {
	if s, ok := v.(string); ok && redactedRE.MatchString(s) {
		return s
	}
	var n int
	if s, ok := v.(string); ok {
		n = utf8.RuneCountInString(s)
	} else {
		b, _ := json.Marshal(v)
		n = utf8.RuneCount(b)
	}
	if n == 1 {
		return "[redacted, 1 character]"
	}
	return fmt.Sprintf("[redacted, %d characters]", n)
}

// redactArguments returns the bounded, redacted view of raw JSON arguments
// (an empty object for anything that isn't JSON).
func redactArguments(raw json.RawMessage) json.RawMessage {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return json.RawMessage(`{}`)
	}
	view := redactValue(v, 0)
	if _, ok := view.(map[string]any); !ok {
		view = map[string]any{"value": view}
	}
	out, err := json.Marshal(view)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	if len(out) > maxArgumentsBytes {
		cut := string(out)
		if utf8.RuneCountInString(cut) > maxArgString {
			cut = string([]rune(cut)[:maxArgString])
		}
		out, _ = json.Marshal(map[string]string{"[truncated]": cut})
	}
	return out
}

func redactValue(v any, depth int) any {
	switch x := v.(type) {
	case nil, bool:
		return x
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			return nil
		}
		return x
	case string:
		if isSecretValue(x) {
			return redactedMark(x)
		}
		if utf8.RuneCountInString(x) > maxArgString {
			return string([]rune(x)[:maxArgString]) + "…"
		}
		return x
	}
	if depth >= maxArgDepth {
		return "[…]"
	}
	switch x := v.(type) {
	case []any:
		n := min(len(x), maxArgItems)
		out := make([]any, 0, n+1)
		for _, item := range x[:n] {
			out = append(out, redactValue(item, depth+1))
		}
		if len(x) > maxArgItems {
			out = append(out, fmt.Sprintf("[… %d more]", len(x)-maxArgItems))
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := map[string]any{}
		for i, k := range keys {
			if i == maxArgItems {
				out["[…]"] = fmt.Sprintf("%d more keys", len(keys)-maxArgItems)
				break
			}
			name := k
			if utf8.RuneCountInString(name) > 200 {
				name = string([]rune(name)[:200]) + "…"
			}
			if isSecretKey(k) {
				out[name] = redactedMark(x[k])
			} else {
				out[name] = redactValue(x[k], depth+1)
			}
		}
		return out
	}
	return nil
}
