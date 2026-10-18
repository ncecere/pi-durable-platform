package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
)

const fakeProviderKey = "sk-fake-provider-key-for-tests"

// fakeProvider is an OpenAI chat-completions server: it lists two models and
// streams one reply, reporting usage in its last chunk.
type fakeProvider struct {
	srv    *httptest.Server
	mu     sync.Mutex
	auth   []string
	models []string
}

func newFakeProvider(t *testing.T) *fakeProvider {
	f := &fakeProvider{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/models":
			if r.Header.Get("Authorization") != "Bearer "+fakeProviderKey {
				http.Error(w, "unauthorized", 401)
				return
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"fake-1"},{"id":"fake-2","context_length":32768}]}`)
		case r.Method == "POST" && r.URL.Path == "/v1/chat/completions":
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.auth, f.models = append(f.auth, r.Header.Get("Authorization")), append(f.models, body.Model)
			f.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			chunk := func(delta, finish string) string {
				fin := "null"
				if finish != "" {
					fin = `"` + finish + `"`
				}
				return fmt.Sprintf(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":%q,"choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`,
					body.Model, delta, fin)
			}
			for _, data := range []string{
				chunk(`{"role":"assistant","content":"Hello from the "}`, ""),
				chunk(`{"content":"fake provider"}`, ""),
				chunk(`{}`, "stop"),
				fmt.Sprintf(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":%q,"choices":[],"usage":{"prompt_tokens":42,"completion_tokens":7,"total_tokens":49,"prompt_tokens_details":{"cached_tokens":10}}}`, body.Model),
				"[DONE]",
			} {
				fmt.Fprintf(w, "data: %s\n\n", data)
				w.(http.Flusher).Flush()
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProvider) calls() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auth...), append([]string(nil), f.models...)
}

