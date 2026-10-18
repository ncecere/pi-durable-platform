package httpapi_test

import (
	"fmt"
	"io"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// Personal workspaces: created at sign-in, owner-only, catalog templates
// only, limited by the platform's defaults or the workspace's overrides,
// models published to personal workspaces only, and visible to platform
// staff for audit (never their conversations).
func TestPersonalWorkspaces(t *testing.T) {
	app := newTestAppWith(t, nil, testutil.Logger(), nil, true)
	admin, auditor, user := app.signIn("admin"), app.signIn("auditor"), app.signIn("user")

	// Signing in made the workspace.
	var mine []apitypes.MyTeam
	if code := user.get("/v1/teams", &mine); code != 200 || len(mine) != 1 || mine[0].Kind == nil || *mine[0].Kind != "personal" ||
		mine[0].Role != "owner" {
		t.Fatalf("teams %d %+v", code, mine)
	}
	ws := "/v1/teams/" + mine[0].Slug

	// Owner only, catalog templates only.
	code, ec := user.call("POST", ws+"/members", map[string]any{"email": "alex@localhost", "role": "member"}, nil, nil)
	mustCode(t, "add a member", code, ec, 409, "personal_workspace")
	code, ec = user.call("POST", ws+"/service-accounts", map[string]any{"name": "bot", "role": "editor"}, nil, nil)
	mustCode(t, "a service account", code, ec, 409, "personal_workspace")
	code, ec = user.call("POST", ws+"/agent-templates", map[string]any{"slug": "mine", "name": "Mine", "instructions": "Hi."}, nil, nil)
	mustCode(t, "an own template", code, ec, 409, "personal_workspace")
	code, ec = admin.call("POST", "/v1/admin/teams/"+mine[0].Slug+"/owners", map[string]any{"email": "alex@localhost"}, nil, nil)
	mustCode(t, "an admin adds an owner", code, ec, 409, "personal_workspace")

	// Limits: the platform's defaults, set by platform admins.
	var settings apitypes.PersonalWorkspaceSettings
	if code := auditor.get("/v1/admin/settings/personal-workspaces", &settings); code != 200 || !settings.Enabled || settings.MaxAgents != 5 {
		t.Fatalf("settings %d %+v", code, settings)
	}
	code, ec = auditor.call("PUT", "/v1/admin/settings/personal-workspaces", map[string]any{"enabled": true, "maxAgents": 1, "maxRunning": 1},
		nil, ifMatch(settings.Revision))
	mustCode(t, "an auditor changes settings", code, ec, 403, "")
	code, ec = admin.call("PUT", "/v1/admin/settings/personal-workspaces", map[string]any{"enabled": true, "maxAgents": 2, "maxRunning": 1,
		"monthlyBudgetUsd": 12.5}, &settings, ifMatch(settings.Revision))
	mustCode(t, "an admin changes settings", code, ec, 200, "")
	if settings.MonthlyBudgetUsd == nil || *settings.MonthlyBudgetUsd != 12.5 {
		t.Fatalf("settings %+v", settings)
	}

	var a1, a2 apitypes.Agent
	code, ec = user.call("POST", ws+"/agents", map[string]any{"name": "One"}, &a1, nil)
	mustCode(t, "first agent", code, ec, 201, "")
	code, ec = user.call("POST", ws+"/agents", map[string]any{"name": "Two"}, &a2, nil)
	mustCode(t, "second agent", code, ec, 201, "")
	code, ec = user.call("POST", ws+"/agents", map[string]any{"name": "Three"}, nil, nil)
	mustCode(t, "past max agents", code, ec, 409, "limit_reached")

	// One running at a time.
	code, ec = user.call("POST", "/v1/agents/"+a1.Id.String()+"/threads/t1/messages", map[string]any{"text": "hi"}, nil, nil)
	mustCode(t, "wake the first", code, ec, 202, "")
	waitReply(t, user, "/v1/agents/"+a1.Id.String()+"/threads/t1", 2)
	code, ec = user.call("POST", "/v1/agents/"+a2.Id.String()+"/threads/t1/messages", map[string]any{"text": "hi"}, nil, nil)
	mustCode(t, "wake a second", code, ec, 409, "too_many_running")
	user.call("POST", "/v1/agents/"+a1.Id.String()+"/hibernate", nil, nil, nil)
	code, ec = user.call("POST", "/v1/agents/"+a2.Id.String()+"/threads/t1/messages", map[string]any{"text": "hi"}, nil, nil)
	mustCode(t, "wake the second after hibernating the first", code, ec, 202, "")

	var limits apitypes.WorkspaceLimits
	if code := user.get(ws+"/limits", &limits); code != 200 || limits.Kind != "personal" || *limits.MaxAgents != 2 || limits.AgentCount != 2 ||
		limits.RunningCount != 1 || len(limits.Overridden) != 0 {
		t.Fatalf("limits %d %+v", code, limits)
	}

	// One workspace's override.
	var team apitypes.TeamSummary
	admin.get("/v1/admin/teams/"+mine[0].Slug, &team)
	code, ec = admin.call("PUT", "/v1/admin/teams/"+mine[0].Slug+"/limits", map[string]any{"maxAgents": 3}, &limits, ifMatch(int64(team.Team.Revision)))
	mustCode(t, "override", code, ec, 200, "")
	if *limits.MaxAgents != 3 || len(limits.Overridden) != 1 || limits.Overridden[0] != "maxAgents" {
		t.Fatalf("overridden %+v", limits)
	}
	code, ec = user.call("POST", ws+"/agents", map[string]any{"name": "Three"}, nil, nil)
	mustCode(t, "a third agent with the override", code, ec, 201, "")
	createTeam(t, admin, "shared", "alex@localhost")
	admin.get("/v1/admin/teams/shared", &team)
	code, ec = admin.call("PUT", "/v1/admin/teams/shared/limits", map[string]any{"maxAgents": 3}, nil, ifMatch(int64(team.Team.Revision)))
	mustCode(t, "limits on a shared team", code, ec, 409, "not_personal")

	// Models: available in personal workspaces only when published there.
	var prov apitypes.ModelProvider
	admin.call("POST", "/v1/admin/model-providers", map[string]any{"name": "P", "kind": "openai-compatible", "baseUrl": "http://127.0.0.1:1/v1",
		"apiKey": "k"}, &prov, nil)
	var teamsOnly, everywhere apitypes.Model
	admin.call("POST", "/v1/admin/model-providers/"+prov.Id.String()+"/models", map[string]any{"upstreamId": "m-all", "personal": false}, &teamsOnly, nil)
	admin.call("POST", "/v1/admin/model-providers/"+prov.Id.String()+"/models", map[string]any{"upstreamId": "m-personal"}, &everywhere, nil)
	if teamsOnly.Personal || !everywhere.Personal {
		t.Fatalf("personal flags %v %v", teamsOnly.Personal, everywhere.Personal)
	}
	var models []apitypes.Model
	if code := user.get(ws+"/models", &models); code != 200 || len(models) != 1 || models[0].Id != everywhere.Id {
		t.Fatalf("personal models %d %+v", code, models)
	}

	// Platform staff audit it: the workspace, its agents, usage and audit log; never its conversations.
	var page apitypes.TeamPage
	found := false
	if code := auditor.get("/v1/admin/teams?kind=personal", &page); code == 200 {
		for _, it := range page.Items { // everyone who signed in has one: admin, auditor, user
			if it.Team.Slug == mine[0].Slug {
				found = it.PersonalOwner != nil && it.PersonalOwner.Email == "user@localhost"
			}
			if it.Team.Kind == nil || *it.Team.Kind != "personal" {
				t.Fatalf("kind=personal listed %+v", it.Team)
			}
		}
	}
	if !found || len(page.Items) != 3 {
		t.Fatalf("admin personal teams %+v", page)
	}
	var agentsSeen []apitypes.Agent
	if code := auditor.get(ws+"/agents", &agentsSeen); code != 200 || len(agentsSeen) != 3 {
		t.Fatalf("auditor sees agents %d %d", code, len(agentsSeen))
	}
	if code := auditor.get("/v1/agents/"+a1.Id.String(), nil); code != 200 {
		t.Fatalf("auditor reads an agent: %d", code)
	}
	for _, path := range []string{"/threads", "/threads/t1"} {
		code, ec := auditor.call("GET", "/v1/agents/"+a1.Id.String()+path, nil, nil, nil)
		mustCode(t, fmt.Sprintf("auditor reads %s", path), code, ec, 404, "agent_not_found")
	}
	var audit struct {
		Items []apitypes.AuditEntry `json:"items"`
	}
	if code := auditor.get(ws+"/audit", &audit); code != 200 || len(audit.Items) == 0 {
		t.Fatalf("auditor reads the audit log: %d", code)
	}
	if code := auditor.get(ws+"/usage", nil); code != 200 {
		t.Fatalf("auditor reads usage: %d", code)
	}

	// Turned off: new people get none; existing workspaces stay.
	admin.get("/v1/admin/settings/personal-workspaces", &settings)
	code, ec = admin.call("PUT", "/v1/admin/settings/personal-workspaces", map[string]any{"enabled": false, "maxAgents": 2, "maxRunning": 1},
		&settings, ifMatch(settings.Revision))
	mustCode(t, "turn off", code, ec, 200, "")
	casey := app.signIn("casey")
	if code := casey.get("/v1/teams", &mine); code != 200 || len(mine) != 0 {
		t.Fatalf("casey's teams with personal workspaces off: %+v", mine)
	}
	if code := user.get("/v1/teams", &mine); len(mine) != 1 {
		t.Fatalf("existing workspace gone: %d %+v", code, mine)
	}
}

// Only people in a required group may sign in; the bootstrap admin always may.
func TestRequiredGroups(t *testing.T) {
	cases := []struct {
		name   string
		claims map[string]any
		want   int
	}{
		{"in a required group", map[string]any{"sub": "a", "email": "a@example.com", "email_verified": true, "groups": []any{"x", "pdp-users"}}, 200},
		{"in no required group", map[string]any{"sub": "b", "email": "b@example.com", "email_verified": true, "groups": []any{"x"}}, 403},
		{"no groups claim", map[string]any{"sub": "c", "email": "c@example.com", "email_verified": true}, 403},
		{"the bootstrap admin", map[string]any{"sub": "boss", "email": "boss@example.com", "email_verified": true}, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, p := oidcApp(t, func(c *config.Config) {
				c.OIDC.AllowedGroups = []string{"pdp-users", "pdp-admins"}
				c.OIDC.BootstrapAdminSubject = "boss"
			})
			p.SetClaims(tc.claims)
			res, err := app.Client.Get(app.URL + "/auth/login")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if tc.want == 403 && (res.StatusCode != 403 || errorCode(body) != "group_denied") {
				t.Fatalf("got %d %s, want 403 group_denied", res.StatusCode, body)
			}
			code, _ := app.me()
			if (tc.want == 200) != (code == 200) {
				t.Fatalf("signed in: %d, want %v", code, tc.want == 200)
			}
		})
	}
}
