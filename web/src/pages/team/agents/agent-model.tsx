/*
 * An agent's model: what the Overview shows (from the template or chosen for
 * the agent), the Settings select (the team's models, or "Template's model",
 * sent as ""), and the warning when the model is no longer available.
 */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { teamModelsQuery } from "../../../api/models";
import { ModelSelect, modelLabel } from "../../../components/model-select";
import { Alert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import s from "../../shared.module.css";
import type { Agent } from "./labels";

/** The Settings value: the model chosen for the agent, or "" for its template's. */
export const chosenModel = (a: Agent) => (a.model?.source === "agent" ? a.model.id : "");

/** The model's display labels by id, for the list of changes made elsewhere. */
export function useModelChoices(agent: Agent): Record<string, string> {
  const models = useQuery(teamModelsQuery(agent.teamSlug));
  const out: Record<string, string> = { "": "Template's model" };
  if (agent.model) out[agent.model.id] = modelLabel(agent.model);
  for (const m of models.data ?? []) out[m.id] = modelLabel(m);
  return out;
}

type FieldProps = { agent: Agent; value: string; onChange: (id: string) => void; disabled?: boolean };

export function AgentModelField({ agent, value, onChange, disabled }: FieldProps) {
  const models = useQuery(teamModelsQuery(agent.teamSlug));
  const own = agent.model?.source === "agent" ? agent.model : null;
  const fromTemplate = agent.model?.source === "template" ? agent.model : null;
  return (
    <ModelSelect
      description={
        <>
          {fromTemplate ? `The template's model is ${modelLabel(fromTemplate)}. ` : !agent.model ? "The template uses the platform default model. " : null}
          A change applies from the agent&apos;s next start; its conversations are kept.
        </>
      }
      models={models.data}
      loading={models.isLoading}
      value={value}
      onChange={onChange}
      defaultLabel="Template's model"
      current={own}
      disabled={disabled}
    />
  );
}

/** The Overview's Model row. */
export function AgentModelValue({ agent }: { agent: Agent }) {
  const m = agent.model;
  if (!m) return <span>Platform default</span>;
  return (
    <span>
      {modelLabel(m)} {!m.available ? <Badge tone="warning">Unavailable</Badge> : null}
      <span className={s.secondary}>{m.source === "agent" ? "Chosen for this agent" : "From the template"}</span>
    </span>
  );
}

/** The agent won't start while its model is off or no longer published to the team. */
export function ModelUnavailableNotice({ agent, canEdit }: { agent: Agent; canEdit: boolean }) {
  const m = agent.model;
  if (!m || m.available) return null;
  return (
    <Alert
      tone="warning"
      title="Its model isn't available"
      actions={
        canEdit ? (
          <Button size="sm" variant="secondary" render={<Link to="/teams/$team/agents/$agentId" params={{ team: agent.teamSlug, agentId: agent.id }} search={{ tab: "settings" }} />}>
            Choose another model
          </Button>
        ) : undefined
      }
    >
      {modelLabel(m)} was turned off or is no longer published to {agent.teamName}. The agent won&apos;t start until it&apos;s moved to another model
      {canEdit ? "." : ": the team's editors can choose one in Settings."}
    </Alert>
  );
}
