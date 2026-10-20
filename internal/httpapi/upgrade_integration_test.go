package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/agenthost"
	"github.com/ncecere/pi-durable-platform/internal/agenthost/provision"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// runtimePiDurable is the pi-durable version ../../runtime is pinned to.
func runtimePiDurable(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../runtime/package.json")
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil || pkg.Dependencies["@earendil-works/pi-durable"] == "" {
		t.Fatalf("runtime/package.json: %v", err)
	}
	return pkg.Dependencies["@earendil-works/pi-durable"]
}

// The upgrade check: an agent whose storage a newer pi-durable has written is
// refused on an older runtime before the runtime opens the storage, and the
// agent's page and the API say why; a runtime as new or newer starts and
// moves the record up. Each change is audited once.
func TestUpgradeCheck(t *testing.T) {
	app := newTestAppWith(t, nil, testutil.Logger(), nil, false)
	e := newTeamEnvOn(t, app)
	ctx := context.Background()
	runtime := runtimePiDurable(t)
	var ag apitypes.Agent
	code, ec := e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Upgraded"}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	base := "/v1/agents/" + ag.Id.String()
	if ag.StorageVersion != nil || ag.RuntimeRefused != nil {
		t.Fatalf("a new agent's record %+v %+v", ag.StorageVersion, ag.RuntimeRefused)
	}

	// A newer pi-durable wrote the agent's data.
	if _, err := app.Pool.Exec(ctx, `INSERT INTO agent_storage_versions (agent_id, pi_durable_version) VALUES ($1, '99.0.0')`, ag.Id); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		code, ec = e.member.call("POST", base+"/threads/u1/messages", map[string]any{"text": "hello"}, nil, nil)
		mustCode(t, "a message to an agent on an older runtime", code, ec, 409, "runtime_too_old")
	}
	_, body := e.member.raw("GET", base+"/threads/u1", nil, nil)
	if !strings.Contains(string(body), "pi-durable 99.0.0") || !strings.Contains(string(body), "pi-durable "+runtime) {
		t.Fatalf("the refusal doesn't say why: %s", body)
	}
	if app.Agents.Host().Running(ag.Id.String()) {
		t.Fatal("a refused runtime is still running")
	}
	// The runtime asked before it opened the agent's storage: nothing was written there.
	schema, _ := provision.NameFor(ag.Id.String())
	conn, err := pgx.Connect(ctx, app.cfg.Agents.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var tables int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = $1`, schema).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("the refused runtime wrote the agent's storage: %d tables (%v)", tables, err)
	}
	var got apitypes.Agent
	if code := e.member.get(base, &got); code != 200 || got.StorageVersion == nil || *got.StorageVersion != "99.0.0" || got.RuntimeRefused == nil ||
		got.RuntimeRefused.RuntimeVersion != runtime || got.RuntimeRefused.StorageVersion != "99.0.0" ||
		!strings.Contains(got.RuntimeRefused.Message, "rolling a runtime back") {
		t.Fatalf("the agent's record %d %+v", code, got.RuntimeRefused)
	}
	var log apitypes.AuditPage
	if code := e.owner.get(toolsTeam+"/audit?action=agent.runtime_refused", &log); code != 200 || len(log.Items) != 1 ||
		log.Items[0].Metadata["runtimeVersion"] != runtime || log.Items[0].Metadata["storageVersion"] != "99.0.0" || log.Items[0].ActorKind != "system" {
		t.Fatalf("refusals audited: %d %+v", code, log)
	}

	// The runtime is as new or newer than what wrote the data: it starts, and the record moves up.
	if _, err := app.Pool.Exec(ctx, `UPDATE agent_storage_versions SET pi_durable_version = '1.0.0' WHERE agent_id = $1`, ag.Id); err != nil {
		t.Fatal(err)
	}
	code, ec = e.member.call("POST", base+"/threads/u1/messages", map[string]any{"text": "hello again"}, nil, nil)
	mustCode(t, "a message once the runtime is new enough", code, ec, 202, "")
	waitReply(t, e.member, base+"/threads/u1", 2)
	got = apitypes.Agent{}
	if code := e.member.get(base, &got); code != 200 || got.StorageVersion == nil || *got.StorageVersion != runtime || got.RuntimeRefused != nil {
		t.Fatalf("after the start %d %v %+v", code, got.StorageVersion, got.RuntimeRefused)
	}
	// The same version again (after hibernation) records nothing new.
	code, ec = e.member.call("POST", base+"/hibernate", nil, nil, nil)
	mustCode(t, "hibernate", code, ec, 200, "")
	code, ec = e.member.call("POST", base+"/threads/u1/messages", map[string]any{"text": "and again"}, nil, nil)
	mustCode(t, "a message after hibernation", code, ec, 202, "")
	waitReply(t, e.member, base+"/threads/u1", 4)
	log = apitypes.AuditPage{}
	if code := e.owner.get(toolsTeam+"/audit?action=agent.runtime_recorded", &log); code != 200 || len(log.Items) != 1 ||
		log.Items[0].Metadata["from"] != "1.0.0" || log.Items[0].Metadata["to"] != runtime || log.Items[0].Metadata["refusalEnded"] != true {
		t.Fatalf("recorded versions audited: %d %+v", code, log)
	}

	// A runtime that reports no version (or a malformed one) may be older: refused once a version is recorded.
	for _, v := range []string{"", "not-a-version", "1.3"} {
		var refused *agenthost.RefusedError
		if err := app.Agents.AdmitRuntime(ctx, ag.Id, v); !errors.As(err, &refused) || !strings.Contains(refused.Message, "doesn't report its version") {
			t.Fatalf("a runtime reporting %q: %v", v, err)
		}
	}
	// A newer one moves the record up (and ends the refusal); an older one after it is refused.
	if err := app.Agents.AdmitRuntime(ctx, ag.Id, "100.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := app.Agents.AdmitRuntime(ctx, ag.Id, "100.0.0-rc.1"); err == nil {
		t.Fatal("a pre-release of the recorded version is older")
	}
	got = apitypes.Agent{}
	if code := e.member.get(base, &got); code != 200 || *got.StorageVersion != "100.0.0" || got.RuntimeRefused == nil || got.RuntimeRefused.RuntimeVersion != "100.0.0-rc.1" {
		t.Fatalf("after a newer and an older runtime %+v %+v", got.StorageVersion, got.RuntimeRefused)
	}
}
