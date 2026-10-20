/*
 * Runs: one row per fire of the agent's triggers (a schedule's time, a webhook's delivery, Run now), newest first:
 * the trigger, how it was fired, its status (and why it was skipped, stopped or failed), when it started, how long it
 * took, its cost and its callback. A run's conversation opens in Test. Refreshes while runs are in progress.
 */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { History } from "lucide-react";
import { useState } from "react";
import { firedByLabel, runReason, runsQuery, runStatus, type TriggerRun, type TriggerRunStatus } from "../../../api/triggers";
import { RelativeTime } from "../../../components/templates/list-page";
import { usd } from "../../../lib/format";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { CellText, type DataTableColumn, DataTable } from "@/components/ui/data-table/data-table";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import { Select } from "@/components/ui/select/select";
import shared from "../../shared.module.css";
import type { Agent } from "./labels";

/** "1 min 05 s", "12 s", or "—" for a run that never started. */
export function runDuration(run: Pick<TriggerRun, "startedAt" | "finishedAt">, now = Date.now()): string {
  if (!run.startedAt) return "—";
  const end = run.finishedAt ? Date.parse(run.finishedAt) : now;
  const s = Math.max(0, Math.round((end - Date.parse(run.startedAt)) / 1000));
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  return m < 60 ? `${m} min ${String(s % 60).padStart(2, "0")} s` : `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, "0")} min`;
}

const statusFilter: { value: string; label: string }[] = [
  { value: "all", label: "Any status" },
  ...(Object.keys(runStatus) as TriggerRunStatus[]).map((v) => ({ value: v, label: runStatus[v].label })),
];

/** The Test tab with a conversation open (a run's, an agent-made schedule's) (?thread= passes through the route's search). */
export const runSearch = (thread: string) => {
  const search = { tab: "test" as const, thread };
  return search;
};

const callbackLabel = { pending: "Sending", delivered: "Delivered", failed: "Failed" } as const;

export function RunsTab({ agent }: { agent: Agent }) {
  const [status, setStatus] = useState("all");
  const runs = useQuery({
    ...runsQuery(agent.id, status !== "all" ? { status: status as TriggerRunStatus } : {}),
    refetchInterval: (q) => (q.state.data?.some((r) => r.status === "pending" || r.status === "running") ? 5_000 : 30_000),
  });

  const columns: DataTableColumn<TriggerRun>[] = [
    {
      id: "trigger",
      header: "Trigger",
      rowHeader: true,
      accessor: "triggerName",
      cell: (r) => <CellText primary={r.triggerName} secondary={firedByLabel[r.firedBy]} />,
    },
    {
      id: "status",
      header: "Status",
      accessor: "status",
      cell: (r) => {
        const st = runStatus[r.status];
        const why = runReason(r.reason);
        return (
          <span>
            <Badge size="sm" tone={st.tone} dot={r.status === "running"}>
              {st.label}
            </Badge>
            {why ? <span className={shared.muted}> {why}</span> : null}
          </span>
        );
      },
    },
    {
      id: "started",
      header: "Started",
      accessor: (r) => r.startedAt ?? r.createdAt,
      cell: (r) => <RelativeTime value={r.startedAt ?? r.createdAt} />,
    },
    { id: "duration", header: "Duration", accessor: (r) => runDuration(r), cell: (r) => runDuration(r) },
    { id: "cost", header: "Cost", accessor: "costUsd", cell: (r) => (r.startedAt ? usd(r.costUsd) : <span className={shared.muted}>—</span>) },
    {
      id: "callback",
      header: "Callback",
      accessor: (r) => r.callbackStatus ?? "",
      cell: (r) => (r.callbackStatus ? callbackLabel[r.callbackStatus] : <span className={shared.muted}>—</span>),
    },
  ];

  return (
    <Card
      title="Runs"
      description={`What ${agent.name}'s triggers started, newest first. Each run is a conversation with the trigger as its author; its cost is the agent's model usage while it ran.`}
      actions={<Select label="Status" hideLabel items={statusFilter} value={status} onValueChange={(v) => setStatus(v ?? "all")} />}
      flush
    >
      <DataTable<TriggerRun>
        caption={`Runs of ${agent.name}`}
        stack
        columns={columns}
        data={runs.data ?? []}
        getRowId={(r) => r.id}
        rowLabel={(r) => `${r.triggerName}, ${runStatus[r.status].label}`}
        loading={runs.isLoading}
        error={runs.error}
        onRetry={() => void runs.refetch()}
        empty={
          <EmptyState
            size="compact"
            icon={<History />}
            title="No runs yet."
            description="A schedule's time, a webhook's delivery or Run now on a trigger starts one."
          />
        }
        rowActions={(r) =>
          r.threadId && r.startedAt ? (
            <Button
              size="sm"
              variant="secondary"
              aria-label={`Open the conversation of ${r.triggerName}'s run`}
              render={<Link to="/teams/$team/agents/$agentId" params={{ team: agent.teamSlug, agentId: agent.id }} search={runSearch(r.threadId)} />}
            >
              Conversation
            </Button>
          ) : null
        }
      />
    </Card>
  );
}
