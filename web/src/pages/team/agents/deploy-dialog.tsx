/* Deploy an agent from a template into a team (editors and above), then open it. */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { agentsKey, templatesQuery } from "../../../api/agents";
import { api, ApiError, errorMessage, unwrap } from "../../../api/client";
import { FormDialog } from "../../../components/form-dialog";
import { Field } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { Select } from "@/components/ui/select/select";
import { toast } from "@/components/ui/toast/toast";
import { nameProblem } from "./labels";

type TeamChoice = { slug: string; name: string };

/** teams: where the person may deploy (one fixed team on a team's page). */
export function DeployAgentDialog({
  teams,
  onClose,
}: {
  teams: TeamChoice[];
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const templates = useQuery(templatesQuery());
  const [team, setTeam] = useState<string | null>(teams[0]?.slug ?? null);
  const [template, setTemplate] = useState<string | null>("assistant");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [submitted, setSubmitted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [taken, setTaken] = useState(false);
  const nameError =
    (submitted ? nameProblem(name) : undefined) ??
    (taken ? "The team already has an agent with this name." : undefined);
  const submit = async () => {
    setSubmitted(true);
    if (!team || nameProblem(name)) return;
    setBusy(true);
    try {
      const created = unwrap(
        await api.POST("/v1/teams/{team}/agents", {
          params: { path: { team } },
          body: {
            name: name.trim(),
            description: description.trim() || undefined,
            template: template ?? undefined,
          },
        }),
      );
      await qc.invalidateQueries({ queryKey: agentsKey });
      toast.success(
        "Agent deployed",
        `${created.name} wakes on its first message.`,
      );
      onClose();
      void navigate({
        to: "/teams/$team/agents/$agentId",
        params: { team: created.teamSlug, agentId: created.id },
        search: { tab: "test" },
      });
    } catch (err) {
      if (err instanceof ApiError && err.code === "name_taken") setTaken(true);
      else toast.error("Couldn't deploy the agent", errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const chosen = templates.data?.find((t) => t.slug === template);
  return (
    <FormDialog
      title="Deploy an agent"
      onClose={onClose}
      onSubmit={() => void submit()}
      submitLabel="Deploy agent"
      busy={busy}
    >
      {teams.length > 1 ? (
        <Select
          label="Team"
          items={teams.map((t) => ({ value: t.slug, label: t.name }))}
          value={team}
          onValueChange={setTeam}
        />
      ) : null}
      <Select
        label="Template"
        items={(templates.data ?? []).map((t) => ({
          value: t.slug,
          label: t.name,
        }))}
        value={template}
        onValueChange={setTemplate}
      />
      {chosen ? <p>{chosen.description}</p> : null}
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
      <Field
        label="Description"
        labelHint="Optional"
        description="Defaults to the template's."
      >
        <Textarea
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          maxLength={2000}
        />
      </Field>
    </FormDialog>
  );
}
