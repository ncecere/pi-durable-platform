/*
 * Agents (/agents): every agent of the person's teams in one list, with the
 * service-managed ones of teams they own or administer. Each opens on its
 * team's agent page; editors deploy new agents from templates.
 */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Plus, Sparkles } from "lucide-react";
import { useState } from "react";
import { agentsQuery } from "../../api/agents";
import { ListPage } from "../../components/templates/list-page";
import { terms } from "../../lib/terms";
import { useCurrentUser } from "../../session";
import { Button } from "@/components/ui/button/button";
import { DeployAgentDialog } from "../team/agents/deploy-dialog";
import { type Agent, whatIsAnAgent } from "../team/agents/labels";
import { agentColumns } from "../team/agents/list";

export function AgentsPage() {
  const me = useCurrentUser();
  const agents = useQuery({ ...agentsQuery(), refetchInterval: 15_000 });
  const deployable = me.teams.filter(
    (t) => t.status === "active" && t.role !== "member",
  );
  const [deploying, setDeploying] = useState(false);
  const deploy = deployable.length > 0 && (
    <Button onClick={() => setDeploying(true)}>
      <Plus aria-hidden /> Deploy agent
    </Button>
  );
  const link = (a: Agent) => (
    <Link
      to="/teams/$team/agents/$agentId"
      params={{ team: a.teamSlug, agentId: a.id }}
    />
  );
  return (
    <>
      <ListPage<Agent>
        id="agents"
        title={terms.agents}
        description="Durable Pi agents across your teams. Teams deploy them from templates, test them here, and reach them from their apps with service account keys."
        primaryAction={deploy}
        caption={terms.agents}
        columns={agentColumns(link, true)}
        data={agents.data ?? []}
        getRowId={(a) => a.id}
        rowLabel={(a) => a.name}
        search={{
          label: "Search agents",
          placeholder: "Name, team, description or key",
        }}
        loading={agents.isLoading}
        error={agents.error}
        onRetry={() => void agents.refetch()}
        rowActions={(a) => [{ label: "Open", render: link(a) }]}
        empty={{
          icon: <Sparkles />,
          title:
            me.teams.length === 0
              ? "Join a team to deploy agents."
              : "No agents yet.",
          description: whatIsAnAgent,
          action: deploy || undefined,
        }}
      />
      {deploying && (
        <DeployAgentDialog
          teams={deployable.map((t) => ({ slug: t.slug, name: t.name }))}
          onClose={() => setDeploying(false)}
        />
      )}
    </>
  );
}
