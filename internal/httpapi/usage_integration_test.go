package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/llm"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// usageEnv is a team ("tools") with three agents, a service-managed one and
// a deleted one's usage, another team ("other") with an agent, and a
// personal workspace (blair's), with usage on fixed days in March 2026.
type usageEnv struct {
	*teamEnv
	alpha, beta, gamma, managed, omega, mine string
	ws                                       string // blair's personal workspace, as a path
}

const usageRange = "&from=2026-03-01&to=2026-03-05"

func newUsageEnv(t *testing.T) *usageEnv {
	t.Helper()
	app := newTestAppWith(t, nil, testutil.Logger(), nil, true)
	e := &usageEnv{teamEnv: newTeamEnvOn(t, app)}
	agent := func(s *session, team, name string) string {
		t.Helper()
		var ag apitypes.Agent
		code, ec := s.call("POST", team+"/agents", map[string]any{"name": name}, &ag, nil)
		mustCode(t, "create "+name, code, ec, 201, "")
		return ag.Id.String()
	}
	e.alpha, e.beta, e.gamma, e.managed = agent(e.editor, toolsTeam, "Alpha"), agent(e.editor, toolsTeam, "Beta"),
		agent(e.editor, toolsTeam, "Gamma"), agent(e.editor, toolsTeam, "Delta")
	e.omega = agent(e.outsider, "/v1/teams/other", "Omega")
	// Service-managed: created for an app user by a service account.
	exec(t, app, `UPDATE agents SET external_key = 'app-user-1' WHERE id = $1`, e.managed)
	var mine []apitypes.Team
	if code := e.member.get("/v1/teams", &mine); code != 200 {
		t.Fatalf("blair's teams: %d", code)
	}
	for _, tm := range mine {
		if tm.Kind != nil && *tm.Kind == "personal" {
			e.ws = "/v1/teams/" + tm.Slug
		}
	}
	if e.ws == "" {
		t.Fatalf("blair has no personal workspace: %+v", mine)
	}
	e.mine = agent(e.member, e.ws, "Mine")

	// A priced model: $2 per million input tokens, $4 per million output.
	admin := authz.Actor{UserID: uuid.MustParse(e.scalar(`SELECT id::text FROM users WHERE email = 'admin@localhost'`)), PlatformRole: authz.PlatformAdmin}
	prov, err := app.Models.CreateProvider(context.Background(), admin, llm.ProviderInput{Name: "Usage provider", Kind: "openai-compatible",
		BaseURL: "http://127.0.0.1:9/v1", APIKey: "usage-key"})
	if err != nil {
		t.Fatal(err)
	}
	in, out := 2.0, 4.0
	m, err := app.Models.CreateModel(context.Background(), admin, prov.Provider.ID, llm.ModelInput{UpstreamID: "priced", InputPrice: &in, OutputPrice: &out})
	if err != nil {
		t.Fatal(err)
	}
	model := m.Model.ID.String()
	team := func(slug string) string { return e.scalar(`SELECT id::text FROM teams WHERE slug = $1`, slug) }
	tools, other, personal := team("tools"), team("other"), team(strings.TrimPrefix(e.ws, "/v1/teams/"))
	use := func(teamID, agentID, modelID, at string, requests, input, output int) {
		var ag, md any
		if agentID != "" {
			ag = agentID
		}
		if modelID != "" {
			md = modelID
		}
		exec(t, app, `INSERT INTO model_usage (team_id, agent_id, model_id, source, requests, input_tokens, output_tokens, created_at)
			VALUES ($1, $2, $3, 'agent', $4, $5, $6, $7)`, teamID, ag, md, requests, input, output, at)
	}
	use(tools, e.alpha, model, "2026-03-01T01:00:00Z", 2, 500000, 250000)
	use(tools, e.alpha, model, "2026-03-01T23:59:59Z", 1, 100, 10)
	use(tools, e.alpha, "", "2026-03-02T12:00:00Z", 1, 50, 5) // the platform's default model: unpriced
	use(tools, e.alpha, model, "2026-03-03T00:00:00Z", 1, 1000, 100)
	use(tools, e.alpha, model, "2026-03-10T00:00:00Z", 9, 9, 9) // outside the range
	use(tools, e.beta, model, "2026-03-02T08:00:00Z", 3, 3000, 300)
	use(tools, e.gamma, model, "2026-03-01T08:00:00Z", 1, 7, 7)
	use(tools, e.managed, model, "2026-03-02T09:00:00Z", 1, 11, 11)
	use(tools, "", model, "2026-03-03T09:00:00Z", 1, 13, 13) // a deleted agent
	use(other, e.omega, model, "2026-03-01T09:00:00Z", 5, 99999, 9999)
	use(personal, e.mine, model, "2026-03-04T09:00:00Z", 2, 20, 2)
	return e
}

