package httpapi_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// During a rolling upgrade a v0.2 pod still serves PUT …/agents/by-key/{key}
// after migration 00018 added agents.service_managed (default false), with
// v0.2's InsertAgent, which doesn't name the column. The agent it creates
// must still be service-managed: the team's people never read an app's
// users' conversations. v0.3's own inserts are unchanged: a keyed agent of
// a personal workspace is the workspace's, one of a shared team the app's.
func TestKeyedAgentOfAnOlderReplicaIsServiceManaged(t *testing.T) {
	e := newMoveEnv(t)
	ctx := context.Background()
	managed := func(id uuid.UUID) (m bool) {
		t.Helper()
		if err := e.app.Pool.QueryRow(ctx, `SELECT service_managed FROM agents WHERE id = $1`, id).Scan(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	// v0.2's InsertAgent, column for column (git show v0.2.0:internal/store/queries/agents.sql).
	id := uuid.New()
	if _, err := e.app.Pool.Exec(ctx, `
		INSERT INTO agents (id, team_id, template_id, template_version, name, description, db_password_enc, runtime_token_enc, created_by, external_key, model_id)
		SELECT $1, t.id, tpl.id, 1, 'Assistant for user 9', '', '\x00', '\x00', NULL, 'user-9', NULL
		FROM teams t, agent_templates tpl WHERE t.slug = 'tools' AND tpl.slug = 'assistant'`, id); err != nil {
		t.Fatal(err)
	}
	if !managed(id) {
		t.Fatal("a keyed agent a v0.2 replica created in a shared team is not service-managed")
	}
	code, ec := e.blair.call("GET", "/v1/agents/"+id.String(), nil, nil, nil)
	mustCode(t, "a member of the team gets the app's agent", code, ec, 404, "agent_not_found")
	code, ec = e.blair.call("GET", "/v1/agents/"+id.String()+"/threads", nil, nil, nil)
	mustCode(t, "a member of the team lists the app's agent's threads", code, ec, 404, "agent_not_found")

	// Nothing turns it into an ordinary team agent while it keeps its key.
	if _, err := e.app.Pool.Exec(ctx, `UPDATE agents SET service_managed = false WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if !managed(id) {
		t.Fatal("a keyed agent of a shared team was made not service-managed")
	}

	// v0.3's own: in a shared team, managed; in a personal workspace, not.
	shared := send(t, e.app, e.chat, "PUT", toolsTeam+"/agents/by-key/user-10", nil, nil)
	if shared.code != 201 || !shared.agent.ServiceManaged || !managed(shared.agent.Id) {
		t.Fatalf("a service account's keyed agent: %+v", shared)
	}
	mine := send(t, e.app, viaSession(e.user), "PUT", e.userWS+"/agents/by-key/notes", map[string]any{"name": "Notes"}, nil)
	if mine.code != 201 || mine.agent.ServiceManaged || managed(mine.agent.Id) {
		t.Fatalf("a keyed agent of a personal workspace: %+v", mine)
	}
	if code := e.user.get("/v1/agents/"+mine.agent.Id.String()+"/threads", nil); code != 200 {
		t.Fatalf("the workspace's owner lists its threads: %d", code)
	}
	// Moving it out of the workspace drops its key: an ordinary agent of the team.
	code, ec = e.user.call("POST", "/v1/agents/"+mine.agent.Id.String()+"/move", map[string]any{"team": "tools"}, nil, ifMatch(mine.agent.Revision))
	mustCode(t, "move a keyed agent out of the workspace", code, ec, 200, "")
	if managed(mine.agent.Id) {
		t.Fatal("a moved agent (its key dropped) is service-managed")
	}
	if code := e.blair.get("/v1/agents/"+mine.agent.Id.String(), nil); code != 200 {
		t.Fatalf("the team's member gets the moved agent: %d", code)
	}
}
