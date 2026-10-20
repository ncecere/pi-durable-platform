/* A new version of a template: its instructions, model, MCP servers, tool approvals and kind, starting from the current version's. */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage, unwrap } from "../../api/client";
import { templateVersionsQuery } from "../../api/templates";
import { FormDialog } from "../../components/form-dialog";
import { attachOf, McpPicker, mcpProblem, sameMcp } from "../../components/mcp-picker";
import { AgentSchedulesField } from "../../components/templates/agent-schedules-field";
import { ModelSelect } from "../../components/model-select";
import type { McpAttach } from "../../api/mcp";
import { Field } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { toast } from "@/components/ui/toast/toast";
import { ApprovalFields, type ApprovalPolicy, approvalsProblem, defaultApprovals, sameApprovals } from "./approval-fields";
import { type AgentKind, defaultKindSettings, KindFields, kindProblem, type KindSettings, kindSettingsOf, sameKindSettings } from "./kind-fields";
import { type AgentTemplate, storeTemplate } from "./labels";
import { catalogMcpNote, catalogMcpUnavailable, catalogModelsNote, catalogRefusal, useTemplateMcpServers, useTemplateModels } from "./template-models";

export function NewVersionDialog({ template, onClose }: { template: AgentTemplate; onClose: () => void }) {
  const qc = useQueryClient();
  const versions = useQuery(templateVersionsQuery(template.id));
  const current = versions.data?.find((v) => v.version === template.currentVersion);
  // In the catalog (or waiting for review), only platform models and MCP servers.
  const inCatalog = Boolean(template.teamSlug) && template.status !== "private";
  const models = useTemplateModels(template.teamSlug, { catalog: inCatalog });
  const servers = useTemplateMcpServers(template.teamSlug, { catalog: inCatalog });
  const [instructions, setInstructions] = useState<string | undefined>(undefined);
  // undefined: the current version's model.
  const [picked, setPicked] = useState<string | undefined>(undefined);
  // undefined: the current version's MCP servers and setting.
  const [pickedMcp, setPickedMcp] = useState<McpAttach[] | undefined>(undefined);
  const [pickedClientMcp, setPickedClientMcp] = useState<boolean | undefined>(undefined);
  // undefined: the current version's tool approvals.
  const [pickedApprovals, setPickedApprovals] = useState<ApprovalPolicy | undefined>(undefined);
  const [pickedSchedules, setPickedSchedules] = useState<boolean | undefined>(undefined);
  // undefined: the current version's kind and settings.
  const [pickedKind, setPickedKind] = useState<AgentKind | undefined>(undefined);
  const [pickedKindSettings, setPickedKindSettings] = useState<KindSettings | undefined>(undefined);
  const [submitted, setSubmitted] = useState(false);
  const [notes, setNotes] = useState("");
  const [busy, setBusy] = useState(false);
  const value = instructions ?? current?.instructions ?? "";
  const currentModel = current?.modelId ?? "";
  const modelId = picked ?? currentModel;
  const currentMcp = attachOf(current?.mcp);
  const mcp = pickedMcp ?? currentMcp;
  const clientMcp = pickedClientMcp ?? current?.clientMcp ?? false;
  const currentApprovals = current?.approvals ?? defaultApprovals;
  const approvals = pickedApprovals ?? currentApprovals;
  const agentSchedules = pickedSchedules ?? current?.agentSchedules ?? false;
  const currentKind: AgentKind = current?.kind ?? "assistant";
  const currentKindSettings = kindSettingsOf(currentKind, current?.kindSettings);
  const kind = pickedKind ?? currentKind;
  const kindSettings = pickedKindSettings ?? (kind === currentKind ? currentKindSettings : defaultKindSettings(kind));
  const kindChanged = kind !== currentKind;
  const submit = async () => {
    setSubmitted(true);
    if (!value.trim() || mcpProblem(mcp) || approvalsProblem(approvals) || kindProblem(kind, kindSettings)) return;
    setBusy(true);
    try {
      const t = unwrap(
        await api.POST("/v1/agent-templates/{templateId}/versions", {
          params: { path: { templateId: template.id } },
          // Left out when unchanged, so a model that's no longer offered stays as it is.
          body: {
            instructions: value,
            notes,
            modelId: modelId === currentModel ? undefined : modelId,
            mcp: sameMcp(mcp, currentMcp) ? undefined : mcp,
            clientMcp: clientMcp === (current?.clientMcp ?? false) ? undefined : clientMcp,
            approvals: sameApprovals(approvals, currentApprovals) ? undefined : approvals,
            agentSchedules: agentSchedules === (current?.agentSchedules ?? false) ? undefined : agentSchedules,
            kind: kindChanged ? kind : undefined,
            kindSettings: kindChanged || !sameKindSettings(kindSettings, currentKindSettings) ? kindSettings : undefined,
          },
        }),
      );
      storeTemplate(qc, t);
      void qc.invalidateQueries({ queryKey: templateVersionsQuery(template.id).queryKey });
      toast.success(`v${t.currentVersion} is the current version`, "New agents get it; existing agents keep theirs until upgraded.");
      onClose();
    } catch (err) {
      toast.error("Couldn't add the version", catalogRefusal(err, "version") ?? errorMessage(err));
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
        description={`Agents move to it when they're upgraded to this version, unless they chose a model of their own.${inCatalog ? ` ${catalogModelsNote}` : ""}`}
        models={models.models}
        loading={models.loading || versions.isLoading}
        value={modelId}
        onChange={setPicked}
        defaultLabel="Platform default"
        current={current?.modelId ? { id: current.modelId, name: current.modelName ?? "Unknown model", providerName: current.modelProviderName ?? "" } : null}
      />
      <McpPicker
        servers={servers.servers}
        loading={servers.loading || versions.isLoading}
        emptyText={servers.emptyText}
        note={inCatalog ? catalogMcpNote : undefined}
        unavailableText={inCatalog ? catalogMcpUnavailable : undefined}
        value={mcp}
        onChange={setPickedMcp}
        current={current?.mcp}
        clientMcp={clientMcp}
        onClientMcpChange={setPickedClientMcp}
        showErrors={submitted}
      />
      <ApprovalFields value={approvals} onChange={setPickedApprovals} showErrors={submitted} />
      <AgentSchedulesField checked={agentSchedules} onCheckedChange={setPickedSchedules} />
      <KindFields
        kind={kind}
        settings={kindSettings}
        onKindChange={(k) => {
          setPickedKind(k);
          setPickedKindSettings(k === currentKind ? currentKindSettings : defaultKindSettings(k));
        }}
        onSettingsChange={setPickedKindSettings}
        showErrors={submitted}
      />
      <Field label="What changed" labelHint="Optional">
        <Input value={notes} maxLength={2000} onValueChange={setNotes} />
      </Field>
    </FormDialog>
  );
}
