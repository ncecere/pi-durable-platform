/*
 * Admin › Usage (/admin/usage): every team's model requests and tokens per
 * day, team or model, with a cost estimate from the models' current prices.
 * Platform admins and auditors. Period (?range=), grouping (?groupBy=) and
 * the connected app it is limited to (?app=) live in the URL.
 */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type AdminUsageGroup, adminUsageQuery, type UsageRow } from "@/api/usage";
import { dayColumn, modelColumn } from "@/components/usage/columns";
import { costEstimate, dayLabel, type GroupOption, modelName, teamName, useGroupByParam, useUsageRange } from "@/components/usage/labels";
import { UsageReportPage } from "@/components/usage/usage-report";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import { TextLink } from "@/components/ui/text-link/text-link";
import s from "../../shared.module.css";
import { AppFilter, useAppParam } from "./app-filter";

const groups: GroupOption<AdminUsageGroup>[] = [
  { value: "day", label: "Day" },
  { value: "team", label: "Team" },
  { value: "model", label: "Model" },
];

/** The team, linked to its admin page. */
const teamColumn: DataTableColumn<UsageRow> = {
  id: "team",
  header: "Team",
  accessor: teamName,
  rowHeader: true,
  hideable: false,
  sortable: true,
  cell: (r) => (
    <CellText
      className={s.compactCell}
      wrap="anywhere"
      primary={
        r.teamSlug ? <TextLink render={<Link to="/admin/teams/$team" params={{ team: r.teamSlug }} />}>{teamName(r)}</TextLink> : teamName(r)
      }
      secondary={r.teamSlug ? <span className={s.mono}>{r.teamSlug}</span> : undefined}
    />
  ),
};

/** /admin/usage */
export function AdminUsagePage() {
  const range = useUsageRange();
  const [groupBy, setGroupBy] = useGroupByParam(groups);
  const [app, setApp] = useAppParam();
  const report = useQuery(adminUsageQuery({ from: range.fromDay, to: range.toDay, groupBy, clientId: app || undefined }));
  const first = groupBy === "team" ? teamColumn : groupBy === "model" ? modelColumn : dayColumn;
  return (
    <UsageReportPage
      id="admin-usage"
      title="Usage"
      description={`Model requests and tokens of every team's agents. ${costEstimate}`}
      range={range}
      groupBy={groupBy}
      options={groups}
      onGroupBy={setGroupBy}
      report={report}
      notices={<AppFilter value={app} onChange={setApp} />}
      firstColumn={first}
      rowLabel={(r) => (groupBy === "team" ? teamName(r) : groupBy === "model" ? modelName(r) : dayLabel(r.day))}
    />
  );
}
