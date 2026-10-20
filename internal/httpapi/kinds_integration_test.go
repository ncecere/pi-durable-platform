package httpapi_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
)

// Agent kinds (docs/first-party-agents.md): a template version's kind and its settings, checked, kept across
// versions, and passed to the runtime, which runs that composition.

func versionsOf(t *testing.T, s *session, templateID string) []apitypes.AgentTemplateVersion {
	t.Helper()
	var versions []apitypes.AgentTemplateVersion
	if code := s.get("/v1/agent-templates/"+templateID+"/versions", &versions); code != 200 {
		t.Fatalf("versions: %d", code)
	}
	return versions
}

func settingsJSON(t *testing.T, s *apitypes.AgentKindSettings) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTemplateVersionsCarryTheirKind(t *testing.T) {
	e := newTeamEnv(t)
	create := func(slug string, extra map[string]any) (apitypes.AgentTemplate, int, string) {
		body := map[string]any{"slug": slug, "name": slug, "instructions": "Hi."}
		for k, v := range extra {
			body[k] = v
		}
		var tpl apitypes.AgentTemplate
		code, ec := e.editor.call("POST", toolsTeam+"/agent-templates", body, &tpl, nil)
		return tpl, code, ec
	}

	// No kind: the assistant, without settings.
	plain, code, ec := create("plain", nil)
	mustCode(t, "an assistant template", code, ec, 201, "")
	if v := versionsOf(t, e.editor, plain.Id.String())[0]; v.Kind == nil || *v.Kind != apitypes.AgentKindAssistant || settingsJSON(t, v.KindSettings) != "{}" {
		t.Fatalf("default kind = %v %s", v.Kind, settingsJSON(t, v.KindSettings))
	}

	// What is refused: an unknown kind, settings of another kind, bad values, an unknown field.
	for name, bad := range map[string]struct {
		body map[string]any
		code int
		err  string
	}{
		"an unknown kind":              {map[string]any{"kind": "poet"}, 400, "invalid_kind"},
		"settings for the assistant":   {map[string]any{"kindSettings": map[string]any{"listTools": []string{"x"}}}, 400, "invalid_kind_settings"},
		"a researcher field on triage": {map[string]any{"kind": "triage", "kindSettings": map[string]any{"maxPlanSteps": 3}}, 400, "invalid_kind_settings"},
		"a bad tool name":              {map[string]any{"kind": "triage", "kindSettings": map[string]any{"writeTools": []string{"rm -rf"}}}, 400, "invalid_kind_settings"},
		"too many plan steps":          {map[string]any{"kind": "researcher", "kindSettings": map[string]any{"maxPlanSteps": 21}}, 400, "invalid_kind_settings"},
		"an unknown field":             {map[string]any{"kind": "triage", "kindSettings": map[string]any{"deleteTools": []string{"x"}}}, 400, "invalid_json"},
	} {
		_, code, ec := create("bad", bad.body)
		mustCode(t, name, code, ec, bad.code, bad.err)
	}

	// Triage: its settings with their defaults filled in.
	tpl, code, ec := create("triage", map[string]any{"kind": "triage", "kindSettings": map[string]any{
		"listTools": []string{"mcp__gh__list_issues"}, "writeTools": []string{"mcp__gh__add_comment", " mcp__gh__add_comment"}}})
	mustCode(t, "a triage template", code, ec, 201, "")
	want := `{"approveWrites":true,"listTools":["mcp__gh__list_issues"],"readTools":[],"writeTools":["mcp__gh__add_comment"]}`
	if v := versionsOf(t, e.editor, tpl.Id.String())[0]; *v.Kind != apitypes.AgentKindTriage || settingsJSON(t, v.KindSettings) != want {
		t.Fatalf("version 1 = %v %s", *v.Kind, settingsJSON(t, v.KindSettings))
	}
	path := "/v1/agent-templates/" + tpl.Id.String() + "/versions"
	// Omitted: the kind and its settings stay.
	code, ec = e.editor.call("POST", path, map[string]any{"instructions": "Again."}, nil, nil)
	mustCode(t, "version 2", code, ec, 201, "")
	// Settings alone replace the kind's settings as a whole.
	code, ec = e.editor.call("POST", path, map[string]any{"instructions": "Again.", "kindSettings": map[string]any{"approveWrites": false}}, nil, nil)
	mustCode(t, "version 3", code, ec, 201, "")
	// Another kind without settings: its defaults.
	code, ec = e.editor.call("POST", path, map[string]any{"instructions": "Research.", "kind": "researcher"}, nil, nil)
	mustCode(t, "version 4", code, ec, 201, "")
	code, ec = e.editor.call("POST", path, map[string]any{"instructions": "x", "kind": "researcher", "kindSettings": map[string]any{"writeTools": []string{"a"}}}, nil, nil)
	mustCode(t, "a triage field on the researcher", code, ec, 400, "invalid_kind_settings")
	versions := versionsOf(t, e.editor, tpl.Id.String())
	if len(versions) != 4 {
		t.Fatalf("%d versions", len(versions))
	}
	if v := versions[0]; *v.Kind != apitypes.AgentKindResearcher || settingsJSON(t, v.KindSettings) != `{"backgroundSubagents":true,"maxPlanSteps":8}` {
		t.Fatalf("version 4 = %v %s", *v.Kind, settingsJSON(t, v.KindSettings))
	}
	if v := versions[1]; *v.Kind != apitypes.AgentKindTriage || settingsJSON(t, v.KindSettings) != `{"approveWrites":false,"listTools":[],"readTools":[],"writeTools":[]}` {
		t.Fatalf("version 3 = %v %s", *v.Kind, settingsJSON(t, v.KindSettings))
	}
	for _, v := range versions[2:] {
		if *v.Kind != apitypes.AgentKindTriage || settingsJSON(t, v.KindSettings) != want {
			t.Fatalf("version %d = %v %s", v.Version, *v.Kind, settingsJSON(t, v.KindSettings))
		}
	}
}