// report gets a usage report, failing unless it is a 200.
func report(t *testing.T, s *session, path string) apitypes.UsageReport {
	t.Helper()
	var rep apitypes.UsageReport
	code, ec := s.call("GET", path, nil, &rep, nil)
	mustCode(t, path, code, ec, 200, "")
	return rep
}

func agentIDs(ids ...string) string {
	var b strings.Builder
	for _, id := range ids {
		b.WriteString("&agentId=" + url.QueryEscape(id))
	}
	return b.String()
}

// rowKey is a row's agent and day.
func rowKey(r apitypes.UsageRow) string {
	agent, day := "-", "-"
	switch {
	case r.AgentId != nil:
		agent = r.AgentId.String()
	case r.Managed:
		agent = "managed"
	}
	if r.Day != nil {
		day = r.Day.String()
	}
	return agent + "@" + day
}

func keys(rows []apitypes.UsageRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = rowKey(r)
	}
	return out
}

func sameNumbers(a, b apitypes.UsageRow) bool {
	return a.Requests == b.Requests && a.InputTokens == b.InputTokens && a.OutputTokens == b.OutputTokens &&
		a.CacheReadTokens == b.CacheReadTokens && a.CacheWriteTokens == b.CacheWriteTokens &&
		a.UnpricedRequests == b.UnpricedRequests && math.Abs(a.CostUsd-b.CostUsd) < 1e-9
}

func sumRows(rows []apitypes.UsageRow) apitypes.UsageRow {
	var s apitypes.UsageRow
	for _, r := range rows {
		s.Requests += r.Requests
		s.InputTokens += r.InputTokens
		s.OutputTokens += r.OutputTokens
		s.CacheReadTokens += r.CacheReadTokens
		s.CacheWriteTokens += r.CacheWriteTokens
		s.CostUsd += r.CostUsd
		s.UnpricedRequests += r.UnpricedRequests
	}
	return s
}

// One request returns several agents' daily usage: one row per agent and
// day, by agent name then day, each equal to the agent's own daily report.
func TestUsageByAgentAndDay(t *testing.T) {
	e := newUsageEnv(t)
	base := toolsTeam + "/usage?groupBy=agent,day" + usageRange

	rep := report(t, e.member, base+agentIDs(e.beta, e.alpha))
	want := []string{e.alpha + "@2026-03-01", e.alpha + "@2026-03-02", e.alpha + "@2026-03-03", e.beta + "@2026-03-02"}
	if rep.GroupBy != "agent,day" || fmt.Sprint(keys(rep.Rows)) != fmt.Sprint(want) {
		t.Fatalf("rows %v, want %v", keys(rep.Rows), want)
	}
	if rep.Rows[0].AgentName == nil || *rep.Rows[0].AgentName != "Alpha" || rep.Rows[0].Requests != 3 || rep.Rows[0].InputTokens != 500100 ||
		rep.Rows[1].UnpricedRequests != 1 || rep.Rows[1].CostUsd != 0 || rep.Total.Requests != 8 || !sameNumbers(rep.Total, sumRows(rep.Rows)) {
		t.Fatalf("report %+v", rep)
	}

	// Each agent's rows equal its own report by day; the totals add up.
	var perAgent []apitypes.UsageRow
	for _, id := range []string{e.alpha, e.beta} {
		own := report(t, e.member, toolsTeam+"/usage?groupBy=day"+usageRange+agentIDs(id))
		var mine []apitypes.UsageRow
		for _, r := range rep.Rows {
			if r.AgentId != nil && r.AgentId.String() == id {
				mine = append(mine, r)
			}
		}
		if len(mine) != len(own.Rows) {
			t.Fatalf("agent %s: %d grouped rows, %d daily", id, len(mine), len(own.Rows))
		}
		for i := range mine {
			if mine[i].Day.String() != own.Rows[i].Day.String() || !sameNumbers(mine[i], own.Rows[i]) {
				t.Fatalf("agent %s day %s: %+v vs %+v", id, own.Rows[i].Day, mine[i], own.Rows[i])
			}
		}
		perAgent = append(perAgent, own.Total)
	}
	if !sameNumbers(rep.Total, sumRows(perAgent)) {
		t.Fatalf("total %+v, per-agent totals %+v", rep.Total, perAgent)
	}
	// The same agents grouped by agent: the same totals.
	byAgent := report(t, e.member, toolsTeam+"/usage?groupBy=agent"+usageRange+agentIDs(e.alpha, e.beta))
	if len(byAgent.Rows) != 2 || !sameNumbers(byAgent.Total, rep.Total) {
		t.Fatalf("by agent %+v", byAgent)
	}

	// Unfiltered, a member sees service-managed agents summed after the
	// named ones, then deleted agents; the total is the team's.
	all := report(t, e.member, base)
	want = []string{e.alpha + "@2026-03-01", e.alpha + "@2026-03-02", e.alpha + "@2026-03-03", e.beta + "@2026-03-02",
		e.gamma + "@2026-03-01", "managed@2026-03-02", "-@2026-03-03"}
	if fmt.Sprint(keys(all.Rows)) != fmt.Sprint(want) {
		t.Fatalf("all rows %v, want %v", keys(all.Rows), want)
	}
	if all.Rows[5].AgentName != nil {
		t.Fatalf("a member sees a service-managed agent's name: %+v", all.Rows[5])
	}
	byDay := report(t, e.member, toolsTeam+"/usage?groupBy=day"+usageRange)
	if !sameNumbers(all.Total, byDay.Total) || !sameNumbers(all.Total, sumRows(byDay.Rows)) || all.Total.Requests != 11 {
		t.Fatalf("totals %+v vs %+v", all.Total, byDay.Total)
	}
	// The owner sees the service-managed agent by name, in its place.
	owned := report(t, e.owner, base)
	if len(owned.Rows) != 7 || owned.Rows[4].AgentName == nil || *owned.Rows[4].AgentName != "Delta" || !owned.Rows[4].Managed ||
		owned.Rows[5].AgentId.String() != e.gamma {
		t.Fatalf("owner rows %v", keys(owned.Rows))
	}

	// An empty range: no rows, zero totals.
	empty := report(t, e.member, toolsTeam+"/usage?groupBy=agent,day&from=2026-04-01&to=2026-04-02"+agentIDs(e.alpha))
	if len(empty.Rows) != 0 || empty.Total.Requests != 0 || empty.Total.CostUsd != 0 {
		t.Fatalf("empty range %+v", empty)
	}
}

