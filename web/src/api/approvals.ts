/*
 * Tool approvals (docs/approvals.md): an agent's requests to run a tool a
 * person must approve first, the caller's inbox, and a decision. A decision
 * reaches the agent through the platform (waking it if it is hibernated); the
 * thread's own stream shows the call's approval change.
 */
import { queryOptions } from "@tanstack/react-query";
import { agentsKey } from "./agents";
import { api, type Schemas, unwrap } from "./client";

export type ToolApproval = Schemas["ToolApproval"];
export type ChatApproval = Schemas["ChatApproval"];
export type ApprovalStatus = Schemas["ToolApprovalStatus"];
export type ApprovalDecision = Schemas["ToolApprovalDecision"]["decision"];

export const approvalsKey = (agentId: string) => [...agentsKey, agentId, "approvals"];

/** An agent's approvals the caller may see: pending ones, or decided ones. */
export const agentApprovalsQuery = (agentId: string, status: "pending" | "decided") =>
  queryOptions({
    queryKey: [...approvalsKey(agentId), status],
    queryFn: async () =>
      unwrap(await api.GET("/v1/agents/{agentId}/approvals", { params: { path: { agentId }, query: { status, limit: 100 } } })),
    refetchInterval: 15_000,
  });

export async function decideApproval(agentId: string, approvalId: string, decision: ApprovalDecision, reason?: string): Promise<ToolApproval> {
  return unwrap(
    await api.POST("/v1/agents/{agentId}/approvals/{approvalId}", {
      params: { path: { agentId, approvalId } },
      body: { decision, ...(reason?.trim() ? { reason: reason.trim() } : {}) },
    }),
  );
}

export const approvalStatusLabels: Record<ApprovalStatus, string> = {
  pending: "Waiting for approval",
  approved: "Approved",
  denied: "Denied",
  timed_out: "Not approved in time",
  cancelled: "Cancelled",
};

/** "Approved by Ann", "Denied by Bo: not now", "Not approved in time". */
export function approvalOutcome(a: Pick<ChatApproval, "status" | "decidedBy" | "reason">): string {
  const label = approvalStatusLabels[a.status];
  if (a.status !== "approved" && a.status !== "denied") return label;
  const by = a.decidedBy && a.decidedBy.kind !== "system" ? ` by ${a.decidedBy.name}` : "";
  return `${label}${by}${a.reason ? `: ${a.reason}` : ""}`;
}
