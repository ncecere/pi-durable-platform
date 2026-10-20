/*
 * Triggers: the agent's schedules and webhooks, which start runs without a person, and the limits they run under.
 * Everyone who may use the agent sees them; its team's owners and admins add, pause, run now, replace secrets and
 * delete them, and pause all of the agent's triggers at once.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CalendarClock, Pause, Play, Plus, Trash2, Webhook } from "lucide-react";
import { useState } from "react";
import { errorMessage } from "../../../api/client";
import {
  deleteTrigger,
  fireTrigger,
  rotateTriggerSecret,
  runsKey,
  setTriggerSettings,
  type Trigger,
  type TriggerSaved,
  triggerSettingsQuery,
  triggersKey,
  triggersQuery,
  updateTrigger,
} from "../../../api/triggers";
import { ConfirmMutationDialog } from "../../../components/confirm-dialog";
import { RelativeTime } from "../../../components/templates/list-page";
import { usd } from "../../../lib/format";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { CellText, type DataTableColumn, DataTable } from "@/components/ui/data-table/data-table";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import { Switch } from "@/components/ui/switch/switch";
import { toast } from "@/components/ui/toast/toast";
import shared from "../../shared.module.css";
import type { Agent } from "./labels";
import { AddTriggerDialog, TriggerSecrets } from "./trigger-dialog";

/** "Every weekday at 8:00, Europe/Paris" is left to cron; the expression and zone, as written. */
const what = (t: Trigger) => (t.schedule ? `${t.schedule.cron} (${t.schedule.timezone})` : (t.webhookUrl ?? ""));

function TriggerState({ t }: { t: Trigger }) {
  if (t.paused)
    return (
      <Badge size="sm" tone="warning">
        {t.pausedReason === "failures" ? "Paused after failures" : "Paused"}
      </Badge>
    );
  if (t.backoffUntil && Date.parse(t.backoffUntil) > Date.now())
    return (
      <Badge size="sm" tone="warning">
        Backing off
      </Badge>
    );
  return (
    <Badge size="sm" tone="success">
      Active
    </Badge>
  );
}