// The model catalog end to end: a platform admin adds a provider (proxy
// mode) and a model published to one team; that team's template and agent
// run on it through the platform's model proxy, which adds the provider's
// key and records usage; the agent can call nothing else; disabling the
// model stops the agent from starting; in direct mode the agent calls the
// provider itself.
func TestModelCatalog(t *testing.T) {
	e := newTeamEnv(t)
	fake := newFakeProvider(t)

	// Only platform admins manage providers; auditors read them.
	body := map[string]any{"name": "Fake gateway", "kind": "openai-compatible", "baseUrl": fake.srv.URL + "/v1/", "apiKey": fakeProviderKey}
	code, ec := e.editor.call("POST", "/v1/admin/model-providers", body, nil, nil)
	mustCode(t, "an editor adds a provider", code, ec, 403, "")
	var prov apitypes.ModelProvider
	code, raw := e.admin.raw("POST", "/v1/admin/model-providers", body, nil)
	if code != 201 || strings.Contains(string(raw), fakeProviderKey) {
		t.Fatalf("create provider: %d %s", code, raw)
	}
	mustDecode(t, raw, &prov)
	if prov.AccessMode != "proxy" || prov.BaseUrl != fake.srv.URL+"/v1" || prov.Kind != "openai-compatible" {
		t.Fatalf("provider %+v", prov)
	}
	var provs []apitypes.ModelProvider
	if code := e.auditor.get("/v1/admin/model-providers", &provs); code != 200 || len(provs) != 1 {
		t.Fatalf("auditor lists providers: %d %+v", code, provs)
	}
	code, ec = e.auditor.call("POST", "/v1/admin/model-providers/"+prov.Id.String()+"/discover", nil, nil, nil)
	mustCode(t, "an auditor discovers", code, ec, 403, "")
	code, ec = e.admin.call("POST", "/v1/admin/model-providers", body, nil, nil)
	mustCode(t, "a second provider with the same name", code, ec, 409, "provider_name_taken")
	bad := map[string]any{"name": "Bad", "kind": "openai-compatible", "baseUrl": "file:///etc/passwd", "apiKey": "k"}
	code, ec = e.admin.call("POST", "/v1/admin/model-providers", bad, nil, nil)
	mustCode(t, "a non-http base URL", code, ec, 400, "invalid_base_url")

	// Discovery lists what the provider offers.
	var found []apitypes.DiscoveredModel
	code, ec = e.admin.call("POST", "/v1/admin/model-providers/"+prov.Id.String()+"/discover", nil, &found, nil)
	mustCode(t, "discover", code, ec, 200, "")
	if len(found) != 2 || found[1].Id != "fake-2" || found[1].ContextWindow == nil || *found[1].ContextWindow != 32768 {
		t.Fatalf("discovered %+v", found)
	}

	// fake-1 is published to team tools only; fake-2 to everyone.
	var m1, m2 apitypes.Model
	code, ec = e.admin.call("POST", "/v1/admin/model-providers/"+prov.Id.String()+"/models",
		map[string]any{"upstreamId": "fake-1", "name": "Fake One", "visibility": "teams", "teams": []string{"tools"}, "inputPrice": 1.5}, &m1, nil)
	mustCode(t, "add fake-1", code, ec, 201, "")
	code, ec = e.admin.call("POST", "/v1/admin/model-providers/"+prov.Id.String()+"/models", map[string]any{"upstreamId": "fake-2"}, &m2, nil)
	mustCode(t, "add fake-2", code, ec, 201, "")
	if m1.Teams == nil || len(*m1.Teams) != 1 || (*m1.Teams)[0].Slug != "tools" || m2.Name != "fake-2" || m2.Visibility != "all" {
		t.Fatalf("models %+v / %+v", m1, m2)
	}
	var mine, theirs []apitypes.Model
	e.member.get(toolsTeam+"/models", &mine)
	e.outsider.get("/v1/teams/other/models", &theirs)
	if len(mine) != 2 || len(theirs) != 1 || theirs[0].Id != m2.Id {
		t.Fatalf("team models: tools %d, other %+v", len(mine), theirs)
	}
	code, ec = e.outsider.call("GET", toolsTeam+"/models", nil, nil, nil)
	mustCode(t, "another team's models", code, ec, 404, "")

	// Another team cannot use fake-1, in a template or an agent.
	code, ec = e.outsider.call("POST", "/v1/teams/other/agent-templates",
		map[string]any{"slug": "x", "name": "X", "instructions": "Hi.", "modelId": m1.Id}, nil, nil)
	mustCode(t, "another team's template on fake-1", code, ec, 409, "model_unavailable")
	code, ec = e.outsider.call("POST", "/v1/teams/other/agents", map[string]any{"name": "X", "modelId": m1.Id}, nil, nil)
	mustCode(t, "another team's agent on fake-1", code, ec, 409, "model_unavailable")

	// A team template on fake-1, and an agent from it.
	var tpl apitypes.AgentTemplate
	code, ec = e.editor.call("POST", toolsTeam+"/agent-templates",
		map[string]any{"slug": "real", "name": "Real", "instructions": "Be brief.", "modelId": m1.Id}, &tpl, nil)
	mustCode(t, "a template on fake-1", code, ec, 201, "")
	var versions []apitypes.AgentTemplateVersion
	e.member.get("/v1/agent-templates/"+tpl.Id.String()+"/versions", &versions)
	if len(versions) != 1 || versions[0].ModelName == nil || *versions[0].ModelName != "Fake One" {
		t.Fatalf("versions %+v", versions)
	}
	var ag apitypes.Agent
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Real bot", "templateId": tpl.Id}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	if ag.Model == nil || ag.Model.Id != m1.Id || ag.Model.Source != "template" || !ag.Model.Available {
		t.Fatalf("agent model %+v", ag.Model)
	}
	base := "/v1/agents/" + ag.Id.String()

	// The agent's runtime gets the platform's gateway and its own credential, never the provider's key.
	spec := e.spec(ag.Id)
	if spec.Env["MODEL_BASE_URL"] != e.app.GatewayURL+"/model-proxy/"+prov.Id.String() || spec.Env["MODEL_API"] != "openai-completions" ||
		!strings.HasPrefix(spec.Env["MODEL_API_KEY"], "pdpm."+ag.Id.String()+".") || strings.Contains(fmt.Sprint(spec.Env), fakeProviderKey) {
		t.Fatalf("runtime env %v", spec.Env)
	}

	// A conversation goes through the proxy: the provider sees its own key and the model.
	code, ec = e.member.call("POST", base+"/threads/t1/messages", map[string]any{"text": "hello"}, nil, nil)
	mustCode(t, "send", code, ec, 202, "")
	v := waitReply(t, e.member, base+"/threads/t1", 2)
	if got := text(v.Messages[1]); got != "Hello from the fake provider" {
		t.Fatalf("reply %q (%+v)", got, v.Messages[1])
	}
	auths, models := fake.calls()
	if len(auths) != 1 || auths[0] != "Bearer "+fakeProviderKey || models[0] != "fake-1" {
		t.Fatalf("provider saw %v %v", auths, models)
	}
	if got := e.scalar(`SELECT format('%s/%s/%s/%s', input_tokens, output_tokens, cache_read_tokens, source) FROM model_usage
		WHERE agent_id = $1`, ag.Id); got != "42/7/10/proxy" {
		t.Fatalf("usage %q", got)
	}

	// The credential works for the agent's own model only.
	gw := spec.Env["MODEL_BASE_URL"]
	cred := spec.Env["MODEL_API_KEY"]
	for _, c := range []struct {
		what, path, key, model string
		want                   int
	}{
		{"no credential", "/chat/completions", "", "fake-1", 401},
		{"a wrong token", "/chat/completions", "pdpm." + ag.Id.String() + ".wrong", "fake-1", 401},
		{"another model", "/chat/completions", cred, "fake-2", 403},
		{"another endpoint", "/responses", cred, "fake-1", 403},
		{"another provider", "", cred, "fake-1", 403},
	} {
		url := gw + c.path
		if c.what == "another provider" {
			url = e.app.GatewayURL + "/model-proxy/" + uuid.NewString() + "/chat/completions"
		}
		req, _ := http.NewRequest("POST", url, bytes.NewReader([]byte(`{"model":"`+c.model+`","messages":[]}`)))
		if c.key != "" {
			req.Header.Set("Authorization", "Bearer "+c.key)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != c.want {
			t.Errorf("%s: %d, want %d", c.what, res.StatusCode, c.want)
		}
	}

	// A model in use cannot be deleted; disabled, the agent doesn't start.
	code, ec = e.admin.call("DELETE", "/v1/admin/models/"+m1.Id.String(), nil, nil, nil)
	mustCode(t, "delete a model in use", code, ec, 409, "model_in_use")
	code, ec = e.admin.call("PATCH", "/v1/admin/models/"+m1.Id.String(), map[string]any{"enabled": false}, &m1, ifMatch(m1.Revision))
	mustCode(t, "disable fake-1", code, ec, 200, "")
	e.member.call("POST", base+"/hibernate", nil, nil, nil)
	e.member.get(base, &ag)
	if ag.Model == nil || ag.Model.Available {
		t.Fatalf("disabled model shown as %+v", ag.Model)
	}
	code, ec = e.member.call("POST", base+"/threads/t1/messages", map[string]any{"text": "still there?"}, nil, nil)
	mustCode(t, "send on a disabled model", code, ec, 409, "agent_model_unavailable")

	// The agent can move to another model, and back to its template's.
	code, ec = e.editor.call("PATCH", base, map[string]any{"modelId": m2.Id.String()}, &ag, ifMatch(ag.Revision))
	mustCode(t, "choose fake-2", code, ec, 200, "")
	if ag.Model == nil || ag.Model.Id != m2.Id || ag.Model.Source != "agent" {
		t.Fatalf("after choosing fake-2: %+v", ag.Model)
	}
	code, ec = e.admin.call("PATCH", "/v1/admin/models/"+m1.Id.String(), map[string]any{"enabled": true}, &m1, ifMatch(m1.Revision))
	mustCode(t, "enable fake-1", code, ec, 200, "")
	code, ec = e.editor.call("PATCH", base, map[string]any{"modelId": ""}, &ag, ifMatch(ag.Revision))
	mustCode(t, "back to the template's model", code, ec, 200, "")
	if ag.Model == nil || ag.Model.Id != m1.Id || ag.Model.Source != "template" {
		t.Fatalf("after clearing: %+v", ag.Model)
	}

	// Direct mode: the agent gets the provider's URL and key and calls it itself.
	code, ec = e.admin.call("PATCH", "/v1/admin/model-providers/"+prov.Id.String(), map[string]any{"accessMode": "direct"}, &prov, ifMatch(prov.Revision))
	mustCode(t, "switch to direct", code, ec, 200, "")
	spec = e.spec(ag.Id)
	if spec.Env["MODEL_BASE_URL"] != fake.srv.URL+"/v1" || spec.Env["MODEL_API_KEY"] != fakeProviderKey {
		t.Fatalf("direct env %v", spec.Env)
	}
	code, ec = e.member.call("POST", base+"/threads/t2/messages", map[string]any{"text": "direct"}, nil, nil)
	mustCode(t, "send directly", code, ec, 202, "")
	if v := waitReply(t, e.member, base+"/threads/t2", 2); text(v.Messages[1]) != "Hello from the fake provider" {
		t.Fatalf("direct reply %+v", v.Messages[1])
	}
	if auths, _ := fake.calls(); len(auths) != 2 || auths[1] != "Bearer "+fakeProviderKey {
		t.Fatalf("provider saw %v", auths)
	}

	// Every change is audited, without the key.
	if got := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action LIKE 'model%' AND (coalesce(before_state::text, '') || coalesce(after_state::text, '')) NOT LIKE '%' || $1 || '%'`,
		fakeProviderKey); got != "6" {
		t.Fatalf("audited model changes: %s", got)
	}
}

// spec is an agent's runtime spec, as its team's member would wake it.
func (e *teamEnv) spec(agentID uuid.UUID) struct{ Env map[string]string } {
	e.t.Helper()
	member := uuid.MustParse(e.scalar(`SELECT id::text FROM users WHERE email = 'blair@localhost'`))
	s, err := e.app.Agents.Spec(context.Background(), authz.Actor{UserID: member, PlatformRole: authz.PlatformNone}, agentID)
	if err != nil {
		e.t.Fatal(err)
	}
	return struct{ Env map[string]string }{s.Env}
}

func mustDecode(t *testing.T, raw []byte, out any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		t.Fatal(err)
	}
}
