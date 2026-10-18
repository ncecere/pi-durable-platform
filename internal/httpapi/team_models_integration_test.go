package httpapi_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/llm"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// Team providers are reached through the address guard, which never dials
// loopback (where test servers listen), whatever private_networks says, and
// the test apps' development allowance (DEV_AUTH on a loopback URL) would
// accept every address. So these tests turn the allowance off and give the
// guard a resolver: test host names resolve to the addresses a table says
// (public or private, as a real name would), the guard decides on those, and
// an approved connection is sent to the local https test server, whose
// certificate is checked (as example.com).

// hostTable is what test host names resolve to; it can change mid-test.
type hostTable struct {
	mu sync.Mutex
	m  map[string]string
}

func (h *hostTable) set(host, addr string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.m[host] = addr
}

func (h *hostTable) lookup(_ context.Context, host string) ([]netip.Addr, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	a, ok := h.m[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	return []netip.Addr{netip.MustParseAddr(a)}, nil
}

// useTeamNetwork sends teams' services' (providers' and MCP servers')
// approved connections to srv (an httptest TLS server) with hosts resolved
// from table, through the real address guard without the development
// allowance.
func useTeamNetwork(t *testing.T, app *testApp, srv *httptest.Server, table map[string]string) *hostTable {
	t.Helper()
	hosts := &hostTable{m: table}
	cfg := llm.TeamNetwork{
		Lookup: hosts.lookup,
		Connect: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
		TLS: &tls.Config{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs, ServerName: "example.com"},
	}
	app.Models.SetTeamNetwork(cfg)
	app.MCP.SetTeamNetwork(cfg)
	return hosts
}

// proxyCall calls the model proxy as an agent and returns the status and
// error code.
func proxyCall(t *testing.T, gatewayURL, providerID, key, model string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", gatewayURL+"/model-proxy/"+providerID+"/chat/completions",
		bytes.NewReader([]byte(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	return res.StatusCode, errorCode(buf.Bytes())
}

func (e *teamEnv) userID(email string) uuid.UUID {
	return uuid.MustParse(e.scalar(`SELECT id::text FROM users WHERE email = $1`, email))
}

func (e *teamEnv) setModelSettings(teamsMay bool, networks ...string) apitypes.ModelSettings {
	e.t.Helper()
	var cur, out apitypes.ModelSettings
	if code := e.admin.get("/v1/admin/settings/models", &cur); code != 200 {
		e.t.Fatalf("model settings: %d", code)
	}
	if networks == nil {
		networks = []string{}
	}
	code, ec := e.admin.call("PUT", "/v1/admin/settings/models", map[string]any{"teamsMayAddProviders": teamsMay, "privateNetworks": networks},
		&out, ifMatch(cur.Revision))
	mustCode(e.t, "set model settings", code, ec, 200, "")
	return out
}

func (e *teamEnv) canAddProviders(s *session, team string) bool {
	e.t.Helper()
	var l apitypes.WorkspaceLimits
	if code := s.get("/v1/teams/"+team+"/limits", &l); code != 200 || l.CanAddProviders == nil {
		e.t.Fatalf("limits of %s: %d %+v", team, code, l)
	}
	return *l.CanAddProviders
}

// A team's own model providers end to end: the address guard and the
// platform's private networks decide where they may be; the team's owners
// and admins manage them (in a browser session), its members read them;
// their models are the team's only (templates, agents, the model proxy,
// the catalog); platform admins see and govern them; deleting the team
// takes them along.
func TestTeamModelProviders(t *testing.T) {
	e := newTeamEnv(t)
	fake := newFakeTLSProvider(t)
	hosts := useTeamNetwork(t, e.app, fake.srv, map[string]string{
		"models.tools.test": "10.20.30.40",   // a private address
		"public.tools.test": "93.184.216.34", // a public one
	})
	const providers = toolsTeam + "/model-providers"
	body := func(name, base string) map[string]any {
		return map[string]any{"name": name, "kind": "openai-compatible", "baseUrl": base, "apiKey": fakeProviderKey}
	}

	// Members see the provider types; everyone signed in to the team.
	var kinds []apitypes.ProviderType
	if code := e.member.get("/v1/teams/tools/model-provider-kinds", &kinds); code != 200 || len(kinds) != len(llm.Kinds) {
		t.Fatalf("kinds: %d %+v", code, kinds)
	}
	code, ec := e.outsider.call("GET", "/v1/teams/tools/model-provider-kinds", nil, nil, nil)
	mustCode(t, "another team's kinds", code, ec, 404, "team_not_found")

	// The base URL must be https, proxy mode, on an address teams may use.
	code, ec = e.owner.call("POST", providers, body("Private", "https://models.tools.test/v1"), nil, nil)
	mustCode(t, "a private address not allowed yet", code, ec, 422, "address_not_allowed")
	loopback := "https://" + fake.srv.Listener.Addr().String() + "/v1"
	code, ec = e.owner.call("POST", providers, body("Loopback", loopback), nil, nil)
	mustCode(t, "a loopback address", code, ec, 422, "address_not_allowed")
	code, ec = e.owner.call("POST", providers, body("Metadata", "https://169.254.169.254/v1"), nil, nil)
	mustCode(t, "the metadata address", code, ec, 422, "address_not_allowed")
	code, ec = e.owner.call("POST", providers, body("Plain", "http://public.tools.test/v1"), nil, nil)
	mustCode(t, "plain http", code, ec, 400, "invalid_base_url")
	code, ec = e.owner.call("POST", providers, body("Unknown", "https://nowhere.tools.test/v1"), nil, nil)
	mustCode(t, "an unknown host", code, ec, 422, "address_unresolved")
	direct := body("Direct", "https://public.tools.test/v1")
	direct["accessMode"] = "direct"
	code, ec = e.owner.call("POST", providers, direct, nil, nil)
	mustCode(t, "direct mode", code, ec, 400, "invalid_access_mode")

	// Platform admins allow private networks; loopback can't be listed.
	var settings apitypes.ModelSettings
	if code := e.auditor.get("/v1/admin/settings/models", &settings); code != 200 || !settings.TeamsMayAddProviders ||
		len(settings.PrivateNetworks) != 0 {
		t.Fatalf("settings: %d %+v", code, settings)
	}
	code, ec = e.auditor.call("PUT", "/v1/admin/settings/models", map[string]any{"teamsMayAddProviders": true, "privateNetworks": []string{}},
		nil, ifMatch(settings.Revision))
	mustCode(t, "an auditor changes model settings", code, ec, 403, "")
	for _, bad := range []string{"127.0.0.0/8", "169.254.0.0/16", "8.8.8.0/24", "not-a-cidr"} {
		code, ec = e.admin.call("PUT", "/v1/admin/settings/models", map[string]any{"teamsMayAddProviders": true, "privateNetworks": []string{bad}},
			nil, ifMatch(settings.Revision))
		mustCode(t, "private network "+bad, code, ec, 400, "invalid_private_networks")
	}
	if s := e.setModelSettings(true, "10.20.1.2/16"); len(s.PrivateNetworks) != 1 || s.PrivateNetworks[0] != "10.20.0.0/16" {
		t.Fatalf("normalised networks %+v", s.PrivateNetworks)
	}
	code, ec = e.owner.call("POST", providers, body("Loopback", loopback), nil, nil)
	mustCode(t, "loopback stays refused", code, ec, 422, "address_not_allowed")

	// The owner adds one; the key is never returned.
	var prov apitypes.ModelProvider
	code, raw := e.owner.raw("POST", providers, body("Team gateway", "https://models.tools.test/v1/"), nil)
	if code != 201 || strings.Contains(string(raw), fakeProviderKey) {
		t.Fatalf("create team provider: %d %s", code, raw)
	}
	mustDecode(t, raw, &prov)
	if prov.AccessMode != "proxy" || prov.BaseUrl != "https://models.tools.test/v1" || prov.Team == nil || prov.Team.Slug != "tools" ||
		prov.Team.Kind != "team" {
		t.Fatalf("team provider %+v", prov)
	}
	pbase := providers + "/" + prov.Id.String()
	// Names are unique per owner: the platform may have the same name.
	code, ec = e.owner.call("POST", providers, body("team GATEWAY", "https://public.tools.test/v1"), nil, nil)
	mustCode(t, "the same name in the team", code, ec, 409, "provider_name_taken")
	var platformProv apitypes.ModelProvider
	platformFake := newFakeProvider(t)
	code, ec = e.admin.call("POST", "/v1/admin/model-providers", body("Team gateway", platformFake.srv.URL+"/v1"), &platformProv, nil)
	mustCode(t, "a platform provider with the same name", code, ec, 201, "")
	if platformProv.Team != nil {
		t.Fatalf("platform provider has a team: %+v", platformProv.Team)
	}

	// Members read; only owners and admins manage, in a browser session.
	var list []apitypes.ModelProvider
	if code := e.member.get(providers, &list); code != 200 || len(list) != 1 || list[0].Id != prov.Id {
		t.Fatalf("member lists: %d %+v", code, list)
	}
	code, ec = e.member.call("POST", providers, body("Mine", "https://public.tools.test/v1"), nil, nil)
	mustCode(t, "a member adds a provider", code, ec, 403, "forbidden")
	code, ec = e.editor.call("PATCH", pbase, map[string]any{"enabled": false}, nil, ifMatch(prov.Revision))
	mustCode(t, "an editor changes it", code, ec, 403, "forbidden")
	code, ec = e.editor.call("POST", pbase+"/discover", nil, nil, nil)
	mustCode(t, "an editor discovers", code, ec, 403, "forbidden")
	sa := createSA(t, e.owner, map[string]any{"name": "bot", "role": "editor"})
	bot := keyClient{t, e.app.URL, createSAKey(t, e.owner, sa.Id.String(), map[string]any{"name": "k"}).Secret}
	code, ec = bot.call("POST", providers, body("Bot's", "https://public.tools.test/v1"), nil)
	mustCode(t, "a service account key adds a provider", code, ec, 401, "")
	code, ec = bot.call("GET", providers, nil, nil)
	mustCode(t, "a service account key lists providers", code, ec, 401, "")

	// Another team, or the platform's provider through a team route, is not found.
	code, ec = e.outsider.call("GET", pbase, nil, nil, nil)
	mustCode(t, "another team's provider list", code, ec, 404, "team_not_found")
	code, ec = e.outsider.call("GET", "/v1/teams/other/model-providers/"+prov.Id.String(), nil, nil, nil)
	mustCode(t, "another team's provider through one's own team", code, ec, 404, "model_provider_not_found")
	code, ec = e.outsider.call("DELETE", "/v1/teams/other/model-providers/"+prov.Id.String(), nil, nil, nil)
	mustCode(t, "deleting another team's provider", code, ec, 404, "model_provider_not_found")
	code, ec = e.owner.call("GET", providers+"/"+platformProv.Id.String(), nil, nil, nil)
	mustCode(t, "the platform's provider through a team route", code, ec, 404, "model_provider_not_found")
	code, ec = e.owner.call("POST", providers+"/"+platformProv.Id.String()+"/models", map[string]any{"upstreamId": "x"}, nil, nil)
	mustCode(t, "a model on the platform's provider through a team route", code, ec, 404, "model_provider_not_found")

	// Discovery and models, through the address guard.
	var found []apitypes.DiscoveredModel
	code, ec = e.owner.call("POST", pbase+"/discover", nil, &found, nil)
	mustCode(t, "discover", code, ec, 200, "")
	if len(found) != 2 || found[0].Id != "fake-1" {
		t.Fatalf("discovered %+v", found)
	}
	code, ec = e.owner.call("POST", pbase+"/models", map[string]any{"upstreamId": "fake-2", "visibility": "all"}, nil, nil)
	mustCode(t, "a team model for every team", code, ec, 400, "team_model_visibility")
	code, ec = e.owner.call("POST", pbase+"/models", map[string]any{"upstreamId": "fake-2", "teams": []string{"other"}}, nil, nil)
	mustCode(t, "a team model for another team", code, ec, 400, "team_model_visibility")
	var m1, m2 apitypes.Model
	code, ec = e.owner.call("POST", pbase+"/models", map[string]any{"upstreamId": "fake-1", "name": "Team One", "inputPrice": 2}, &m1, nil)
	mustCode(t, "add fake-1", code, ec, 201, "")
	code, ec = e.owner.call("POST", pbase+"/models", map[string]any{"upstreamId": "fake-2"}, &m2, nil)
	mustCode(t, "add fake-2", code, ec, 201, "")
	if m1.ProviderTeam == nil || m1.ProviderTeam.Slug != "tools" || m1.Personal || m1.Teams != nil {
		t.Fatalf("team model %+v", m1)
	}
	code, ec = e.member.call("POST", pbase+"/models", map[string]any{"upstreamId": "fake-3"}, nil, nil)
	mustCode(t, "a member adds a model", code, ec, 403, "forbidden")
	var provModels []apitypes.Model
	if code := e.member.get(pbase+"/models", &provModels); code != 200 || len(provModels) != 2 {
		t.Fatalf("provider models: %d %+v", code, provModels)
	}
	code, ec = e.owner.call("PATCH", toolsTeam+"/models/"+m2.Id.String(), map[string]any{"name": "Team Two", "personal": true}, nil, ifMatch(m2.Revision))
	mustCode(t, "a team model in personal workspaces", code, ec, 400, "team_model_visibility")
	code, ec = e.owner.call("PATCH", toolsTeam+"/models/"+m2.Id.String(), map[string]any{"name": "Team Two"}, &m2, ifMatch(m2.Revision))
	mustCode(t, "rename fake-2", code, ec, 200, "")
	code, ec = e.outsider.call("PATCH", "/v1/teams/other/models/"+m2.Id.String(), map[string]any{"name": "Mine"}, nil, ifMatch(m2.Revision))
	mustCode(t, "another team's model through one's own team", code, ec, 404, "model_not_found")
	code, ec = e.admin.call("PATCH", "/v1/admin/models/"+m2.Id.String(), map[string]any{"visibility": "all"}, nil, ifMatch(m2.Revision))
	mustCode(t, "an admin publishes a team model to everyone", code, ec, 400, "team_model_visibility")

	// The models are the team's only, whatever reads them.
	var mine, theirs []apitypes.Model
	e.member.get(toolsTeam+"/models", &mine)
	e.outsider.get("/v1/teams/other/models", &theirs)
	if len(mine) != 2 || mine[0].ProviderTeam == nil || len(theirs) != 0 {
		t.Fatalf("available: tools %+v, other %+v", mine, theirs)
	}
	if code, _ := bot.call("GET", toolsTeam+"/models", nil, &mine); code != 200 || len(mine) != 2 {
		t.Fatalf("a service account lists the team's models: %d %+v", code, mine)
	}

	// Templates: the team's may use them, another team's and platform templates may not.
	var tpl apitypes.AgentTemplate
	code, ec = e.editor.call("POST", toolsTeam+"/agent-templates",
		map[string]any{"slug": "own", "name": "Own", "instructions": "Be brief.", "modelId": m1.Id}, &tpl, nil)
	mustCode(t, "a team template on a team model", code, ec, 201, "")
	code, ec = e.outsider.call("POST", "/v1/teams/other/agent-templates",
		map[string]any{"slug": "x", "name": "X", "instructions": "Hi.", "modelId": m1.Id}, nil, nil)
	mustCode(t, "another team's template on it", code, ec, 409, "model_unavailable")
	code, ec = e.admin.call("POST", "/v1/agent-templates", map[string]any{"slug": "p", "name": "P", "instructions": "Hi.", "modelId": m1.Id}, nil, nil)
	mustCode(t, "a platform template on it", code, ec, 409, "team_model")
	code, ec = e.owner.call("POST", "/v1/agent-templates/"+tpl.Id.String()+"/publish", nil, nil, nil)
	mustCode(t, "publishing a template on a team model", code, ec, 409, "team_model")
	// Nor can a template on its way to the catalog move to one.
	var pub apitypes.AgentTemplate
	code, ec = e.editor.call("POST", toolsTeam+"/agent-templates", map[string]any{"slug": "pub", "name": "Pub", "instructions": "Hi."}, &pub, nil)
	mustCode(t, "a template on the default model", code, ec, 201, "")
	code, ec = e.owner.call("POST", "/v1/agent-templates/"+pub.Id.String()+"/publish", nil, nil, nil)
	mustCode(t, "ask to publish it", code, ec, 200, "")
	code, ec = e.editor.call("POST", "/v1/agent-templates/"+pub.Id.String()+"/versions",
		map[string]any{"instructions": "Hi.", "modelId": m1.Id.String()}, nil, nil)
	mustCode(t, "a pending template's version on a team model", code, ec, 409, "team_model")
	e.scalar(`UPDATE agent_template_versions SET model_id = $2 WHERE template_id = $1 RETURNING model_id::text`, pub.Id, m1.Id)
	code, ec = e.admin.call("POST", "/v1/agent-templates/"+pub.Id.String()+"/review", map[string]any{"decision": "approve"}, nil, nil)
	mustCode(t, "approving a template on a team model", code, ec, 409, "team_model")

	// An agent of the team runs on it through the model proxy; the provider sees its own key.
	var ag apitypes.Agent
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Own bot", "templateId": tpl.Id}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	if ag.Model == nil || ag.Model.Id != m1.Id || !ag.Model.Available {
		t.Fatalf("agent model %+v", ag.Model)
	}
	code, ec = e.outsider.call("POST", "/v1/teams/other/agents", map[string]any{"name": "X", "modelId": m1.Id}, nil, nil)
	mustCode(t, "another team's agent on it", code, ec, 409, "model_unavailable")
	spec := e.spec(ag.Id)
	if spec.Env["MODEL_BASE_URL"] != e.app.GatewayURL+"/model-proxy/"+prov.Id.String() || strings.Contains(spec.Env["MODEL_BASE_URL"], "tools.test") {
		t.Fatalf("runtime env %v", spec.Env)
	}
	cred := spec.Env["MODEL_API_KEY"]
	if code, ec := proxyCall(t, e.app.GatewayURL, prov.Id.String(), cred, "fake-1"); code != 200 {
		t.Fatalf("proxied call: %d %s", code, ec)
	}
	if auths, models := fake.calls(); len(auths) != 1 || auths[0] != "Bearer "+fakeProviderKey || models[0] != "fake-1" {
		t.Fatalf("provider saw %v %v", auths, models)
	}

	// Another team's agent can't, even if its model were set to it.
	var other apitypes.Agent
	code, ec = e.outsider.call("POST", "/v1/teams/other/agents", map[string]any{"name": "Other bot"}, &other, nil)
	mustCode(t, "another team's agent", code, ec, 201, "")
	otherSpec, err := e.app.Agents.Spec(context.Background(), authz.Actor{UserID: e.userID("casey@localhost"), PlatformRole: authz.PlatformNone}, other.Id)
	if err != nil {
		t.Fatal(err)
	}
	otherCred := otherSpec.Env["PLATFORM_KEY"]
	e.scalar(`UPDATE agents SET model_id = $2 WHERE id = $1 RETURNING id::text`, other.Id, m1.Id)
	if code, ec := proxyCall(t, e.app.GatewayURL, prov.Id.String(), otherCred, "fake-1"); code != 403 || ec != "model_unavailable" {
		t.Fatalf("another team's agent through the proxy: %d %s", code, ec)
	}
	e.scalar(`UPDATE agents SET model_id = NULL WHERE id = $1 RETURNING id::text`, other.Id)

	// Usage names the team's model and provider; the platform's report too.
	var rep apitypes.UsageReport
	if code := e.member.get(toolsTeam+"/usage?groupBy=model", &rep); code != 200 || len(rep.Rows) != 1 || rep.Rows[0].ModelName == nil ||
		*rep.Rows[0].ModelName != "Team One" || rep.Rows[0].ProviderName == nil || *rep.Rows[0].ProviderName != "Team gateway" ||
		rep.Total.InputTokens != 42 || rep.Total.CostUsd <= 0 {
		t.Fatalf("team usage: %d %+v", code, rep)
	}
	if code := e.auditor.get("/v1/admin/usage?groupBy=model", &rep); code != 200 || len(rep.Rows) != 1 || *rep.Rows[0].ModelName != "Team One" {
		t.Fatalf("platform usage: %d %+v", code, rep)
	}

	// Who may add one now (the UI's button).
	if !e.canAddProviders(e.owner, "tools") || e.canAddProviders(e.member, "tools") || e.canAddProviders(e.admin, "tools") {
		t.Fatal("canAddProviders: owner yes, member and platform admin no")
	}

	// Turned off, no new providers; existing ones keep working and changing.
	e.setModelSettings(false, "10.20.0.0/16")
	code, ec = e.owner.call("POST", providers, body("Second", "https://public.tools.test/v1"), nil, nil)
	mustCode(t, "a provider while turned off", code, ec, 409, "team_providers_off")
	if e.canAddProviders(e.owner, "tools") {
		t.Fatal("canAddProviders while turned off")
	}
	code, ec = e.owner.call("PATCH", pbase, map[string]any{"name": "Team gateway"}, &prov, ifMatch(prov.Revision))
	mustCode(t, "changing it while turned off", code, ec, 200, "")
	if code, ec := proxyCall(t, e.app.GatewayURL, prov.Id.String(), cred, "fake-1"); code != 200 {
		t.Fatalf("proxied call while turned off: %d %s", code, ec)
	}
	e.setModelSettings(true, "10.20.0.0/16")

	// The guard decides at every connection: once its network is no longer
	// allowed, the provider is not reached.
	e.setModelSettings(true)
	code, ec = e.owner.call("POST", pbase+"/discover", nil, nil, nil)
	mustCode(t, "discover off the allowed networks", code, ec, 422, "address_not_allowed")
	if code, ec := proxyCall(t, e.app.GatewayURL, prov.Id.String(), cred, "fake-1"); code != 502 || ec != "address_not_allowed" {
		t.Fatalf("proxied call off the allowed networks: %d %s", code, ec)
	}
	e.setModelSettings(true, "10.20.0.0/16")
	hosts.set("models.tools.test", "169.254.169.254") // DNS now points at the metadata service
	code, ec = e.owner.call("POST", pbase+"/discover", nil, nil, nil)
	mustCode(t, "discover after the name moved", code, ec, 422, "address_not_allowed")
	hosts.set("models.tools.test", "10.20.30.40")
	code, ec = e.owner.call("PATCH", pbase, map[string]any{"baseUrl": "https://127.0.0.1/v1"}, nil, ifMatch(prov.Revision))
	mustCode(t, "moving it to loopback", code, ec, 422, "address_not_allowed")

	// Platform admins see every owner's providers and govern teams'.
	var all, plat, teamOnly []apitypes.ModelProvider
	e.auditor.get("/v1/admin/model-providers", &all)
	e.auditor.get("/v1/admin/model-providers?owner=platform", &plat)
	e.auditor.get("/v1/admin/model-providers?owner=tools", &teamOnly)
	if len(all) != 2 || all[0].Team != nil || all[1].Team == nil || all[1].Team.Slug != "tools" ||
		len(plat) != 1 || plat[0].Id != platformProv.Id || len(teamOnly) != 1 || teamOnly[0].Id != prov.Id {
		t.Fatalf("admin lists: all %+v, platform %+v, tools %+v", all, plat, teamOnly)
	}
	code, ec = e.auditor.call("GET", "/v1/admin/model-providers?owner=nope", nil, nil, nil)
	mustCode(t, "an unknown owner", code, ec, 404, "team_not_found")
	var adminModels []apitypes.Model
	if code := e.auditor.get("/v1/admin/models?owner=tools", &adminModels); code != 200 || len(adminModels) != 2 ||
		adminModels[0].ProviderTeam == nil || adminModels[0].ProviderTeam.Slug != "tools" {
		t.Fatalf("admin models: %d %+v", code, adminModels)
	}
	code, ec = e.admin.call("PATCH", "/v1/admin/model-providers/"+prov.Id.String(), map[string]any{"accessMode": "direct"}, nil, ifMatch(prov.Revision))
	mustCode(t, "an admin makes a team provider direct", code, ec, 400, "invalid_access_mode")
	code, ec = e.admin.call("PATCH", "/v1/admin/model-providers/"+prov.Id.String(), map[string]any{"enabled": false}, &prov, ifMatch(prov.Revision))
	mustCode(t, "an admin turns it off", code, ec, 200, "")
	if e.member.get(toolsTeam+"/models", &mine); len(mine) != 0 {
		t.Fatalf("models of a disabled team provider: %+v", mine)
	}
	code, ec = e.owner.call("PATCH", pbase, map[string]any{"enabled": true}, &prov, ifMatch(prov.Revision))
	mustCode(t, "the owner turns it back on", code, ec, 200, "")

	// Deleting: refused while used; an unused model goes.
	code, ec = e.owner.call("DELETE", toolsTeam+"/models/"+m1.Id.String(), nil, nil, nil)
	mustCode(t, "delete a model in use", code, ec, 409, "model_in_use")
	code, ec = e.owner.call("DELETE", toolsTeam+"/models/"+m2.Id.String(), nil, nil, nil)
	mustCode(t, "delete an unused model", code, ec, 200, "")

	// Audited in the team's log (as the admin catalog is), never with the key.
	page := auditPage(t, e.editor, toolsTeam+"/audit", url.Values{"action": {"model_provider.create"}})
	if entry := findAudit(t, page, "model_provider.create", prov.Id.String()); entry.TargetLabel == nil || *entry.TargetLabel != "Team gateway" {
		t.Fatalf("team audit entry %+v", entry)
	}
	page = auditPage(t, e.editor, toolsTeam+"/audit", url.Values{"action": {"model_provider.update"}})
	findAudit(t, page, "model_provider.update", prov.Id.String()) // the platform admin's too
	page = auditPage(t, e.auditor, "/v1/admin/audit", url.Values{"action": {"platform.model_settings"}})
	if entry := findAudit(t, page, "platform.model_settings", "models"); entry.TargetLabel == nil || *entry.TargetLabel != "Model settings" {
		t.Fatalf("settings audit entry %+v", entry)
	}
	if got := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action LIKE 'model%' AND team_id IS NOT NULL
		AND (coalesce(before_state::text, '') || coalesce(after_state::text, '') || coalesce(metadata::text, '')) LIKE '%' || $1 || '%'`,
		fakeProviderKey); got != "0" {
		t.Fatalf("audit entries with the key: %s", got)
	}

	// Deleting the team takes its providers along, with the agents and
	// templates that use their models (which keep a model from being
	// deleted on its own).
	code, ec = e.editor.call("PATCH", "/v1/agents/"+ag.Id.String(), map[string]any{"modelId": m1.Id.String()}, &ag, ifMatch(ag.Revision))
	mustCode(t, "the agent's own model", code, ec, 200, "")
	if ag.Model == nil || ag.Model.Source != "agent" {
		t.Fatalf("agent model %+v", ag.Model)
	}
	if got := e.scalar(`WITH d AS (DELETE FROM teams WHERE slug = 'tools' RETURNING id) SELECT count(*)::text FROM d`); got != "1" {
		t.Fatalf("deleted %s teams", got)
	}
	if got := e.scalar(`SELECT count(*)::text FROM model_providers WHERE team_id IS NOT NULL`); got != "0" {
		t.Fatalf("%s team providers left", got)
	}
}

// Personal workspaces add their own providers only when platform admins let
// them.
func TestPersonalWorkspaceProviders(t *testing.T) {
	app := newTestAppWith(t, nil, testutil.Logger(), nil, true)
	fake := newFakeTLSProvider(t)
	useTeamNetwork(t, app, fake.srv, map[string]string{"api.example.test": "93.184.216.34"})
	admin, user := app.signIn("admin"), app.signIn("user")
	var mine []apitypes.MyTeam
	if code := user.get("/v1/teams", &mine); code != 200 || len(mine) != 1 {
		t.Fatalf("teams %d %+v", code, mine)
	}
	ws := "/v1/teams/" + mine[0].Slug
	body := map[string]any{"name": "Mine", "kind": "openai-compatible", "baseUrl": "https://api.example.test/v1", "apiKey": fakeProviderKey}

	code, ec := user.call("POST", ws+"/model-providers", body, nil, nil)
	mustCode(t, "a personal provider by default", code, ec, 409, "personal_workspace")
	var limits apitypes.WorkspaceLimits
	if user.get(ws+"/limits", &limits); limits.CanAddProviders == nil || *limits.CanAddProviders {
		t.Fatalf("limits %+v", limits)
	}

	var settings apitypes.PersonalWorkspaceSettings
	admin.get("/v1/admin/settings/personal-workspaces", &settings)
	if settings.OwnProviders == nil || *settings.OwnProviders {
		t.Fatalf("settings %+v", settings)
	}
	code, ec = admin.call("PUT", "/v1/admin/settings/personal-workspaces", map[string]any{"enabled": true, "maxAgents": 5, "maxRunning": 2,
		"ownProviders": true}, &settings, ifMatch(settings.Revision))
	mustCode(t, "let personal workspaces add providers", code, ec, 200, "")
	if settings.OwnProviders == nil || !*settings.OwnProviders {
		t.Fatalf("settings %+v", settings)
	}
	// Omitted, it is kept.
	code, ec = admin.call("PUT", "/v1/admin/settings/personal-workspaces", map[string]any{"enabled": true, "maxAgents": 5, "maxRunning": 2},
		&settings, ifMatch(settings.Revision))
	mustCode(t, "change other settings", code, ec, 200, "")
	if settings.OwnProviders == nil || !*settings.OwnProviders {
		t.Fatalf("ownProviders not kept: %+v", settings)
	}

	var prov apitypes.ModelProvider
	code, ec = user.call("POST", ws+"/model-providers", body, &prov, nil)
	mustCode(t, "a personal provider when allowed", code, ec, 201, "")
	if prov.Team == nil || prov.Team.Kind != "personal" {
		t.Fatalf("personal provider %+v", prov)
	}
	var m apitypes.Model
	code, ec = user.call("POST", ws+"/model-providers/"+prov.Id.String()+"/models", map[string]any{"upstreamId": "fake-1"}, &m, nil)
	mustCode(t, "a personal model", code, ec, 201, "")
	var avail []apitypes.Model
	if code := user.get(ws+"/models", &avail); code != 200 || len(avail) != 1 || avail[0].Id != m.Id {
		t.Fatalf("personal models: %d %+v", code, avail)
	}
	if user.get(ws+"/limits", &limits); limits.CanAddProviders == nil || !*limits.CanAddProviders {
		t.Fatalf("limits %+v", limits)
	}
}