// The filter: duplicates count once; another team's agent, an unknown one,
// and (for callers who see them summed) a service-managed one match
// nothing, without an error; the IDs are checked and bounded.
func TestUsageAgentFilter(t *testing.T) {
	e := newUsageEnv(t)
	base := toolsTeam + "/usage?groupBy=agent,day" + usageRange
	alpha := report(t, e.member, base+agentIDs(e.alpha))
	for name, ids := range map[string][]string{
		"a duplicate":          {e.alpha, e.alpha},
		"another team's agent": {e.alpha, e.omega},
		"an unknown agent":     {e.alpha, uuid.NewString()},
		"a personal agent":     {e.alpha, e.mine},
		"a hidden managed one": {e.alpha, e.managed},
	} {
		got := report(t, e.member, base+agentIDs(ids...))
		if fmt.Sprint(keys(got.Rows)) != fmt.Sprint(keys(alpha.Rows)) || !sameNumbers(got.Total, alpha.Total) {
			t.Fatalf("%s: %v / %+v, want %v", name, keys(got.Rows), got.Total, keys(alpha.Rows))
		}
	}
	for name, ids := range map[string][]string{
		"only another team's agent": {e.omega},
		"only an unknown agent":     {uuid.NewString()},
		"only a hidden managed one": {e.managed},
	} {
		for _, g := range []string{"agent,day", "day", "agent", "model"} {
			got := report(t, e.member, toolsTeam+"/usage?groupBy="+g+usageRange+agentIDs(ids...))
			if len(got.Rows) != 0 || got.Total.Requests != 0 {
				t.Fatalf("%s by %s: %+v", name, g, got)
			}
		}
	}
	// Who sees service-managed agents gets theirs.
	owned := report(t, e.owner, base+agentIDs(e.managed))
	if len(owned.Rows) != 1 || owned.Rows[0].AgentName == nil || *owned.Rows[0].AgentName != "Delta" {
		t.Fatalf("owner's managed agent %+v", owned)
	}
	// An empty agentId is no filter (as before).
	if got := report(t, e.member, base+"&agentId="); len(got.Rows) != 7 {
		t.Fatalf("empty agentId: %v", keys(got.Rows))
	}

	// Validation.
	code, ec := e.member.call("GET", base+"&agentId=nope", nil, nil, nil)
	mustCode(t, "a bad agent ID", code, ec, 400, "invalid_agent_id")
	many := make([]string, 101)
	for i := range many {
		many[i] = uuid.NewString()
	}
	code, ec = e.member.call("GET", base+agentIDs(many...), nil, nil, nil)
	mustCode(t, "101 agents", code, ec, 400, "too_many_agents")
	many = append(many[:99], e.alpha)
	if got := report(t, e.member, base+agentIDs(many...)); !sameNumbers(got.Total, alpha.Total) {
		t.Fatalf("100 agents %+v", got.Total)
	}
	for _, g := range []string{"day,agent", "agent,model", "team"} {
		code, ec = e.member.call("GET", toolsTeam+"/usage?groupBy="+url.QueryEscape(g), nil, nil, nil)
		mustCode(t, "groupBy "+g, code, ec, 400, "invalid_group_by")
	}
	code, ec = e.member.call("GET", toolsTeam+"/usage?groupBy=agent,day&from=2025-01-01&to=2026-03-01", nil, nil, nil)
	mustCode(t, "over 366 days", code, ec, 400, "invalid_range")
	// The platform's report doesn't group by agent.
	code, ec = e.auditor.call("GET", "/v1/admin/usage?groupBy=agent,day", nil, nil, nil)
	mustCode(t, "the platform by agent and day", code, ec, 400, "invalid_group_by")
}

