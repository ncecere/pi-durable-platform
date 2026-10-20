package triggers

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTemplateCheck(t *testing.T) {
	for tpl, webhook := range map[string]bool{
		"Daily digest":                                  false,
		"{{trigger.name}} at {{ fire.time }}":           false,
		"New issue {{payload.issue.title}} {{payload}}": true,
		"{{payload.items.0.id}} ({{fire.id}})":          true,
	} {
		if err := checkTemplate(tpl, webhook); err != nil {
			t.Errorf("%q: %v", tpl, err)
		}
	}
	for tpl, webhook := range map[string]bool{
		"":                                 true,
		"   ":                              true,
		"{{payload.title}}":                false, // a schedule has no payload
		"{{env.HOME}}":                     true,
		"{{trigger.secret}}":               true,
		"{{payload.a..b}}":                 true,
		"{{payload.a b}}":                  true,
		"{{ exec \"rm\" }}":                true,
		strings.Repeat("x", 8001):          true,
		strings.Repeat("{{fire.id}}", 101): true,
		"{{payload." + strings.Repeat("a.", 10) + "a}}": true,
	} {
		if err := checkTemplate(tpl, webhook); err == nil {
			t.Errorf("%q accepted", tpl)
		}
	}
}

func TestRenderInsertsTextOnly(t *testing.T) {
	body := []byte(`{"issue":{"title":"Crash {{trigger.name}}","n":42,"labels":["bug"],"ok":true}}`)
	payload, ok := decodePayload(body)
	if !ok {
		t.Fatal("payload not decoded")
	}
	at := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	v := fireVars{TriggerName: "Triage", TriggerKind: KindWebhook, FireID: "wh_x", FireTime: at, Payload: payload, PayloadText: string(body), HasPayload: true}
	got := render("{{trigger.name}}: {{payload.issue.title}} #{{payload.issue.n}} {{payload.issue.labels}} {{payload.issue.labels.0}} "+
		"{{payload.issue.ok}} [{{payload.missing}}] {{fire.time}} {{fire.id}}", v)
	want := `Triage: Crash {{trigger.name}} #42 ["bug"] bug true [] 2026-10-20T08:00:00Z wh_x`
	if got != want {
		t.Errorf("render = %q\nwant     %q", got, want)
	}
	// The whole payload, and a text body.
	if got := render("{{payload}}", v); got != `{"issue":{"labels":["bug"],"n":42,"ok":true,"title":"Crash {{trigger.name}}"}}` {
		t.Errorf("{{payload}} = %q", got)
	}
	text := fireVars{PayloadText: "plain words", HasPayload: true}
	if got := render("got: {{payload}} / {{payload.a}}", text); got != "got: plain words / " {
		t.Errorf("text payload = %q", got)
	}
	// Bounded: an insertion, and the message.
	huge := fireVars{PayloadText: strings.Repeat("a", 10_000), HasPayload: true}
	if got := render("{{payload}}", huge); len([]rune(got)) != maxInsert {
		t.Errorf("an insertion of %d characters", len([]rune(got)))
	}
	many := render(strings.Repeat("{{payload}}", 100), huge)
	if len([]rune(many)) != maxMessage {
		t.Errorf("a message of %d characters", len([]rune(many)))
	}
}

func TestFilter(t *testing.T) {
	payload, _ := decodePayload([]byte(`{"action":"opened","n":3,"draft":false,"pr":{"labels":[{"name":"bot"}]}}`))
	cases := []struct {
		f    Filter
		want bool
	}{
		{Filter{"action", "opened"}, true},
		{Filter{"action", "closed"}, false},
		{Filter{"n", "3"}, true},
		{Filter{"draft", "false"}, true},
		{Filter{"pr.labels.0.name", "bot"}, true},
		{Filter{"pr.labels.1.name", "bot"}, false},
		{Filter{"missing", ""}, false},
	}
	for _, c := range cases {
		if got := c.f.matches(payload, true); got != c.want {
			t.Errorf("%+v matches = %v", c.f, got)
		}
	}
	if (Filter{"action", "opened"}).matches(nil, false) {
		t.Error("a body that isn't JSON matched")
	}
	for _, bad := range []Filter{{"", "x"}, {"a..b", "x"}, {"a", strings.Repeat("x", 1001)}} {
		if checkFilter(bad) == nil {
			t.Errorf("filter %+v accepted", bad)
		}
	}
}

