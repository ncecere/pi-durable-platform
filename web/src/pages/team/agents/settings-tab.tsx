/*
 * An agent's Settings: name, description, model and the switch (If-Match on
 * its revision). Editors change them; owners and admins move the agent to
 * another team (move-agent.tsx) or delete it and all its conversations in the
 * Danger zone.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { agentsKey } from "../../../api/agents";
import { api, ApiError, ifMatch, unwrap } from "../../../api/client";
import { ConfirmMutationDialog } from "../../../components/confirm-dialog";
import { useRevisionForm } from "../../../components/templates/revision-form";
import {
  DangerAction,
  DangerZone,
  SettingsPage,
  SettingsSection,
} from "../../../components/templates/settings-page";
import { Button } from "@/components/ui/button/button";
import { Field } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { Switch } from "@/components/ui/switch/switch";
import { toast } from "@/components/ui/toast/toast";
import { AgentModelField, chosenModel, useModelChoices } from "./agent-model";
import { MoveAgent } from "./move-agent";
import {
  type Agent,
  type AgentAbilities,
  nameProblem,
  storeAgent,
} from "./labels";

type Form = {
  name: string;
  description: string;
  /** A model chosen for the agent, or "" for its template's. */
  modelId: string;
  enabled: boolean;
};
const formOf = (a: Agent): Form => ({
  name: a.name,
  description: a.description,
  modelId: chosenModel(a),
  enabled: a.enabled,
});
const labels = {
  name: "Name",
  description: "Description",
  modelId: "Model",
  enabled: "Enabled",
};

export function SettingsTab({
  agent,
  abilities,
}: {
  agent: Agent;
  abilities: AgentAbilities;
}) {
  const qc = useQueryClient();
  const saved = formOf(agent);
  const modelNames = useModelChoices(agent);
  const [form, setForm, revision] = useRevisionForm(saved, agent.revision, {
    labels,
    choices: { modelId: modelNames },
  });
  const [submitted, setSubmitted] = useState(false);
  const [confirmingOff, setConfirmingOff] = useState<Form | null>(null);
  const save = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.PATCH("/v1/agents/{agentId}", {
          params: {
            path: { agentId: agent.id },
            header: ifMatch(agent.revision),
          },
          body: {
            name: form.name.trim(),
            description: form.description,
            enabled: form.enabled,
            // Only when changed: "" moves it back to its template's model.
            ...(form.modelId !== saved.modelId
              ? { modelId: form.modelId }
              : {}),
          },
        }),
      ),
    onSuccess: (updated) => {
      setSubmitted(false);
      setConfirmingOff(null);
      storeAgent(qc, updated);
      toast.success(
        "Agent saved",
        updated.enabled === agent.enabled
          ? undefined
          : updated.enabled
            ? "It accepts messages again."
            : "It refuses new messages from now on.",
      );
    },
  });
  const canEdit = abilities.canEdit;
  const taken =
    save.error instanceof ApiError && save.error.code === "name_taken";
  const nameError =
    (submitted ? nameProblem(form.name) : undefined) ??
    (taken ? "Another agent in this team has this name." : undefined);
  const dirty = JSON.stringify(form) !== JSON.stringify(saved);
  return (
    <SettingsPage
      revision={revision}
      canEdit={canEdit}
      readOnlyNote={
        agent.serviceManaged
          ? "The service that created this agent manages it."
          : undefined
      }
      dirty={dirty}
      saving={save.isPending}
      error={taken ? undefined : save.error}
      message={nameError ? "Not saved: fix the highlighted field" : undefined}
      onSave={() => {
        setSubmitted(true);
        if (nameProblem(form.name)) return;
        if (agent.enabled && !form.enabled) setConfirmingOff(form);
        else save.mutate();
      }}
      onDiscard={() => {
        setForm(saved);
        setSubmitted(false);
        save.reset();
      }}
    >
      <SettingsSection
        title="General"
        description="How the agent is named in lists, and the model it runs on. The template sets its instructions and tools."
      >
        <Field label="Name" description="Unique in the team." error={nameError}>
          <Input
            maxLength={100}
            disabled={!canEdit}
            value={form.name}
            onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
          />
        </Field>
        <Field label="Description" labelHint="Optional">
          <Textarea
            maxLength={2000}
            disabled={!canEdit}
            value={form.description}
            onChange={(e) =>
              setForm((f) => ({ ...f, description: e.target.value }))
            }
          />
        </Field>
        <AgentModelField
          agent={agent}
          value={form.modelId}
          disabled={!canEdit}
          onChange={(modelId) => setForm((f) => ({ ...f, modelId }))}
        />
        <Switch
          label="Enabled"
          description="Turned off, it hibernates and refuses new messages. Its conversations are kept."
          disabled={!canEdit}
          checked={form.enabled}
          onCheckedChange={(v) => setForm((f) => ({ ...f, enabled: v }))}
        />
      </SettingsSection>
      {abilities.canDelete && <DeleteAgent agent={agent} />}
      <ConfirmMutationDialog
        target={confirmingOff}
        onClose={() => setConfirmingOff(null)}
        mutation={save}
        onConfirm={() => save.mutate()}
        title={`Turn off ${agent.name}?`}
        description="It hibernates now, and apps get an error on their next message until it's turned on again."
        confirmLabel="Turn off agent"
      />
    </SettingsPage>
  );
}

function DeleteAgent({ agent }: { agent: Agent }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState<Agent | null>(null);
  const del = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.DELETE("/v1/agents/{agentId}", {
          params: { path: { agentId: agent.id } },
        }),
      ),
    onSuccess: () => {
      setConfirming(null);
      toast.success(`${agent.name} was deleted`);
      void navigate({
        to: "/teams/$team/agents",
        params: { team: agent.teamSlug },
        replace: true,
      }).then(() => qc.invalidateQueries({ queryKey: agentsKey }));
    },
  });
  return (
    <DangerZone>
      <MoveAgent agent={agent} />
      <DangerAction
        title="Delete this agent"
        description={`Stops ${agent.name} and deletes its storage: every conversation it holds. This can't be undone.`}
        action={
          <Button variant="danger" onClick={() => setConfirming(agent)}>
            Delete agent
          </Button>
        }
      />
      <ConfirmMutationDialog
        target={confirming}
        onClose={() => setConfirming(null)}
        mutation={del}
        onConfirm={() => del.mutate()}
        title={`Delete ${agent.name}?`}
        description="Its conversations are deleted with it, and apps using it get errors."
        confirmLabel="Delete agent"
      />
    </DangerZone>
  );
}