// Who may read it: the team's members and service accounts, platform
// staff, and an app acting for a member (for that person's teams and
// personal workspace only); never another team's caller.
func TestUsageByAgentAndDayAccess(t *testing.T) {
	e := newUsageEnv(t)
	q := "/usage?groupBy=agent,day" + usageRange
	ids := agentIDs(e.alpha, e.beta)
	want := keys(report(t, e.member, toolsTeam+q+ids).Rows)

	code, ec := e.outsider.call("GET", toolsTeam+q+agentIDs(e.alpha, e.omega), nil, nil, nil)
	mustCode(t, "another team's member", code, ec, 404, "")
	code, ec = e.owner.call("GET", e.ws+q, nil, nil, nil)
	mustCode(t, "someone else's personal workspace", code, ec, 404, "")
	if got := report(t, e.auditor, toolsTeam+q+agentIDs(e.managed)); len(got.Rows) != 1 || got.Rows[0].AgentName == nil {
		t.Fatalf("auditor %+v", got)
	}
	// The other team's own report has its own agent only.
	if got := report(t, e.outsider, "/v1/teams/other"+q+agentIDs(e.omega, e.alpha)); len(got.Rows) != 1 || got.Rows[0].AgentId.String() != e.omega {
		t.Fatalf("other team %v", keys(got.Rows))
	}

	// A service account of the team sees its service-managed agents by name.
	key := createSAKey(t, e.owner, createSA(t, e.owner, map[string]any{"name": "chat-app"}).Id.String(), map[string]any{"name": "k"}).Secret
	var rep apitypes.UsageReport
	if c, ec, _ := bearer(t, e.app, key, "GET", toolsTeam+q+agentIDs(e.alpha, e.managed), nil, &rep); c != 200 || len(rep.Rows) != 4 ||
		rep.Rows[3].AgentName == nil || *rep.Rows[3].AgentName != "Delta" {
		t.Fatalf("service account %d %s %v", c, ec, keys(rep.Rows))
	}
	if c, _, _ := bearer(t, e.app, key, "GET", "/v1/teams/other"+q, nil, nil); c != 404 {
		t.Fatalf("service account on another team: %d", c)
	}

	// An app acting for blair: blair's teams and personal workspace, as blair.
	regBody, _ := json.Marshal(map[string]any{"client_name": "Chat", "redirect_uris": []string{"http://127.0.0.1/cb"},
		"token_endpoint_auth_method": "none"})
	res, err := http.Post(e.app.URL+"/oauth/register", "application/json", bytes.NewReader(regBody))
	if err != nil {
		t.Fatal(err)
	}
	var reg map[string]any
	_ = json.NewDecoder(res.Body).Decode(&reg)
	res.Body.Close()
	clientID, _ := reg["client_id"].(string)
	f := oauthFlow{t, e.app}
	access := connect(t, f, e.member, clientID, "s1")
	rep = apitypes.UsageReport{}
	if c, ec, _ := bearer(t, e.app, access, "GET", toolsTeam+q+ids, nil, &rep); c != 200 || fmt.Sprint(keys(rep.Rows)) != fmt.Sprint(want) {
		t.Fatalf("app for blair on tools: %d %s %v", c, ec, keys(rep.Rows))
	}
	rep = apitypes.UsageReport{}
	if c, ec, _ := bearer(t, e.app, access, "GET", e.ws+q+agentIDs(e.mine, e.alpha), nil, &rep); c != 200 || len(rep.Rows) != 1 ||
		rep.Rows[0].AgentId.String() != e.mine || rep.Rows[0].Requests != 2 {
		t.Fatalf("app for blair on her workspace: %d %s %+v", c, ec, rep)
	}
	if c, _, _ := bearer(t, e.app, access, "GET", "/v1/teams/other"+q, nil, nil); c != 404 {
		t.Fatalf("app for blair on another team: %d", c)
	}
	// An app acting for a platform auditor gets no platform powers.
	audAccess := connect(t, f, e.auditor, clientID, "s2")
	if c, _, _ := bearer(t, e.app, audAccess, "GET", toolsTeam+q, nil, nil); c != 404 {
		t.Fatalf("app for the auditor on tools: %d", c)
	}
	if c, _, _ := bearer(t, e.app, audAccess, "GET", "/v1/admin/usage", nil, nil); c != 401 {
		t.Fatalf("app for the auditor on the platform's usage: %d", c)
	}
}