func TestSignature(t *testing.T) {
	now := time.Unix(1_760_000_000, 0)
	body := []byte(`{"a":1}`)
	header := Sign("pdpwh_secret", now, body)
	sig, ok := verify(header, "pdpwh_secret", body, now.Add(time.Minute))
	if !ok || !strings.Contains(header, sig) {
		t.Fatalf("a valid signature was refused: %s", header)
	}
	// Two signatures (a sender rotating): either may match.
	if _, ok := verify(header+",v1=00ff", "pdpwh_secret", body, now); !ok {
		t.Error("a second v1 broke a valid one")
	}
	for name, c := range map[string]struct {
		header, secret string
		body           []byte
		at             time.Time
	}{
		"wrong secret":    {header, "pdpwh_other", body, now},
		"changed body":    {header, "pdpwh_secret", []byte(`{"a":2}`), now},
		"too old":         {header, "pdpwh_secret", body, now.Add(Tolerance + time.Second)},
		"from the future": {header, "pdpwh_secret", body, now.Add(-Tolerance - time.Second)},
		"re-dated":        {strings.Replace(header, "t=1760000000", "t=1760000100", 1), "pdpwh_secret", body, now},
		"no time":         {header[strings.Index(header, ",")+1:], "pdpwh_secret", body, now},
		"no signature":    {"t=1760000000", "pdpwh_secret", body, now},
		"empty":           {"", "pdpwh_secret", body, now},
		"huge":            {header + strings.Repeat(",v1=0", 300), "pdpwh_secret", body, now},
	} {
		if _, ok := verify(c.header, c.secret, c.body, c.at); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFireIDs(t *testing.T) {
	id := uuid.MustParse("4c1a1f7e-0000-4000-8000-000000000001")
	at := time.Unix(1_760_000_000, 0)
	for _, fid := range []string{scheduleFireID(id, at), webhookFireID(id, "abc"), manualFireID()} {
		if !threadIDRE.MatchString(fid) {
			t.Errorf("fire ID %q is not a valid requestId", fid)
		}
	}
	if scheduleFireID(id, at) != scheduleFireID(id, at) || scheduleFireID(id, at) == scheduleFireID(id, at.Add(time.Minute)) {
		t.Error("schedule fire IDs are not one per scheduled time")
	}
	if webhookFireID(id, "abc") != webhookFireID(id, "abc") || webhookFireID(id, "abc") == webhookFireID(id, "abd") {
		t.Error("webhook fire IDs are not one per signature")
	}
	if manualFireID() == manualFireID() {
		t.Error("manual fire IDs repeat")
	}
}

func TestSchedule(t *testing.T) {
	s, err := parseSchedule("0 8 * * 1-5", "Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	// Monday 2026-10-19 07:00 Paris (05:00 UTC, summer time) -> 08:00 Paris = 06:00 UTC.
	from := time.Date(2026, 10, 19, 5, 0, 0, 0, time.UTC)
	if got := s.next(from); !got.Equal(time.Date(2026, 10, 19, 6, 0, 0, 0, time.UTC)) {
		t.Errorf("next = %v", got)
	}
	// After the change to winter time (25 October), 08:00 Paris is 07:00 UTC.
	if got := s.next(time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 10, 26, 7, 0, 0, 0, time.UTC)) {
		t.Errorf("next after DST = %v", got)
	}
	// The latest of several missed fire times.
	first := time.Date(2026, 10, 19, 6, 0, 0, 0, time.UTC)
	if got := s.lastBefore(first, time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 10, 22, 6, 0, 0, 0, time.UTC)) {
		t.Errorf("lastBefore = %v", got)
	}
	for expr, zone := range map[string]string{"@every 1m": "UTC", "61 * * * *": "UTC", "0 0 30 2 *": "UTC", "* * * * *": "Mars/Base",
		"0 8 * * *": "Local", "CRON_TZ=UTC 0 8 * * *": "UTC", "": "UTC", "* * * * * *": "UTC"} {
		if _, err := parseSchedule(expr, zone); err == nil {
			t.Errorf("%q in %q accepted", expr, zone)
		}
	}
	if _, err := parseSchedule("@daily", "UTC"); err != nil {
		t.Errorf("@daily: %v", err)
	}
}
