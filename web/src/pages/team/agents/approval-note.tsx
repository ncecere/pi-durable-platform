/*
 * A tool call's approval in a conversation: while it waits, who it's for and
 * when it expires, with Approve and Deny (and an optional reason the agent is
 * given) for someone who may decide; once decided, how. The decision goes
 * through the platform, which wakes a hibernated agent; the conversation's
 * stream then shows the call run, or the refusal.
 */
import { Check, X } from "lucide-react";
import { useState } from "react";
import { type ApprovalDecision, approvalOutcome, type ChatApproval, decideApproval } from "../../../api/approvals";
import { errorMessage } from "../../../api/client";
import { Alert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { Input } from "@/components/ui/input/input";
import { Time } from "@/components/ui/time/time";
import { toast } from "@/components/ui/toast/toast";
import s from "./agents.module.css";

type Props = {
  approval: ChatApproval;
  /** Decide from here (the test chat); absent: the note only says what happened. */
  agentId?: string;
  /** False when the platform says this person may not decide it. */
  canDecide?: boolean;
  onDecided?: () => void;
};

export function ApprovalNote({ approval, agentId, canDecide = true, onDecided }: Props) {
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState<ApprovalDecision | null>(null);
  if (approval.status !== "pending") {
    return <p className={approval.status === "approved" ? s.approvalDone : s.approvalRefused}>{approvalOutcome(approval)}</p>;
  }
  const decide = async (decision: ApprovalDecision) => {
    if (!agentId) return;
    setBusy(decision);
    try {
      await decideApproval(agentId, approval.id, decision, reason);
      onDecided?.();
    } catch (err) {
      toast.error(decision === "approve" ? "Couldn't approve" : "Couldn't deny", errorMessage(err));
    } finally {
      setBusy(null);
    }
  };
  const forWhom = approval.requestedFor ? `Asked for ${approval.requestedFor.name}.` : "An unattended run: the team's owners and admins decide.";
  const actions =
    agentId && canDecide ? (
      <div className={s.approvalActions}>
        <Input
          size="sm"
          aria-label="Reason (optional, given to the agent when denied)"
          placeholder="Reason (optional)"
          maxLength={1000}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
        />
        <Button size="sm" disabled={busy !== null} onClick={() => void decide("approve")}>
          <Check aria-hidden /> {busy === "approve" ? "Approving…" : "Approve"}
        </Button>
        <Button size="sm" variant="secondary" disabled={busy !== null} onClick={() => void decide("deny")}>
          <X aria-hidden /> {busy === "deny" ? "Denying…" : "Deny"}
        </Button>
      </div>
    ) : undefined;
  return (
    <Alert tone="warning" title={`${approval.tool} waits for approval`} actions={actions}>
      {approval.summary ? <>{approval.summary} </> : null}
      {forWhom}
      {approval.expiresAt ? (
        <>
          {" "}
          Denied if nobody decides by <Time value={approval.expiresAt} />.
        </>
      ) : null}
      {agentId && !canDecide ? " You may not decide it." : null}
    </Alert>
  );
}