// The existing shapes, pinned: groupBy day (the default), model and agent,
// with one agentId, read exactly as before agent,day and several agents.
func TestUsageReportShapesUnchanged(t *testing.T) {
	e := newUsageEnv(t)
	model := e.scalar(`SELECT id::text FROM models WHERE upstream_id = 'priced'`)
	for _, c := range []struct{ path, want string }{
		{"/usage?from=2026-03-01&to=2026-03-02&agentId=" + e.alpha,
			`{"data":{"from":"2026-03-01","groupBy":"day","rows":[` +
				`{"cacheReadTokens":0,"cacheWriteTokens":0,"costUsd":2.00024,"day":"2026-03-01","inputTokens":500100,"managed":false,"outputTokens":250010,"requests":3,"unpricedRequests":0},` +
				`{"cacheReadTokens":0,"cacheWriteTokens":0,"costUsd":0,"day":"2026-03-02","inputTokens":50,"managed":false,"outputTokens":5,"requests":1,"unpricedRequests":1}],` +
				`"to":"2026-03-02","total":{"cacheReadTokens":0,"cacheWriteTokens":0,"costUsd":2.00024,"inputTokens":500150,"managed":false,"outputTokens":250015,"requests":4,"unpricedRequests":1}}}`},
		{"/usage?groupBy=agent&from=2026-03-02&to=2026-03-02&agentId=" + e.beta,
			`{"data":{"from":"2026-03-02","groupBy":"agent","rows":[` +
				`{"agentId":"` + e.beta + `","agentName":"Beta","cacheReadTokens":0,"cacheWriteTokens":0,"costUsd":0.0072,"inputTokens":3000,"managed":false,"outputTokens":300,"requests":3,"unpricedRequests":0}],` +
				`"to":"2026-03-02","total":{"cacheReadTokens":0,"cacheWriteTokens":0,"costUsd":0.0072,"inputTokens":3000,"managed":false,"outputTokens":300,"requests":3,"unpricedRequests":0}}}`},
		{"/usage?groupBy=model&from=2026-03-02&to=2026-03-02&agentId=" + e.beta,
			`{"data":{"from":"2026-03-02","groupBy":"model","rows":[` +
				`{"cacheReadTokens":0,"cacheWriteTokens":0,"costUsd":0.0072,"inputTokens":3000,"managed":false,"modelId":"` + model + `","modelName":"priced","outputTokens":300,"providerName":"Usage provider","requests":3,"unpricedRequests":0}],` +
				`"to":"2026-03-02","total":{"cacheReadTokens":0,"cacheWriteTokens":0,"costUsd":0.0072,"inputTokens":3000,"managed":false,"outputTokens":300,"requests":3,"unpricedRequests":0}}}`},
	} {
		code, raw := e.member.raw("GET", toolsTeam+c.path, nil, nil)
		var got, want any
		if err := json.Unmarshal(raw, &got); err != nil || code != 200 {
			t.Fatalf("%s: %d %s", c.path, code, raw)
		}
		if err := json.Unmarshal([]byte(c.want), &want); err != nil {
			t.Fatal(err)
		}
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		if !bytes.Equal(g, w) {
			t.Fatalf("%s:\n got %s\nwant %s", c.path, g, w)
		}
	}
}
