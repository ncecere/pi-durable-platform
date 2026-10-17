package httpapi_test

import (
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
)

// A team template: private, versioned, deployed and upgraded by its team,
// published to the catalog after a platform admin's review, then deployed by
// another team, and archived.
func TestTemplateLifecycle(t *testing.T) {
	e := newTeamEnv(t)
	const otherTeam = "/v1/teams/other"

	var tpl apitypes.AgentTemplate
	code, ec := e.editor.call("POST", toolsTeam+"/agent-templates", map[string]any{
		"slug": "triage", "name": "Triage", "description": "Sorts tickets.", "instructions": "Sort the ticket."}, &tpl, nil)
	mustCode(t, "create a team template", code, ec, 201, "")
	if tpl.Status != "private" || tpl.CurrentVersion != 1 || tpl.TeamSlug == nil || *tpl.TeamSlug != "tools" {
		t.Fatalf("created %+v", tpl)
	}
	base := "/v1/agent-templates/" + tpl.Id.String()

	// Deployed by its team at version 1.
	var ag apitypes.Agent
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Triage bot", "templateId": tpl.Id}, &ag, nil)
	mustCode(t, "deploy the team template", code, ec, 201, "")
	if ag.TemplateId != tpl.Id || ag.TemplateVersion != 1 || ag.Description != "Sorts tickets." {
		t.Fatalf("deployed %+v", ag)
	}

	// Private: another team neither sees nor deploys it.
	code, ec = e.outsider.call("GET", base, nil, nil, nil)
	mustCode(t, "another team reads a private template", code, ec, 404, "template_not_found")
	code, ec = e.outsider.call("POST", otherTeam+"/agents", map[string]any{"name": "Copy", "templateId": tpl.Id}, nil, nil)
	mustCode(t, "another team deploys a private template", code, ec, 404, "template_not_found")

	// A new version: new agents get it; the deployed one keeps v1 until upgraded.
	code, ec = e.editor.call("POST", base+"/versions", map[string]any{"instructions": "Sort and label the ticket.", "notes": "Labels"}, &tpl, nil)
	mustCode(t, "add a version", code, ec, 201, "")
	var got apitypes.Agent
	if e.member.get("/v1/agents/"+ag.Id.String(), &got); got.TemplateVersion != 1 || got.TemplateCurrentVersion != 2 {
		t.Fatalf("after a new version: %+v", got)
	}
	code, ec = e.editor.call("PATCH", "/v1/agents/"+ag.Id.String(), map[string]any{"templateVersion": 2}, &got, ifMatch(got.Revision))
	mustCode(t, "upgrade", code, ec, 200, "")
	if got.TemplateVersion != 2 {
		t.Fatalf("upgraded %+v", got)
	}
	code, ec = e.editor.call("PATCH", "/v1/agents/"+ag.Id.String(), map[string]any{"templateVersion": 9}, nil, ifMatch(got.Revision))
	mustCode(t, "a version that doesn't exist", code, ec, 404, "version_not_found")
	var versions []apitypes.AgentTemplateVersion
	if code := e.member.get(base+"/versions", &versions); code != 200 || len(versions) != 2 || versions[0].Version != 2 || versions[0].Notes != "Labels" {
		t.Fatalf("versions = %d %+v", code, versions)
	}

	// Publishing: the team's owners and admins ask; a platform admin reviews.
	code, ec = e.editor.call("POST", base+"/publish", nil, nil, nil)
	mustCode(t, "an editor asks to publish", code, ec, 403, "forbidden")
	code, ec = e.owner.call("POST", base+"/publish", nil, &tpl, nil)
	mustCode(t, "the owner asks to publish", code, ec, 200, "")
	var queue []apitypes.AgentTemplate
	if code := e.admin.get("/v1/admin/agent-templates?status=pending", &queue); code != 200 || len(queue) != 1 || queue[0].Id != tpl.Id {
		t.Fatalf("review queue = %d %+v", code, queue)
	}
	code, ec = e.admin.call("POST", base+"/review", map[string]any{"decision": "reject", "note": "Add a description of the labels."}, &tpl, nil)
	mustCode(t, "reject", code, ec, 200, "")
	if tpl.Status != "private" || tpl.ReviewNote == nil || *tpl.ReviewNote != "Add a description of the labels." {
		t.Fatalf("rejected %+v", tpl)
	}
	code, ec = e.admin.call("POST", base+"/review", map[string]any{"decision": "approve"}, nil, nil)
	mustCode(t, "review without a request", code, ec, 409, "not_pending")
	e.owner.call("POST", base+"/publish", nil, nil, nil)
	var approved apitypes.AgentTemplate
	code, ec = e.admin.call("POST", base+"/review", map[string]any{"decision": "approve"}, &approved, nil)
	mustCode(t, "approve", code, ec, 200, "")
	if approved.Status != "published" || approved.ReviewNote != nil {
		t.Fatalf("approved %+v", approved)
	}

	// In the catalog: another team sees and deploys it.
	var catalog []apitypes.AgentTemplate
	if code := e.outsider.get("/v1/agent-templates", &catalog); code != 200 || !hasTemplate(catalog, tpl.Id.String()) {
		t.Fatalf("catalog = %d %+v", code, catalog)
	}
	code, ec = e.outsider.call("POST", otherTeam+"/agents", map[string]any{"name": "Their triage", "templateId": tpl.Id}, nil, nil)
	mustCode(t, "another team deploys a published template", code, ec, 201, "")

	// Archived: no new deployments; deployed agents keep working.
	var cur apitypes.AgentTemplate
	e.editor.get(base, &cur)
	code, ec = e.editor.call("PATCH", base, map[string]any{"archived": true}, nil, ifMatch(cur.Revision))
	mustCode(t, "archive", code, ec, 200, "")
	code, ec = e.outsider.call("POST", otherTeam+"/agents", map[string]any{"name": "Another", "templateId": tpl.Id}, nil, nil)
	mustCode(t, "deploy an archived template", code, ec, 409, "template_archived")
	code, ec = e.member.call("POST", "/v1/agents/"+ag.Id.String()+"/threads/x/messages", map[string]any{"text": "still there?"}, nil, nil)
	mustCode(t, "an agent of an archived template", code, ec, 202, "")

	// Platform templates are platform admins'.
	code, ec = e.owner.call("POST", "/v1/agent-templates", map[string]any{"slug": "x", "name": "X", "instructions": "Hi."}, nil, nil)
	mustCode(t, "a team owner creates a platform template", code, ec, 403, "forbidden")
	code, ec = e.admin.call("POST", "/v1/agent-templates", map[string]any{"slug": "assistant", "name": "Dup", "instructions": "Hi."}, nil, nil)
	mustCode(t, "a taken platform slug", code, ec, 409, "slug_taken")
}

func hasTemplate(list []apitypes.AgentTemplate, id string) bool {
	for _, t := range list {
		if t.Id.String() == id {
			return true
		}
	}
	return false
}
