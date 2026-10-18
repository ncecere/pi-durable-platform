package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
)

// keyClient calls the API with a bearer key and no cookies.
type keyClient struct {
	t   *testing.T
	url string
	key string
}

func (k keyClient) call(method, path string, body, out any) (int, string) {
	k.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, k.url+path, rdr)
	req.Header.Set("Authorization", "Bearer "+k.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		k.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 200 && res.StatusCode < 300 && out != nil {
		env := struct{ Data any }{Data: out}
		if err := json.Unmarshal(raw, &env); err != nil {
			k.t.Fatalf("decode %s %s: %v: %s", method, path, err, raw)
		}
	}
	return res.StatusCode, errorCode(raw)
}

// Services reach agents with keys: a chat app's service account gives each
// of its users an agent; people reach their teams' agents with API keys.
func TestAgentsWithKeys(t *testing.T) {
	e := newTeamEnv(t)
	app := e.app
	chatApp := keyClient{t, app.URL, createSAKey(t, e.owner, createSA(t, e.owner, map[string]any{"name": "chat-app", "role": "editor"}).Id.String(), map[string]any{"name": "prod"}).Secret}
	reader := keyClient{t, app.URL, createSAKey(t, e.owner, createSA(t, e.owner, map[string]any{"name": "reader"}).Id.String(), map[string]any{"name": "k"}).Secret}

	// The chat app ensures one agent per user, idempotently.
	var ag, again apitypes.Agent
	code, ec := chatApp.call("PUT", toolsTeam+"/agents/by-key/user-42", map[string]any{"name": "Assistant for user 42"}, &ag)
	mustCode(t, "ensure by key", code, ec, 201, "")
	code, ec = chatApp.call("PUT", toolsTeam+"/agents/by-key/user-42", nil, &again)
	mustCode(t, "ensure again", code, ec, 200, "")
	if again.Id != ag.Id || ag.ExternalKey == nil || *ag.ExternalKey != "user-42" || ag.Name != "Assistant for user 42" {
		t.Fatalf("ensured %+v then %+v", ag, again)
	}
	base := "/v1/agents/" + ag.Id.String()

	// It talks to the agent with no session or CSRF token.
	code, ec = chatApp.call("POST", base+"/threads/c1/messages", map[string]any{"text": "hello"}, nil)
	mustCode(t, "send with a key", code, ec, 202, "")
	view := waitReplyKey(t, chatApp, base+"/threads/c1", 2)
	if len(view.Messages) < 2 {
		t.Fatalf("no reply: %+v", view)
	}

	// Platform admins don't see the titles of an app's users' conversations.
	var adminList []apitypes.AdminAgentThread
	if code := e.admin.get("/v1/admin/agents/"+ag.Id.String()+"/threads", &adminList); code != 200 || len(adminList) != 1 ||
		adminList[0].Title != nil || !adminList[0].TitleHidden {
		t.Fatalf("admin lists a service-managed agent's threads %d %+v", code, adminList)
	}

	// Another service account of the team may use it but not create agents.
	if code, _ := reader.call("GET", base+"/threads/c1", nil, nil); code != 200 {
		t.Fatalf("member service account reads: %d", code)
	}
	code, ec = reader.call("PUT", toolsTeam+"/agents/by-key/user-43", nil, nil)
	mustCode(t, "member service account ensures", code, ec, 403, "forbidden")

	// The team's people don't reach service-managed conversations. Owners and
	// admins see the agents (to manage them), not what's in them.
	for _, s := range []*session{e.owner, e.editor, e.member} {
		code, ec = s.call("GET", base+"/threads/c1", nil, nil, nil)
		mustCode(t, "a person reads a service-managed conversation", code, ec, 404, "agent_not_found")
	}
	if code := e.owner.get(base, nil); code != 200 {
		t.Fatalf("owner sees the agent: %d", code)
	}
	code, ec = e.editor.call("GET", base, nil, nil, nil)
	mustCode(t, "an editor sees a service-managed agent", code, ec, 404, "agent_not_found")
	var ownerList, editorList, plainList, serviceList []apitypes.Agent
	if code := e.owner.get(toolsTeam+"/agents?includeManaged=true", &ownerList); code != 200 || len(ownerList) != 1 {
		t.Fatalf("owner's managed list = %d %+v", code, ownerList)
	}
	if code := e.owner.get("/v1/agents?includeManaged=true", &ownerList); code != 200 || len(ownerList) != 1 {
		t.Fatalf("owner's agents with managed = %d %+v", code, ownerList)
	}
	if code := e.owner.get("/v1/agents", &plainList); code != 200 || len(plainList) != 0 {
		t.Fatalf("owner's agents = %d %+v", code, plainList)
	}
	if code := e.editor.get(toolsTeam+"/agents?includeManaged=true", &editorList); code != 200 || len(editorList) != 0 {
		t.Fatalf("editor's managed list = %d %+v", code, editorList)
	}
	if code, _ := chatApp.call("GET", toolsTeam+"/agents?includeManaged=true", nil, &serviceList); code != 200 || len(serviceList) != 1 {
		t.Fatalf("service list = %d %+v", code, serviceList)
	}
	code, ec = e.editor.call("PUT", toolsTeam+"/agents/by-key/user-44", nil, nil, nil)
	mustCode(t, "a person ensures by key", code, ec, 403, "forbidden")

	// People reach their teams' agents with personal API keys; service
	// accounts never reach another team's agents.
	var keyOut apitypes.ApiKeyCreated
	code, ec = e.editor.call("POST", "/v1/me/api-keys", map[string]any{"name": "script"}, &keyOut, nil)
	mustCode(t, "create API key", code, ec, 201, "")
	alexKey := keyClient{t, app.URL, keyOut.Secret}
	var deployed apitypes.Agent
	code, ec = alexKey.call("POST", toolsTeam+"/agents", map[string]any{"name": "Scripted"}, &deployed)
	mustCode(t, "deploy with an API key", code, ec, 201, "")
	code, ec = alexKey.call("POST", "/v1/agents/"+deployed.Id.String()+"/threads/s1/messages", map[string]any{"text": "hi"}, nil)
	mustCode(t, "test with an API key", code, ec, 202, "")
	casey := keyClient{t, app.URL, ""}
	code, ec = e.outsider.call("POST", "/v1/me/api-keys", map[string]any{"name": "other"}, &keyOut, nil)
	mustCode(t, "outsider API key", code, ec, 201, "")
	casey.key = keyOut.Secret
	code, ec = casey.call("GET", "/v1/agents/"+deployed.Id.String(), nil, nil)
	mustCode(t, "another team's person", code, ec, 404, "agent_not_found")

	// Bad keys are refused.
	code, ec = keyClient{t, app.URL, "pdp_sa_" + "x"}.call("GET", "/v1/agents", nil, nil)
	mustCode(t, "malformed key", code, ec, 401, "unauthorized")

	// Audited as the service account.
	if kind := e.scalar(`SELECT actor_kind FROM audit_log WHERE action = 'agent.create' AND target_id = $1`, ag.Id.String()); kind != "service_account" {
		t.Errorf("audit actor = %q", kind)
	}
	code, ec = e.owner.call("DELETE", base, nil, nil, nil)
	mustCode(t, "team owner deletes a service-managed agent", code, ec, 200, "")
}

func waitReplyKey(t *testing.T, k keyClient, path string, n int) apitypes.ChatView {
	t.Helper()
	for i := 0; i < 300; i++ {
		var v apitypes.ChatView
		if code, _ := k.call("GET", path, nil, &v); code == 200 && !v.Busy && len(v.Messages) >= n {
			return v
		}
		sleep100()
	}
	t.Fatalf("no reply in %s", path)
	return apitypes.ChatView{}
}
