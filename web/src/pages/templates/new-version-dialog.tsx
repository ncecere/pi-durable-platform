/* A new version of a template: its instructions and model, starting from the current version's. */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage, unwrap } from "../../api/client";
import { templateVersionsQuery } from "../../api/templates";
import { FormDialog } from "../../components/form-dialog";
import { ModelSelect } from "../../components/model-select";
import { Field } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { toast } from "@/components/ui/toast/toast";
import { type AgentTemplate, storeTemplate } from "./labels";
import { useTemplateModels } from "./template-models";

export function NewVersionDialog({ template, onClose }: { template: AgentTemplate; onClose: () => void }) {
  const qc = useQueryClient();
  const versions = useQuery(templateVersionsQuery(template.id));
  const current = versions.data?.find((v) => v.version === template.currentVersion);
  const models = useTemplateModels(template.teamSlug);
  const [instructions, setInstructions] = useState<string | undefined>(undefined);
  // undefined: the current version's model.
  const [picked, setPicked] = useState<string | undefined>(undefined);
  const [notes, setNotes] = useState("");
  const [busy, setBusy] = useState(false);
  const value = instructions ?? current?.instructions ?? "";
  const currentModel = current?.modelId ?? "";
  const modelId = picked ?? currentModel;
  const submit = async () => {
    if (!value.trim()) return;
    setBusy(true);
    try {
      const t = unwrap(
        await api.POST("/v1/agent-templates/{templateId}/versions", {
          params: { path: { templateId: template.id } },
          // Left out when unchanged, so a model that's no longer offered stays as it is.
          body: { instructions: value, notes, modelId: modelId === currentModel ? undefined : modelId },
        }),
      );
      storeTemplate(qc, t);
      void qc.invalidateQueries({ queryKey: templateVersionsQuery(template.id).queryKey });
      toast.success(`v${t.currentVersion} is the current version`, "New agents get it; existing agents keep theirs until upgraded.");
      onClose();
    } catch (err) {
      toast.error("Couldn't add the version", errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <FormDialog
      title={`New version of ${template.name}`}
      size="lg"
      onClose={onClose}
      onSubmit={() => void submit()}
      submitLabel={`Save v${template.currentVersion + 1}`}
      busy={busy}
      submitDisabled={!value.trim()}
    >
      <Field label="Instructions">
        <Textarea value={value} rows={10} maxLength={20000} onChange={(e) => setInstructions(e.target.value)} />
      </Field>
      <ModelSelect
        description="Agents move to it when they're upgraded to this version, unless they chose a model of their own."
        models={models.models}
        loading={models.loading || versions.isLoading}
        value={modelId}
        onChange={setPicked}
        defaultLabel="Platform default"
        current={current?.modelId ? { id: current.modelId, name: current.modelName ?? "Unknown model", providerName: current.modelProviderName ?? "" } : null}
      />
      <Field label="What changed" labelHint="Optional">
        <Input value={notes} maxLength={2000} onValueChange={setNotes} />
      </Field>
    </FormDialog>
  );
}
