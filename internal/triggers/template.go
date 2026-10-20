package triggers

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Message templates and payload filters (docs/triggers.md). A template is
// text with placeholders that insert values: there are no loops, conditions,
// functions or escapes, so a payload can only ever become text.

const (
	maxTemplate     = 8000
	maxPlaceholders = 100
	maxPathDepth    = 10
	// maxInsert bounds one placeholder's value, maxMessage the message (the
	// runtime refuses longer ones).
	maxInsert  = 4000
	maxMessage = 32000
)

var (
	placeholderRE = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)
	segmentRE     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
)

// Placeholder roots.
const (
	rootPayload = "payload"
	rootTrigger = "trigger"
	rootFire    = "fire"
)

// fireVars are the values a fire offers its template.
type fireVars struct {
	TriggerName, TriggerKind, FireID string
	FireTime                         time.Time
	// Payload is the webhook's body decoded (nil: none, or not JSON);
	// PayloadText the body as sent.
	Payload     any
	PayloadText string
	HasPayload  bool
}

// parsePath splits a dotted path into segments (names or array indexes).
func parsePath(p string) ([]string, bool) {
	if p == "" {
		return nil, true
	}
	segs := strings.Split(p, ".")
	if len(segs) > maxPathDepth {
		return nil, false
	}
	for _, s := range segs {
		if !segmentRE.MatchString(s) {
			return nil, false
		}
	}
	return segs, true
}

// checkTemplate checks a message template: its length and that every
// placeholder names something a fire of this kind offers (payload only for
// webhooks).
func checkTemplate(tpl string, webhook bool) error {
	if strings.TrimSpace(tpl) == "" || utf8.RuneCountInString(tpl) > maxTemplate || strings.ContainsRune(tpl, 0) {
		return errBadTemplate
	}
	ms := placeholderRE.FindAllStringSubmatch(tpl, -1)
	if len(ms) > maxPlaceholders {
		return errBadTemplate
	}
	for _, m := range ms {
		root, rest, _ := strings.Cut(m[1], ".")
		switch root {
		case rootPayload:
			if !webhook {
				return errTemplatePayload
			}
			if _, ok := parsePath(rest); !ok {
				return errBadTemplate
			}
		case rootTrigger:
			if rest != "name" && rest != "kind" {
				return errBadTemplate
			}
		case rootFire:
			if rest != "id" && rest != "time" {
				return errBadTemplate
			}
		default:
			return errBadTemplate
		}
	}
	return nil
}

// render fills a (checked) template. A missing value is empty; each value
// is cut at maxInsert characters and the message at maxMessage.
func render(tpl string, v fireVars) string {
	out := placeholderRE.ReplaceAllStringFunc(tpl, func(m string) string {
		inner := strings.TrimSpace(m[2 : len(m)-2])
		root, rest, _ := strings.Cut(inner, ".")
		var val string
		switch root {
		case rootPayload:
			switch {
			case !v.HasPayload:
			case rest == "" && v.Payload == nil:
				val = v.PayloadText
			default:
				segs, _ := parsePath(rest)
				if x, ok := lookup(v.Payload, segs); ok {
					val = textOf(x)
				}
			}
		case rootTrigger:
			if rest == "name" {
				val = v.TriggerName
			} else {
				val = v.TriggerKind
			}
		case rootFire:
			if rest == "id" {
				val = v.FireID
			} else {
				val = v.FireTime.UTC().Format(time.RFC3339)
			}
		}
		return cut(val, maxInsert)
	})
	return cut(out, maxMessage)
}

// cut keeps at most n characters of s.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// lookup finds the value at a path in decoded JSON.
func lookup(v any, segs []string) (any, bool) {
	for _, s := range segs {
		switch x := v.(type) {
		case map[string]any:
			var ok bool
			if v, ok = x[s]; !ok {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(s)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			v = x[i]
		default:
			return nil, false
		}
	}
	return v, true
}

// textOf is a JSON value as text: a string as is, anything else as compact
// JSON.
func textOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// decodePayload decodes a webhook's body as JSON (numbers kept exact); ok is
// false when it isn't JSON.
func decodePayload(body []byte) (any, bool) {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil || d.More() {
		return nil, false
	}
	return v, true
}

// Filter is a webhook's condition: the field at Path equals Equals.
type Filter struct {
	Path, Equals string
}

func checkFilter(f Filter) error {
	segs, ok := parsePath(f.Path)
	if !ok || len(segs) == 0 || utf8.RuneCountInString(f.Equals) > 1000 {
		return errBadFilter
	}
	return nil
}

// matches reports whether a payload meets the filter: a string field equals
// the value, any other field's JSON text does (42, true, null).
func (f Filter) matches(payload any, isJSON bool) bool {
	if !isJSON {
		return false
	}
	segs, _ := parsePath(f.Path)
	v, ok := lookup(payload, segs)
	return ok && textOf(v) == f.Equals
}