export function TriggersTab({ agent, canManage }: { agent: Agent; canManage: boolean }) {
  const qc = useQueryClient();
  const triggers = useQuery({ ...triggersQuery(agent.id), refetchInterval: 30_000 });
  const settings = useQuery(triggerSettingsQuery(agent.id));
  const [adding, setAdding] = useState(false);
  const [deleting, setDeleting] = useState<Trigger | null>(null);
  const [secrets, setSecrets] = useState<TriggerSaved | null>(null);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: triggersKey(agent.id) });
    void qc.invalidateQueries({ queryKey: runsKey(agent.id) });
  };
  const fail = (what: string) => (err: unknown) => toast.error(what, errorMessage(err));
  const pause = useMutation({
    mutationFn: (t: Trigger) => updateTrigger(t, { paused: !t.paused }),
    onSuccess: (s) => {
      refresh();
      toast.success(s.trigger.paused ? `${s.trigger.name} is paused` : `${s.trigger.name} is running again`);
    },
    onError: fail("Couldn't change the trigger"),
  });
  const fire = useMutation({
    mutationFn: fireTrigger,
    onSuccess: (run) => {
      refresh();
      if (run.status === "skipped") toast.error("The run was skipped", run.reason ?? undefined);
      else toast.success("Run started", "It's listed under Runs.");
    },
    onError: fail("Couldn't run the trigger"),
  });
  const rotate = useMutation({
    mutationFn: ({ t, which }: { t: Trigger; which: "webhook" | "callback" }) => rotateTriggerSecret(t, which),
    onSuccess: (s) => {
      refresh();
      setSecrets(s);
    },
    onError: fail("Couldn't replace the secret"),
  });
  const remove = useMutation({
    mutationFn: deleteTrigger,
    onSuccess: () => {
      refresh();
      setDeleting(null);
    },
  });
  const pauseAll = useMutation({
    mutationFn: (paused: boolean) => setTriggerSettings(agent.id, settings.data!, paused),
    onSuccess: (st) => {
      qc.setQueryData(triggerSettingsQuery(agent.id).queryKey, st);
      toast.success(st.paused ? "All of this agent's triggers are paused" : "This agent's triggers run again");
    },
    onError: fail("Couldn't change the agent's triggers"),
  });

  const columns: DataTableColumn<Trigger>[] = [
    {
      id: "name",
      header: "Trigger",
      rowHeader: true,
      accessor: "name",
      cell: (t) => (
        <CellText
          primary={
            <span className={shared.primary}>
              {t.kind === "schedule" ? <CalendarClock aria-hidden size={14} /> : <Webhook aria-hidden size={14} />} {t.name}
            </span>
          }
          secondary={what(t)}
        />
      ),
    },
    { id: "state", header: "State", accessor: (t) => (t.paused ? "paused" : "active"), cell: (t) => <TriggerState t={t} /> },
    {
      id: "next",
      header: "Next / last",
      accessor: (t) => t.schedule?.nextFireAt ?? t.lastFiredAt ?? "",
      cell: (t) =>
        t.schedule?.nextFireAt && !t.paused ? (
          <span>
            Next <RelativeTime value={t.schedule.nextFireAt} />
          </span>
        ) : t.lastFiredAt ? (
          <span>
            Last <RelativeTime value={t.lastFiredAt} />
          </span>
        ) : (
          <span className={shared.muted}>Not yet</span>
        ),
    },
    {
      id: "budget",
      header: "Daily budget",
      accessor: (t) => t.dailyBudgetUsd ?? -1,
      cell: (t) => (t.dailyBudgetUsd != null ? usd(t.dailyBudgetUsd) : <span className={shared.muted}>None</span>),
    },
  ];

  const st = settings.data;
  return (
    <>
      <Card
        title="Limits"
        description={
          st
            ? `At most ${st.effectiveConcurrentRuns} run${st.effectiveConcurrentRuns === 1 ? "" : "s"} at once, each stopped after ${Math.round(st.effectiveRunSeconds / 60)} minutes${st.dailyBudgetUsd != null ? `, and ${usd(st.dailyBudgetUsd)} a day for all its runs` : ""}. A trigger pauses itself after repeated failures.`
            : undefined
        }
      >
        <Switch
          label="Pause all of this agent's triggers"
          description="Schedules move on without running; webhook deliveries are recorded as skipped."
          checked={st?.paused ?? false}
          disabled={!canManage || !st || pauseAll.isPending}
          onCheckedChange={(v) => pauseAll.mutate(v)}
        />
      </Card>
      <Card
        title="Triggers"
        description="Schedules and webhooks that start a run without a person. A run acts as its trigger, with the team's models and tools, never as someone."
        actions={
          canManage ? (
            <Button size="sm" onClick={() => setAdding(true)}>
              <Plus aria-hidden /> Add trigger
            </Button>
          ) : undefined
        }
        flush
      >
        <DataTable<Trigger>
          caption={`Triggers of ${agent.name}`}
          stack
          columns={columns}
          data={triggers.data ?? []}
          getRowId={(t) => t.id}
          rowLabel={(t) => t.name}
          loading={triggers.isLoading}
          error={triggers.error}
          onRetry={() => void triggers.refetch()}
          empty={<EmptyState size="compact" icon={<CalendarClock />} title="No triggers yet." description="Add a schedule or a webhook to run this agent on its own." />}
          rowActions={
            canManage
              ? (t) => (
                  <>
                    <Button size="sm" variant="secondary" onClick={() => fire.mutate(t)} aria-label={`Run ${t.name} now`}>
                      <Play aria-hidden /> Run now
                    </Button>
                    <Button size="sm" variant="secondary" onClick={() => pause.mutate(t)} aria-label={`${t.paused ? "Resume" : "Pause"} ${t.name}`}>
                      {t.paused ? <Play aria-hidden /> : <Pause aria-hidden />} {t.paused ? "Resume" : "Pause"}
                    </Button>
                    {t.kind === "webhook" ? (
                      <Button size="sm" variant="secondary" onClick={() => rotate.mutate({ t, which: "webhook" })} aria-label={`Replace ${t.name}'s secret`}>
                        New secret
                      </Button>
                    ) : null}
                    <Button size="sm" variant="secondary" onClick={() => setDeleting(t)} aria-label={`Delete ${t.name}`}>
                      <Trash2 aria-hidden />
                    </Button>
                  </>
                )
              : undefined
          }
        />
      </Card>
      {adding ? <AddTriggerDialog agent={agent} onClose={() => setAdding(false)} /> : null}
      {secrets ? <TriggerSecrets saved={secrets} onClose={() => setSecrets(null)} /> : null}
      <ConfirmMutationDialog
        target={deleting}
        onClose={() => setDeleting(null)}
        mutation={remove}
        onConfirm={(t) => remove.mutate(t)}
        title={deleting ? `Delete ${deleting.name}?` : ""}
        description="It stops firing and its webhook address stops working. Its runs stay listed."
        confirmLabel="Delete trigger"
      />
    </>
  );
}
