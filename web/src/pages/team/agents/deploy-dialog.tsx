/* Deploy an agent from a template into a team (editors and above), then open its Test tab. */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { agentsKey } from "../../../api/agents";
import { api, ApiError, errorMessage, unwrap } from "../../../api/client";
import { catalogQuery } from "../../../api/templates";
import { FormDialog } from "../../../components/form-dialog";
import { Field } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { Select } from "@/components/ui/select/select";
import { toast } from "@/components/ui/toast/toast";
import { nameProblem } from "./labels";

type TeamChoice = { slug: string; name: string };

type Props = {
  /** Where the person may deploy (one fixed team on a team's page). */
  teams: TeamChoice[];
  /** Fixes the template (from its page); otherwise the person picks one the team can deploy. */
  templateId?: string;
  templateName?: string;
  onClose: () => void;
};

export function DeployAgentDialog({ teams, templateId, templateName, onClose }: Props) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [team, setTeam] = useState<string | null>(teams[0]?.slug ?? null);
  // The catalog plus the chosen team's own templates.
  const templates = useQuery({ ...catalogQuery(team ?? undefined), enabled: !templateId && !!team });
  const [picked, setPicked] = useState<string | null>(null);
  const template = templateId ?? (picked && templates.data?.some((t) => t.id === picked) ? picked : (templates.data?.[0]?.id ?? null));
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [submitted, setSubmitted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [taken, setTaken] = useState(false);
  const nameError = (submitted ? nameProblem(name) : undefined) ?? (taken ? "The team already has an agent with this name." : undefined);
  const submit = async () => {
    setSubmitted(true);
    if (!team || !template || nameProblem(name)) return;
    setBusy(true);
    try {
      const created = unwrap(
        await api.POST("/v1/teams/{team}/agents", {
          params: { path: { team } },
          body: { name: name.trim(), description: description.trim() || undefined, templateId: template },
        }),
      );
      await qc.invalidateQueries({ queryKey: agentsKey });
      toast.success("Agent deployed", `${created.name} wakes on its first message.`);
      onClose();
      void navigate({ to: "/teams/$team/agents/$agentId", params: { team: created.teamSlug, agentId: created.id }, search: { tab: "test" } });
    } catch (err) {
      if (err instanceof ApiError && err.code === "name_taken") setTaken(true);
      else toast.error("Couldn't deploy the agent", errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const chosen = templates.data?.find((t) => t.id === template);
  return (
    <FormDialog
      title={templateName ? `Deploy ${templateName}` : "Deploy an agent"}
      onClose={onClose}
      onSubmit={() => void submit()}
      submitLabel="Deploy agent"
      busy={busy}
      submitDisabled={!template || !team}
    >
      {teams.length > 1 ? <Select label="Team" items={teams.map((t) => ({ value: t.slug, label: t.name }))} value={team} onValueChange={setTeam} /> : null}
      {templateId ? null : (
        <Select
          label="Template"
          items={(templates.data ?? []).map((t) => ({ value: t.id, label: t.name, hint: t.teamName ?? "Platform" }))}
          value={template}
          onValueChange={setPicked}
        />
      )}
      {chosen?.description ? <p>{chosen.description}</p> : null}
      <Field label="Name" description="Unique in the team." error={nameError}>
        <Input
          value={name}
          onValueChange={(v) => {
            setName(v);
            setTaken(false);
          }}
          maxLength={100}
          autoComplete="off"
        />
      </Field>
      <Field label="Description" labelHint="Optional" description="Defaults to the template's.">
        <Textarea value={description} onChange={(e) => setDescription(e.target.value)} maxLength={2000} />
      </Field>
    </FormDialog>
  );
}
