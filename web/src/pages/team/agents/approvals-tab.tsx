/*
 * An agent's tool approvals (docs/approvals.md): calls waiting for a person,
 * with Approve and Deny for whoever may decide each one, and recent decisions.
 * Everyone who may use the agent sees the approvals of the conversations they
 * can read (others' private ones are left out); the arguments are a redacted
 * view. A decision reaches the agent through the platform, waking it if it is
 * hibernated.
 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ShieldCheck, X } from "lucide-react";
import { useState } from "react";
import { agentApprovalsQuery, type ApprovalDecision, approvalOutcome, approvalsKey, decideApproval, type ToolApproval } from "../../../api/approvals";
import { errorMessage } from "../../../api/client";
import { RelativeTime } from "../../../components/templates/list-page";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { CellText, type DataTableColumn, DataTable } from "@/components/ui/data-table/data-table";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import { toast } from "@/components/ui/toast/toast";
import shared from "../../shared.module.css";
import type { Agent } from "./labels";

/** At most this much of the arguments in a row (the whole redacted view is in its title). */
const ARGS_SHOWN = 120;

function argumentsText(a: ToolApproval): string {
  const text = JSON.stringify(a.arguments);
  return text.length > ARGS_SHOWN ? `${text.slice(0, ARGS_SHOWN)}…` : text;
}

const columns = (decided: boolean): DataTableColumn<ToolApproval>[] => [
  {
    id: "tool",
    header: "Tool call",
    rowHeader: true,
    accessor: "tool",
    cell: (a) => <CellText primary={<code>{a.tool}</code>} secondary={a.summary ?? argumentsText(a)} />,
  },
  {
    id: "thread",
    header: "Conversation",
    accessor: (a) => a.threadTitle ?? a.threadId,
    cell: (a) => <span className={shared.primary}>{a.threadTitle ?? a.threadId}</span>,
  },
  {
    id: "for",
    header: "Asked for",
    accessor: (a) => a.requestedFor?.name ?? "",
    cell: (a) => (a.requestedFor ? a.requestedFor.name : <span className={shared.muted}>Unattended</span>),
  },
  decided
    ? { id: "outcome", header: "Outcome", accessor: "status", cell: (a) => approvalOutcome({ ...a, reason: a.reason ?? undefined }) }
    : { id: "expires", header: "Denied if undecided", accessor: "expiresAt", cell: (a) => <RelativeTime value={a.expiresAt} /> },
  {
    id: "asked",
    header: decided ? "Decided" : "Asked",
    accessor: (a) => (decided ? (a.decidedAt ?? a.requestedAt) : a.requestedAt),
    cell: (a) => <RelativeTime value={decided ? (a.decidedAt ?? a.requestedAt) : a.requestedAt} />,
  },
];

export function ApprovalsTab({ agent }: { agent: Agent }) {
  const queryClient = useQueryClient();
  const pending = useQuery(agentApprovalsQuery(agent.id, "pending"));
  const decided = useQuery(agentApprovalsQuery(agent.id, "decided"));
  const [busy, setBusy] = useState<string | null>(null);

  const decide = async (a: ToolApproval, decision: ApprovalDecision) => {
    setBusy(a.id);
    try {
      await decideApproval(agent.id, a.id, decision);
      toast.success(decision === "approve" ? `Approved ${a.tool}` : `Denied ${a.tool}`);
    } catch (err) {
      toast.error(decision === "approve" ? "Couldn't approve" : "Couldn't deny", errorMessage(err));
    } finally {
      setBusy(null);
      void queryClient.invalidateQueries({ queryKey: approvalsKey(agent.id) });
    }
  };

  return (
    <>
      <Card
        title="Waiting for approval"
        description={`Tool calls of ${agent.name} that wait for a person. Each runs once someone allowed approves it; a denied one is refused and the agent is told why. Nobody deciding in time denies it.`}
        flush
      >
        <DataTable<ToolApproval>
          caption={`Tool calls of ${agent.name} waiting for approval`}
          stack
          columns={columns(false)}
          data={pending.data ?? []}
          getRowId={(a) => a.id}
          rowLabel={(a) => a.tool}
          defaultSort={{ columnId: "asked", direction: "descending" }}
          loading={pending.isLoading}
          error={pending.error}
          onRetry={() => void pending.refetch()}
          empty={<EmptyState size="compact" icon={<ShieldCheck />} title="Nothing waits for approval." />}
          rowActions={(a) =>
            a.canDecide ? (
              <>
                <Button size="sm" disabled={busy === a.id} onClick={() => void decide(a, "approve")} aria-label={`Approve ${a.tool}`}>
                  <Check aria-hidden /> Approve
                </Button>
                <Button size="sm" variant="secondary" disabled={busy === a.id} onClick={() => void decide(a, "deny")} aria-label={`Deny ${a.tool}`}>
                  <X aria-hidden /> Deny
                </Button>
              </>
            ) : (
              <span className={shared.muted}>Not yours to decide</span>
            )
          }
        />
      </Card>
      <Card title="Recent decisions" flush>
        <DataTable<ToolApproval>
          caption={`Decided tool calls of ${agent.name}`}
          stack
          columns={columns(true)}
          data={decided.data ?? []}
          getRowId={(a) => a.id}
          rowLabel={(a) => a.tool}
          defaultSort={{ columnId: "asked", direction: "descending" }}
          loading={decided.isLoading}
          error={decided.error}
          onRetry={() => void decided.refetch()}
          empty={<EmptyState size="compact" icon={<ShieldCheck />} title="No decisions yet." />}
        />
      </Card>
    </>
  );
}
