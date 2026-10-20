/*
 * Team › Usage (/teams/$team/usage): the team's model requests and tokens per
 * day, model, agent, or agent and day, with a cost estimate from the models' current prices.
 * The period (?range=), grouping (?groupBy=) and one agent (?agentId=, from
 * an agent's Overview) live in the URL.
 */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { agentQuery } from "@/api/agents";
import { type TeamUsageGroup, teamUsageQuery, type UsageRow } from "@/api/usage";
import { agentName, costEstimate, dayLabel, type GroupOption, modelName, useGroupByParam, useUsageRange } from "@/components/usage/labels";
import { dayColumn, modelColumn, secondDayColumn } from "@/components/usage/columns";
import { UsageReportPage } from "@/components/usage/usage-report";
import { Alert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import { TextLink } from "@/components/ui/text-link/text-link";
import { useSearchParams } from "@/lib/url-search";
import s from "../../shared.module.css";
import { useTeam } from "../common";

const AGENT_PARAM = "agentId";

const groups: GroupOption<TeamUsageGroup>[] = [
  { value: "day", label: "Day" },
  { value: "model", label: "Model" },
  { value: "agent", label: "Agent" },
  { value: "agent,day", label: "Agent by day" },
];

function agentColumn(team: string, linkable: boolean): DataTableColumn<UsageRow> {
  return {
    id: "agent",
    header: "Agent",
    accessor: agentName,
    rowHeader: true,
    hideable: false,
    sortable: true,
    cell: (r) =>
      r.agentId ? (
        <CellText
          className={s.compactCell}
          wrap="anywhere"
          primary={
            linkable ? <TextLink render={<Link to="/teams/$team/agents/$agentId" params={{ team, agentId: r.agentId }} />}>{agentName(r)}</TextLink> : agentName(r)
          }
          secondary={r.managed ? "Service-managed" : undefined}
        />
      ) : (
        <CellText
          className={s.compactCell}
          primary={<span className={s.muted}>{agentName(r)}</span>}
          secondary={r.managed ? "Created by the team's apps, one per user" : "No longer in the team"}
        />
      ),
  };
}

/** "Only <agent>": the report is narrowed to one agent, with a way back to all. */
function AgentFilter({ agentId, onClear, canRead }: { agentId: string; onClear: () => void; canRead: boolean }) {
  const agent = useQuery({ ...agentQuery(agentId), enabled: canRead });
  const name = agent.data?.name ?? "one agent";
  return (
    <Alert
      tone="info"
      title={`Only ${name}`}
      actions={
        <Button size="sm" variant="secondary" onClick={onClear}>
          Show all agents
        </Button>
      }
    >
      The report counts this agent&apos;s requests only.
    </Alert>
  );
}

/** /teams/$team/usage */
export function TeamUsagePage() {
  const { slug, team, role } = useTeam();
  const range = useUsageRange();
  const [groupBy, setGroupBy] = useGroupByParam(groups);
  const [params, setParams] = useSearchParams();
  const agentId = params.get(AGENT_PARAM) ?? "";
  const report = useQuery(teamUsageQuery(slug, { from: range.fromDay, to: range.toDay, groupBy, agentId: agentId || undefined }));
  const clearAgent = () =>
    setParams((p) => {
      const out = new URLSearchParams(p);
      out.delete(AGENT_PARAM);
      return out;
    });
  const byAgent = groupBy === "agent" || groupBy === "agent,day";
  const first = groupBy === "model" ? modelColumn : byAgent ? agentColumn(slug, Boolean(role)) : dayColumn;
  return (
    <UsageReportPage
      id="team-usage"
      title="Usage"
      description={`Model requests and tokens of ${team.name}'s agents. ${costEstimate}`}
      notices={agentId ? <AgentFilter agentId={agentId} onClear={clearAgent} canRead={Boolean(role)} /> : undefined}
      range={range}
      groupBy={groupBy}
      options={groups}
      onGroupBy={setGroupBy}
      report={report}
      firstColumn={first}
      secondColumn={groupBy === "agent,day" ? secondDayColumn : undefined}
      rowLabel={(r) =>
        groupBy === "model" ? modelName(r) : groupBy === "agent" ? agentName(r) : groupBy === "agent,day" ? `${agentName(r)}, ${dayLabel(r.day)}` : dayLabel(r.day)
      }
    />
  );
}