func TestAKindReachesTheRuntime(t *testing.T) {
	e := newTeamEnv(t)
	deploy := func(slug string, extra map[string]any) (string, map[string]string) {
		body := map[string]any{"slug": slug, "name": slug, "instructions": "Hi."}
		for k, v := range extra {
			body[k] = v
		}
		var tpl apitypes.AgentTemplate
		code, ec := e.editor.call("POST", toolsTeam+"/agent-templates", body, &tpl, nil)
		mustCode(t, "template "+slug, code, ec, 201, "")
		var ag apitypes.Agent
		code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": slug + " desk", "templateId": tpl.Id}, &ag, nil)
		mustCode(t, "deploy "+slug, code, ec, 201, "")
		return ag.Id.String(), e.spec(ag.Id).Env
	}

	// The assistant's variables are as before.
	_, env := deploy("plain", nil)
	if _, ok := env["AGENT_KIND"]; ok {
		t.Fatalf("an assistant got AGENT_KIND %q", env["AGENT_KIND"])
	}
	if _, ok := env["AGENT_KIND_SETTINGS"]; ok {
		t.Fatal("an assistant got AGENT_KIND_SETTINGS")
	}

	// Triage: its kind and settings, and its write tools ask first, after the version's own list. The demo model
	// calls get_time when asked the time: as a write tool, it waits for a person through the platform.
	agentID, env := deploy("triage", map[string]any{"kind": "triage", "approvals": map[string]any{"tools": []string{"mcp__crm__*"}},
		"kindSettings": map[string]any{"listTools": []string{"mcp__gh__list"}, "writeTools": []string{"get_time"}}})
	if env["AGENT_KIND"] != "triage" || !strings.Contains(env["AGENT_KIND_SETTINGS"], `"writeTools":["get_time"]`) {
		t.Fatalf("triage variables: %q %q", env["AGENT_KIND"], env["AGENT_KIND_SETTINGS"])
	}
	var approvals struct{ Tools []string }
	if err := json.Unmarshal([]byte(env["TOOL_APPROVALS"]), &approvals); err != nil || strings.Join(approvals.Tools, ",") != "mcp__crm__*,get_time" {
		t.Fatalf("TOOL_APPROVALS = %s", env["TOOL_APPROVALS"])
	}
	base := "/v1/agents/" + agentID
	thread := base + "/threads/hook"
	code, ec := e.member.call("POST", thread+"/messages", map[string]any{"text": "What time is it?"}, nil, nil)
	mustCode(t, "ask the time", code, ec, 202, "")
	ap := (&approvalEnv{teamEnv: e, agent: agentID, base: base}).pending(e.member, "hook")
	if ap.Tool != "get_time" {
		t.Fatalf("pending approval = %+v", ap)
	}
	// The queue is exposed: the view lists it, and apps read it with the thread's access.
	var view apitypes.ChatView
	if code := e.member.get(thread, &view); code != 200 || view.Documents == nil || (*view.Documents)["triage.queue"].Revision == "" {
		t.Fatalf("view documents = %d %+v", code, view.Documents)
	}
	var queue apitypes.ThreadDocument
	if code := e.member.get(thread+"/documents/triage.queue", &queue); code != 200 || queue.Kind != "triage.queue" {
		t.Fatalf("queue = %d %+v", code, queue)
	}
	if code := e.outsider.get(thread+"/documents/triage.queue", nil); code != 404 {
		t.Fatalf("another team reads the queue: %d", code)
	}
	code, ec = e.member.call("POST", base+"/approvals/"+ap.Id, map[string]any{"decision": "approve"}, nil, nil)
	mustCode(t, "approve", code, ec, 200, "")
	if r := toolResult(t, waitReply(t, e.member, thread, 4)); !strings.Contains(r, "(UTC)") {
		t.Fatalf("the approved call's result = %q", r)
	}

	// With approveWrites off, nothing is added.
	_, env = deploy("triage-free", map[string]any{"kind": "triage", "kindSettings": map[string]any{"writeTools": []string{"get_time"}, "approveWrites": false}})
	if v, ok := env["TOOL_APPROVALS"]; ok {
		t.Fatalf("TOOL_APPROVALS with approveWrites off = %s", v)
	}

	// The researcher: its plan and sources are exposed in every thread.
	agentID, env = deploy("research", map[string]any{"kind": "researcher", "kindSettings": map[string]any{"maxPlanSteps": 3}})
	var rs struct {
		BackgroundSubagents bool
		MaxPlanSteps        int
	}
	if env["AGENT_KIND"] != "researcher" || json.Unmarshal([]byte(env["AGENT_KIND_SETTINGS"]), &rs) != nil || !rs.BackgroundSubagents || rs.MaxPlanSteps != 3 {
		t.Fatalf("researcher variables: %q %q", env["AGENT_KIND"], env["AGENT_KIND_SETTINGS"])
	}
	thread = "/v1/agents/" + agentID + "/threads/r1"
	code, ec = e.member.call("POST", thread+"/messages", map[string]any{"text": "Hello"}, nil, nil)
	mustCode(t, "say hello", code, ec, 202, "")
	view = waitReply(t, e.member, thread, 2)
	if view.Documents == nil || (*view.Documents)["research.plan"].Revision == "" || (*view.Documents)["research.sources"].Revision == "" {
		t.Fatalf("researcher documents = %+v", view.Documents)
	}
	var plan apitypes.ThreadDocument
	if code := e.member.get(thread+"/documents/research.plan", &plan); code != 200 {
		t.Fatalf("plan = %d", code)
	}
	if b, _ := json.Marshal(plan.Value); string(b) != `{"question":"","steps":[]}` {
		t.Fatalf("plan value = %s", b)
	}
}
