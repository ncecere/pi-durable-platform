/* Create a template with its first version: in a team (private), or a platform template (platform admins). */
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { templatesKey } from "../../api/templates";
import { api, ApiError, errorMessage, unwrap } from "../../api/client";
import { FormDialog } from "../../components/form-dialog";
import { McpPicker, mcpProblem } from "../../components/mcp-picker";
import { AgentSchedulesField } from "../../components/templates/agent-schedules-field";
import { ModelSelect } from "../../components/model-select";
import type { McpAttach } from "../../api/mcp";
import { Field } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { toast } from "@/components/ui/toast/toast";
import { type AgentKind, defaultKindSettings, KindFields, kindProblem, type KindSettings } from "./kind-fields";
import { slugProblem } from "./labels";
import { useTemplateMcpServers, useTemplateModels } from "./template-models";

/** team: the owning team's slug, or undefined for a platform template. */
export function CreateTemplateDialog({
  team,
  onClose,
}: {
  team?: string;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [instructions, setInstructions] = useState("");
  const [modelId, setModelId] = useState("");
  const models = useTemplateModels(team);
  const servers = useTemplateMcpServers(team);
  const [mcp, setMcp] = useState<McpAttach[]>([]);
  const [clientMcp, setClientMcp] = useState(false);
  const [agentSchedules, setAgentSchedules] = useState(false);
  const [kind, setKind] = useState<AgentKind>("assistant");
  const [kindSettings, setKindSettings] = useState<KindSettings>({});
  const [submitted, setSubmitted] = useState(false);
  const [taken, setTaken] = useState(false);
  const [busy, setBusy] = useState(false);
  const errors = {
    slug:
      (submitted ? slugProblem(slug) : undefined) ??
      (taken ? "Taken: choose another slug." : undefined),
    name: submitted && !name.trim() ? "Enter a name." : undefined,
    instructions:
      submitted && !instructions.trim()
        ? "Write the agent's instructions."
        : undefined,
  };
  const submit = async () => {
    setSubmitted(true);
    if (slugProblem(slug) || !name.trim() || !instructions.trim() || mcpProblem(mcp) || kindProblem(kind, kindSettings))
      return;
    setBusy(true);
    try {
      const body = {
        slug,
        name: name.trim(),
        description: description.trim() || undefined,
        instructions,
        notes: "Initial version",
        modelId: modelId || undefined,
        mcp: mcp.length ? mcp : undefined,
        clientMcp: clientMcp || undefined,
        agentSchedules: agentSchedules || undefined,
        // The assistant is the default and takes no settings.
        kind: kind === "assistant" ? undefined : kind,
        kindSettings: kind === "assistant" ? undefined : kindSettings,
      };
      const created = team
        ? unwrap(
            await api.POST("/v1/teams/{team}/agent-templates", {
              params: { path: { team } },
              body,
            }),
          )
        : unwrap(await api.POST("/v1/agent-templates", { body }));
      await qc.invalidateQueries({ queryKey: templatesKey });
      toast.success(
        "Template created",
        team
          ? "It's private to the team until it's published."
          : "It's in the catalog.",
      );
      onClose();
      void navigate({
        to: "/templates/$templateId",
        params: { templateId: created.id },
      });
    } catch (err) {
      if (err instanceof ApiError && err.code === "slug_taken") setTaken(true);
      else toast.error("Couldn't create the template", errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <FormDialog
      title={team ? "New team template" : "New platform template"}
      description={
        team
          ? "Private to the team. Its owners and admins can ask to publish it to the catalog."
          : "Every team sees it in the catalog."
      }
      size="lg"
      onClose={onClose}
      onSubmit={() => void submit()}
      submitLabel="Create template"
      busy={busy}
    >
      <Field label="Name" error={errors.name}>
        <Input
          value={name}
          maxLength={100}
          onValueChange={setName}
          autoComplete="off"
        />
      </Field>
      <Field
        label="Slug"
        description="Identifies it in the API; unique in its team (or among platform templates)."
        error={errors.slug}
      >
        <Input
          value={slug}
          maxLength={64}
          spellCheck={false}
          autoComplete="off"
          onValueChange={(v) => {
            setSlug(v.toLowerCase());
            setTaken(false);
          }}
        />
      </Field>
      <Field label="Description" labelHint="Optional">
        <Textarea
          value={description}
          maxLength={2000}
          onChange={(e) => setDescription(e.target.value)}
        />
      </Field>
      <Field
        label="Instructions"
        description="The agent's system prompt. Every later change becomes a new version."
        error={errors.instructions}
      >
        <Textarea
          value={instructions}
          maxLength={20000}
          rows={8}
          onChange={(e) => setInstructions(e.target.value)}
        />
      </Field>
      <ModelSelect
        description={
          team
            ? "The models published to the team. A team can pick another one for each agent."
            : "Teams can pick another of their models for each agent."
        }
        models={models.models}
        loading={models.loading}
        value={modelId}
        onChange={setModelId}
        defaultLabel="Platform default"
      />
      <McpPicker
        servers={servers.servers}
        loading={servers.loading}
        emptyText={servers.emptyText}
        value={mcp}
        onChange={setMcp}
        clientMcp={clientMcp}
        onClientMcpChange={setClientMcp}
        showErrors={submitted}
      />
      <AgentSchedulesField checked={agentSchedules} onCheckedChange={setAgentSchedules} />
      <KindFields
        kind={kind}
        settings={kindSettings}
        onKindChange={(k) => {
          setKind(k);
          setKindSettings(defaultKindSettings(k));
        }}
        onSettingsChange={setKindSettings}
        showErrors={submitted}
      />
    </FormDialog>
  );
}
