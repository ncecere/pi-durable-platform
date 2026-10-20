package httpapi_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/teamnet"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
	"github.com/ncecere/pi-durable-platform/internal/triggers"
)

// A webhook starts a run end to end, with a real agent runtime: the signed
// delivery, the message (whose author is the trigger, not a person), the
// run's end as the runtime reports it, the Runs API, and the signed
// completion callback. Only owners and admins in a browser session manage
// triggers; moving the agent deletes them.
func TestTriggersEndToEnd(t *testing.T) {
	e := newTeamEnv(t)
	app := e.app
	var mu sync.Mutex
	var callbacks [][]byte
	var sigs []string
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		callbacks, sigs = append(callbacks, b), append(sigs, r.Header.Get(triggers.SignatureHeader))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(receiver.Close)
	app.Triggers.SetTeamNetwork(teamnet.Config{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		},
		Connect: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, receiver.Listener.Addr().String())
		},
		TLS: &tls.Config{RootCAs: receiver.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs, ServerName: "example.com"},
	})

	var ag apitypes.Agent
	code, ec := e.owner.call("POST", toolsTeam+"/agents", map[string]any{"name": "Triage"}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	base := "/v1/agents/" + ag.Id.String()
	body := map[string]any{"kind": "webhook", "name": "New issue", "messageTemplate": "Triage {{payload.title}}",
		"filter": map[string]any{"path": "action", "equals": "opened"}, "callbackUrl": "https://hooks.example.com/done"}

	// Editors, and the owner with a key, can't; the owner in the console can.
	code, ec = e.editor.call("POST", base+"/triggers", body, nil, nil)
	mustCode(t, "an editor", code, ec, 403, "forbidden")
	var key apitypes.ApiKeyCreated
	code, ec = e.owner.call("POST", "/v1/me/api-keys", map[string]any{"name": "script"}, &key, nil)
	mustCode(t, "api key", code, ec, 201, "")
	code, _ = app.do("POST", base+"/triggers", body, map[string]string{"Authorization": "Bearer " + key.Secret})
	if code != 401 {
		t.Fatalf("with a personal API key: %d", code)
	}
	var saved apitypes.TriggerSaved
	code, ec = e.owner.call("POST", base+"/triggers", body, &saved, nil)
	mustCode(t, "create", code, ec, 201, "")
	if saved.WebhookSecret == nil || saved.CallbackSecret == nil || saved.Trigger.WebhookUrl == nil {
		t.Fatalf("saved = %+v", saved)
	}
	// Members read it, without its secrets.
	var listed []apitypes.Trigger
	if code := e.member.get(base+"/triggers", &listed); code != 200 || len(listed) != 1 {
		t.Fatalf("list = %d %+v", code, listed)
	}
	raw, _ := json.Marshal(listed)
	if bytes.Contains(raw, []byte(*saved.WebhookSecret)) || bytes.Contains(raw, []byte(*saved.CallbackSecret)) {
		t.Fatal("a listing shows a secret")
	}

	hook := strings.TrimPrefix(*saved.Trigger.WebhookUrl, app.URL)
	deliver := func(sig string, payload string) (int, apitypes.WebhookAccepted, string) {
		req, _ := http.NewRequest("POST", app.URL+hook, strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		if sig != "" {
			req.Header.Set(triggers.SignatureHeader, sig)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		var env struct{ Data apitypes.WebhookAccepted }
		_ = json.Unmarshal(out, &env)
		return res.StatusCode, env.Data, errorCode(out)
	}
	payload := `{"action":"opened","title":"Login fails"}`
	if code, _, ec := deliver("", payload); code != 401 || ec != "invalid_signature" {
		t.Fatalf("unsigned: %d %s", code, ec)
	}
	if code, _, ec := deliver(triggers.Sign("pdpwh_guess", time.Now(), []byte(payload)), payload); code != 401 || ec != "invalid_signature" {
		t.Fatalf("wrong secret: %d %s", code, ec)
	}
	if code, acc, _ := deliver(triggers.Sign(*saved.WebhookSecret, time.Now(), []byte(`{"action":"closed"}`)), `{"action":"closed"}`); code != 202 || acc.Matched {
		t.Fatalf("filtered: %d %+v", code, acc)
	}
	sig := triggers.Sign(*saved.WebhookSecret, time.Now(), []byte(payload))
	code, acc, ec := deliver(sig, payload)
	if code != 202 || !acc.Matched || acc.RunId == nil || acc.Replayed {
		t.Fatalf("delivery: %d %+v %s", code, acc, ec)
	}
	if code, again, _ := deliver(sig, payload); code != 202 || !again.Replayed || *again.RunId != *acc.RunId {
		t.Fatalf("replay: %d %+v", code, again)
	}

	// The run ends when the runtime reports the reply's end; then its callback goes.
	var run apitypes.TriggerRun
	deadline := time.Now().Add(60 * time.Second)
	for {
		app.Triggers.Wait()
		if err := app.Triggers.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		if code := e.member.get(base+"/runs/"+acc.RunId.String(), &run); code != 200 {
			t.Fatalf("run = %d", code)
		}
		if run.Status == "succeeded" && run.CallbackStatus != nil && *run.CallbackStatus == "delivered" {
			break
		}
		if time.Now().After(deadline) || run.Status == "failed" {
			t.Fatalf("run = %+v", run)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if run.ThreadId == nil || run.Link == nil || !strings.Contains(*run.Link, "thread="+*run.ThreadId) || run.FiredBy != "webhook" {
		t.Fatalf("run = %+v", run)
	}
	// The thread: shared, started by nobody, its message's author the trigger.
	view := waitReply(t, e.member, base+"/threads/"+*run.ThreadId, 2)
	first := view.Messages[0]
	if first.Author == nil || first.Author.Kind != "trigger" || first.Author.Name != "New issue" || text(first) != "Triage Login fails" {
		t.Fatalf("first message = %+v", first)
	}
	thread := waitThread(t, e.member, base, *run.ThreadId, func(apitypes.AgentThread) bool { return true })
	if thread.CreatedBy != nil || thread.Visibility != "shared" || len(thread.Participants) != 0 {
		t.Errorf("thread = %+v", thread)
	}
	// Listed for the agent and the team.
	var runs []apitypes.TriggerRun
	if code := e.member.get(base+"/runs", &runs); code != 200 || len(runs) != 1 {
		t.Errorf("agent runs = %d %+v", code, runs)
	}
	if code := e.member.get(toolsTeam+"/runs?status=succeeded", &runs); code != 200 || len(runs) != 1 {
		t.Errorf("team runs = %d %+v", code, runs)
	}
	if code, _ := e.outsider.call("GET", toolsTeam+"/runs", nil, nil, nil); code != 404 {
		t.Errorf("an outsider's team runs = %d", code)
	}
	// The callback: signed with the callback secret, a summary, never the conversation.
	mu.Lock()
	if len(callbacks) != 1 {
		t.Fatalf("%d callbacks", len(callbacks))
	}
	cb, cbSig := callbacks[0], sigs[0]
	mu.Unlock()
	ts := strings.TrimPrefix(strings.Split(cbSig, ",")[0], "t=")
	at, _ := time.Parse(time.RFC3339, "1970-01-01T00:00:00Z")
	var sec int64
	for _, c := range ts {
		sec = sec*10 + int64(c-'0')
	}
	if cbSig != triggers.Sign(*saved.CallbackSecret, at.Add(time.Duration(sec)*time.Second), cb) {
		t.Errorf("callback signature %s doesn't check", cbSig)
	}
	var got triggers.Callback
	if err := json.Unmarshal(cb, &got); err != nil || got.Status != "succeeded" || got.Excerpt == nil || *got.Excerpt == "" ||
		got.RunID.String() != acc.RunId.String() {
		t.Errorf("callback %s (%v)", cb, err)
	}
	// A summary: these fields and no others (the excerpt is the reply's start, at most 500 characters).
	var fields map[string]any
	_ = json.Unmarshal(cb, &fields)
	for k := range fields {
		if !strings.Contains(" type runId triggerId triggerName agentId threadId status reason scheduledFor startedAt finishedAt costUsd excerpt link ", " "+k+" ") {
			t.Errorf("the callback carries %s", k)
		}
	}
	if got.Excerpt != nil && len([]rune(*got.Excerpt)) > triggers.ExcerptLength {
		t.Errorf("an excerpt of %d characters", len([]rune(*got.Excerpt)))
	}
	// Audited, naming the trigger.
	if n := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action = 'trigger.run' AND metadata->>'triggerName' = 'New issue'`); n != "1" {
		t.Errorf("%s trigger.run entries", n)
	}

	// Moving the agent deletes its triggers (and the move plan says so).
	createTeam(t, e.admin, "third", "user@localhost")
	var plan apitypes.AgentMovePlan
	if code := e.owner.get(base+"/move?team=third", &plan); code != 200 || plan.Triggers == nil || *plan.Triggers != 1 {
		t.Fatalf("plan = %d %+v", code, plan.Triggers)
	}
	var cur apitypes.Agent
	e.owner.get(base, &cur)
	code, ec = e.owner.call("POST", base+"/move", map[string]any{"team": "third"}, nil, ifMatch(cur.Revision))
	mustCode(t, "move", code, ec, 200, "")
	if code := e.owner.get(base+"/triggers", &listed); code != 200 || len(listed) != 0 {
		t.Errorf("triggers after the move = %d %+v", code, listed)
	}
	if code, _, ec := deliver(triggers.Sign(*saved.WebhookSecret, time.Now(), []byte(payload)), payload); code != 404 || ec != "hook_not_found" {
		t.Errorf("the moved agent's webhook: %d %s", code, ec)
	}
}

// Only verified deliveries count against a webhook address's limit: someone
// with the address but not the secret, flooding it with bad signatures from
// their own client address, is limited on their own and never keeps the
// real sender (another client address, X-Forwarded-For from a trusted
// proxy) out.
func TestWebhookBadSignaturesDontSpendTheSendersLimit(t *testing.T) {
	app := newTestAppWith(t, func(c *config.Config) {
		c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	}, testutil.Logger(), nil, false)
	e := newTeamEnvOn(t, app)
	var ag apitypes.Agent
	code, ec := e.owner.call("POST", toolsTeam+"/agents", map[string]any{"name": "Hooked"}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	// A filter that never matches: deliveries are verified, and start no run.
	var saved apitypes.TriggerSaved
	code, ec = e.owner.call("POST", "/v1/agents/"+ag.Id.String()+"/triggers", map[string]any{"kind": "webhook", "name": "Issues",
		"messageTemplate": "x", "filter": map[string]any{"path": "action", "equals": "never"}}, &saved, nil)
	mustCode(t, "create", code, ec, 201, "")
	hook := *saved.Trigger.WebhookUrl
	deliver := func(from, sig string) int {
		t.Helper()
		req, _ := http.NewRequest("POST", hook, strings.NewReader(`{"action":"opened"}`))
		req.Header.Set("X-Forwarded-For", from)
		if sig != "" {
			req.Header.Set(triggers.SignatureHeader, sig)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res.StatusCode
	}
	good := func() string { return triggers.Sign(*saved.WebhookSecret, time.Now(), []byte(`{"action":"opened"}`)) }
	bad := triggers.Sign("pdpwh_guess", time.Now(), []byte(`{"action":"opened"}`))

	// The flood: within two one-minute windows, one of them goes over the attacker's limit.
	limited := false
	for i := 0; i < 481 && !limited; i++ {
		switch code := deliver("203.0.113.66", bad); code {
		case 401:
		case 429:
			limited = true
		default:
			t.Fatalf("a bad signature: %d", code)
		}
	}
	if !limited {
		t.Fatal("a flood of bad signatures was never limited")
	}
	// The real sender, from its own address, is let through; so is a bad signature of its own (not 429).
	if code := deliver("198.51.100.7", good()); code != 202 {
		t.Fatalf("the real sender after the flood: %d", code)
	}
	if code := deliver("198.51.100.7", ""); code != 401 {
		t.Fatalf("an unsigned request from another address: %d", code)
	}
}
