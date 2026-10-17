/*
 * A team's agents (/teams/$team/agents): deployed agents, and for owners and
 * admins also the service-managed ones its apps created per user. Editors
 * deploy agents from templates.
 */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Plus, Sparkles } from "lucide-react";
import { useState } from "react";
import { teamAgentsQuery } from "../../../api/agents";
import {
  ListPage,
  RelativeTime,
  timeColumn,
} from "../../../components/templates/list-page";
import { terms } from "../../../lib/terms";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import {
  CellText,
  type DataTableColumn,
} from "@/components/ui/data-table/data-table";
import { TextLink } from "@/components/ui/text-link/text-link";
import s from "../../shared.module.css";
import { useTeam } from "../common";
import { ArchivedNotice } from "../layout";
import { DeployAgentDialog } from "./deploy-dialog";
import { type Agent, agentStatus, whatIsAnAgent } from "./labels";

export function agentColumns(
  link: (a: Agent) => React.ReactElement,
  withTeam = false,
): DataTableColumn<Agent>[] {
  return [
    {
      id: "name",
      header: "Agent",
      accessor: (a) => a.name,
      rowHeader: true,
      hideable: false,
      sortable: true,
      cell: (a) => (
        <CellText
          className={s.compactCell}
          wrap="anywhere"
          primary={<TextLink render={link(a)}>{a.name}</TextLink>}
          secondary={
            a.externalKey ? (
              <span className={s.mono}>{a.externalKey}</span>
            ) : a.description ? (
              <span className={s.truncate}>{a.description}</span>
            ) : undefined
          }
        />
      ),
    },
    ...(withTeam
      ? [
          {
            id: "team",
            header: "Team",
            accessor: (a: Agent) => a.teamName,
            sortable: true,
          } as DataTableColumn<Agent>,
        ]
      : []),
    {
      id: "template",
      header: "Template",
      accessor: (a) => a.templateName,
      sortable: true,
    },
    {
      id: "managed",
      header: "Managed by",
      accessor: (a) => (a.externalKey ? "Service" : "Team"),
      sortable: true,
      cell: (a) => (
        <Badge tone={a.externalKey ? "info" : "neutral"}>
          {a.externalKey ? "Service" : "Team"}
        </Badge>
      ),
    },
    {
      id: "status",
      header: "Status",
      accessor: (a) => agentStatus(a).label,
      sortable: true,
      cell: (a) => {
        const st = agentStatus(a);
        return (
          <Badge dot tone={st.tone}>
            {st.label}
          </Badge>
        );
      },
    },
    {
      ...timeColumn<Agent>(
        "lastUsedAt",
        "Last used",
        (a) => a.lastUsedAt ?? undefined,
      ),
      cell: (a) =>
        a.lastUsedAt ? (
          <span className={s.nowrap}>
            <RelativeTime value={a.lastUsedAt} />
          </span>
        ) : (
          <span className={s.muted}>Never</span>
        ),
    },
    {
      ...timeColumn<Agent>("createdAt", "Created", (a) => a.createdAt),
      defaultHidden: true,
    },
  ];
}

export function TeamAgentsPage() {
  const { slug, team, canEdit } = useTeam();
  const agents = useQuery({
    ...teamAgentsQuery(slug),
    refetchInterval: 15_000,
  });
  const [deploying, setDeploying] = useState(false);
  const deploy = canEdit && (
    <Button onClick={() => setDeploying(true)}>
      <Plus aria-hidden /> Deploy agent
    </Button>
  );
  const link = (a: Agent) => (
    <Link
      to="/teams/$team/agents/$agentId"
      params={{ team: slug, agentId: a.id }}
    />
  );
  return (
    <>
      <ListPage<Agent>
        id="team-agents"
        title={terms.agents}
        description={`Durable Pi agents ${team.name} deploys, tests and reaches from its apps with service account keys.`}
        primaryAction={deploy}
        notices={
          <ArchivedNotice>
            Its agents can be viewed but not deployed or changed while the team
            is archived.
          </ArchivedNotice>
        }
        caption={terms.agents}
        columns={agentColumns(link)}
        data={agents.data ?? []}
        getRowId={(a) => a.id}
        rowLabel={(a) => a.name}
        search={{
          label: "Search agents",
          placeholder: "Name, description or key",
        }}
        loading={agents.isLoading}
        error={agents.error}
        onRetry={() => void agents.refetch()}
        rowActions={(a) => [
          { label: "Open", render: link(a) },
          {
            label: "Test",
            hidden: a.externalKey != null,
            render: (
              <Link
                to="/teams/$team/agents/$agentId"
                params={{ team: slug, agentId: a.id }}
                search={{ tab: "test" }}
              />
            ),
          },
        ]}
        empty={{
          icon: <Sparkles />,
          title: "No agents yet.",
          description: whatIsAnAgent,
          action: deploy || undefined,
        }}
      />
      {deploying && (
        <DeployAgentDialog
          teams={[{ slug, name: team.name }]}
          onClose={() => setDeploying(false)}
        />
      )}
    </>
  );
}
