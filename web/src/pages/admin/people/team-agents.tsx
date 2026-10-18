/*
 * Admin team › Agents: the team's agents, service-managed ones too, each
 * linking to its page, which platform staff open read-only (Overview and
 * Settings; platform admins also get Conversations, where they read one with
 * a reason). The Test chat stays the team's members'.
 */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Sparkles } from "lucide-react";
import { teamAgentsQuery } from "@/api/agents";
import { ListPage } from "@/components/templates/list-page";
import { terms } from "@/lib/terms";
import { type Agent } from "../../team/agents/labels";
import { agentColumns } from "../../team/agents/list";

export function TeamAgentsTab({ team, personal }: { team: string; personal: boolean }) {
  const agents = useQuery(teamAgentsQuery(team));
  const link = (a: Agent) => <Link to="/teams/$team/agents/$agentId" params={{ team, agentId: a.id }} />;
  return (
    <ListPage<Agent>
      id="admin-team-agents"
      caption={`${terms.agents} of this ${personal ? "workspace" : "team"}`}
      columns={agentColumns(link)}
      data={agents.data ?? []}
      getRowId={(a) => a.id}
      rowLabel={(a) => a.name}
      search={{ label: "Search agents", placeholder: "Name, description or key" }}
      loading={agents.isLoading}
      error={agents.error}
      onRetry={() => void agents.refetch()}
      rowActions={(a) => [{ label: "Open", render: link(a) }]}
      empty={{
        icon: <Sparkles />,
        title: personal ? "No agents in this workspace." : "This team has no agents.",
        description: "Agents its members deploy show here.",
      }}
    />
  );
}
