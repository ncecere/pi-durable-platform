package agents

import (
	"strings"
	"testing"
)

func TestKindApprovalsAddATriageVersionsWriteTools(t *testing.T) {
	_, raw, err := checkKind(KindTriage, &KindSettings{WriteTools: &[]string{"mcp__gh__comment", "mcp__crm__*"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(kindApprovals(KindTriage, raw, []string{"mcp__crm__*", "get_time"}), ","); got != "mcp__crm__*,get_time,mcp__gh__comment" {
		t.Fatalf("triage approvals = %s", got)
	}
	off := false
	_, raw, _ = checkKind(KindTriage, &KindSettings{WriteTools: &[]string{"mcp__gh__comment"}, ApproveWrites: &off})
	if got := kindApprovals(KindTriage, raw, []string{}); len(got) != 0 {
		t.Fatalf("approveWrites off = %v", got)
	}
	if got := kindApprovals(KindResearcher, []byte(`{"writeTools":["x"]}`), nil); got != nil {
		t.Fatalf("a researcher's approvals = %v", got)
	}
	many := make([]string, 100)
	for i := range many {
		many[i] = "t" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + strings.Repeat("y", i/26)
	}
	_, raw, _ = checkKind(KindTriage, &KindSettings{WriteTools: &[]string{"w"}})
	if got := kindApprovals(KindTriage, raw, many); len(got) != 100 {
		t.Fatalf("capped at %d", len(got))
	}
}

func TestKindEnvIsCompactAndOnlyForKinds(t *testing.T) {
	env := map[string]string{}
	kindEnv(KindAssistant, []byte("{}"), env)
	if len(env) != 0 {
		t.Fatalf("assistant env = %v", env)
	}
	kindEnv(KindResearcher, []byte(`{"maxPlanSteps": 3, "backgroundSubagents": true}`), env)
	if env["AGENT_KIND"] != "researcher" || env["AGENT_KIND_SETTINGS"] != `{"maxPlanSteps":3,"backgroundSubagents":true}` {
		t.Fatalf("researcher env = %v", env)
	}
}
