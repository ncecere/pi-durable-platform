package agents

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// The arguments of a tool call that asks for approval, as the people who
// decide see them: the runtime sends a redacted view, and the platform
// applies the same rules again (runtime/src/lib/redact.ts), since the
// runtime is not trusted to. Values under keys that look like credentials or
// headers read "[redacted]"; strings are cut at 2,000 characters, arrays and
// objects at 100 items, nesting at 6 levels; a view whose JSON is still over
// 16 KiB becomes {"[truncated]": "<its first 2,000 characters>"}.

const (
	redacted          = "[redacted]"
	maxArgString      = 2000
	maxArgItems       = 100
	maxArgDepth       = 6
	maxArgumentsBytes = 16 << 10
)

var (
	secretContains = []string{"authorization", "cookie", "password", "passwd", "passphrase", "credential", "bearer", "privatekey"}
	secretSuffix   = []string{"token", "secret", "apikey", "accesskey", "secretkey", "signature", "sessionid"}
	secretExact    = map[string]bool{"headers": true, "header": true, "key": true, "auth": true, "xapikey": true}
)

// isSecretKey reports whether an argument's key names a secret
// (Authorization, api_key, clientSecret, refresh-token, headers, ...).
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
	for _, s := range secretContains {
		if strings.Contains(k, s) {
			return true
		}
	}
	for _, s := range secretSuffix {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
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
				out[name] = redacted
			} else {
				out[name] = redactValue(x[k], depth+1)
			}
		}
		return out
	}
	return nil
}
