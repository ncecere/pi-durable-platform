/* A platform admin approves a request to publish (into the catalog) or rejects it with a note the team sees. */
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage, unwrap } from "../../api/client";
import { Button } from "@/components/ui/button/button";
import { Dialog, DialogClose } from "@/components/ui/dialog/dialog";
import { Field } from "@/components/ui/field/field";
import { Textarea } from "@/components/ui/input/input";
import { toast } from "@/components/ui/toast/toast";
import { type AgentTemplate, storeTemplate } from "./labels";
import { catalogRefusal } from "./template-models";

export function ReviewDialog({
  template,
  onClose,
}: {
  template: AgentTemplate;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState<"approve" | "reject" | null>(null);
  const decide = async (decision: "approve" | "reject") => {
    setBusy(decision);
    try {
      const t = unwrap(
        await api.POST("/v1/agent-templates/{templateId}/review", {
          params: { path: { templateId: template.id } },
          body: { decision, note: note.trim() || undefined },
        }),
      );
      storeTemplate(qc, t);
      toast.success(
        decision === "approve"
          ? `${t.name} is in the catalog`
          : "Request rejected",
        decision === "approve"
          ? "Every team can deploy it now."
          : "The team sees your note.",
      );
      onClose();
    } catch (err) {
      toast.error(
        decision === "approve" ? "Couldn't approve the template" : "Couldn't review the template",
        catalogRefusal(err, "approve") ?? errorMessage(err),
      );
    } finally {
      setBusy(null);
    }
  };
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title={`Review ${template.name}`}
      description={`${template.teamName ?? "A team"} asked to publish v${template.currentVersion} of this template to the catalog. Read its instructions on the Overview tab first.`}
      footer={
        <>
          <DialogClose>Cancel</DialogClose>
          <Button
            variant="danger"
            loading={busy === "reject"}
            disabled={busy !== null || !note.trim()}
            onClick={() => void decide("reject")}
          >
            Reject
          </Button>
          <Button
            loading={busy === "approve"}
            disabled={busy !== null}
            onClick={() => void decide("approve")}
          >
            Approve
          </Button>
        </>
      }
    >
      <Field
        label="Note to the team"
        description="Required to reject: what to change before asking again."
      >
        <Textarea
          value={note}
          maxLength={2000}
          onChange={(e) => setNote(e.target.value)}
        />
      </Field>
    </Dialog>
  );
}
