/*
 * Agents: the person's assistant and their teams' agents, each a durable Pi
 * agent in its own runtime. Team editors create team agents from templates.
 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Bot, Plus, Sparkles } from "lucide-react";
import { useState } from "react";
import { type Agent, agentsKey, agentsQuery, templatesQuery } from "../../api/agents";
import { api, errorMessage, unwrap } from "../../api/client";
import { FormDialog } from "../../components/form-dialog";
import { terms } from "../../lib/terms";
import { useCurrentUser } from "../../session";
import { Alert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import { Field } from "@/components/ui/field/field";
import { Input } from "@/components/ui/input/input";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { Select } from "@/components/ui/select/select";
import { Loading } from "@/components/ui/spinner/spinner";
import { toast } from "@/components/ui/toast/toast";
import shared from "../shared.module.css";
import s from "./agents.module.css";

export function AgentsPage() {
  const agents = useQuery(agentsQuery());
  const me = useCurrentUser();
  const editable = me.teams.filter((t) => t.status === "active" && t.role !== "member");
  const [creating, setCreating] = useState(false);
  return (
    <Stack gap={6} className={shared.page}>
      <PageHeader
        title={terms.agents}
        description="Durable Pi agents: your assistant and your teams' agents. Each runs in its own runtime, hibernates when idle and wakes when you talk to it."
        actions={
          editable.length > 0 ? (
            <Button onClick={() => setCreating(true)}>
              <Plus aria-hidden /> New team agent
            </Button>
          ) : undefined
        }
      />
      {agents.isPending ? (
        <Loading label="Loading agents…" />
      ) : agents.isError ? (
        <Alert tone="danger" title="Couldn't load agents">
          {errorMessage(agents.error)}
        </Alert>
      ) : agents.data.length === 0 ? (
        <NoAgents />
      ) : (
        <AgentGrid agents={agents.data} />
      )}
      {creating ? <CreateAgentDialog teams={editable} onClose={() => setCreating(false)} /> : null}
    </Stack>
  );
}

function NoAgents() {
  return (
    <Card>
      <EmptyState
        icon={<Sparkles />}
        title="No agents yet."
        description="Open your assistant to create it, or create an agent for one of your teams."
        action={
          <Button render={<Link to="/assistant" />} variant="secondary">
            Open my assistant
          </Button>
        }
      />
    </Card>
  );
}

function AgentGrid({ agents }: { agents: Agent[] }) {
  return (
    <div className={s.grid}>
      {agents.map((a) => (
        <Card
          key={a.id}
          interactive
          title={
            <Link to={a.kind === "personal" ? "/assistant" : "/agents/$agentId"} params={{ agentId: a.id }}>
              {a.name}
            </Link>
          }
        >
          <p className={s.description}>{a.description}</p>
          <div className={s.cardMeta}>
            <Badge size="sm" tone={a.kind === "personal" ? "info" : "neutral"}>
              {a.kind === "personal" ? "Personal" : a.teamName}
            </Badge>
            <Badge size="sm" dot tone={a.running ? "success" : "neutral"}>
              {a.running ? "Awake" : "Hibernating"}
            </Badge>
            {!a.enabled ? (
              <Badge size="sm" tone="warning">
                Off
              </Badge>
            ) : null}
            <span className={s.hint}>{a.templateName}</span>
          </div>
        </Card>
      ))}
    </div>
  );
}

type EditableTeam = { slug: string; name: string };

function CreateAgentDialog({ teams, onClose }: { teams: EditableTeam[]; onClose: () => void }) {
  const queryClient = useQueryClient();
  const templates = useQuery(templatesQuery());
  const [team, setTeam] = useState<string | null>(teams[0]?.slug ?? null);
  const [template, setTemplate] = useState<string | null>("assistant");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    if (!team) return;
    setBusy(true);
    try {
      const created = unwrap(
        await api.POST("/v1/teams/{team}/agents", {
          params: { path: { team } },
          body: { name, description: description || undefined, template: template ?? undefined },
        }),
      );
      await queryClient.invalidateQueries({ queryKey: agentsKey });
      toast.success("Agent created", `${created.name} is ready to talk to.`);
      onClose();
    } catch (err) {
      toast.error("Couldn't create the agent", errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <FormDialog title="New team agent" onClose={onClose} onSubmit={() => void submit()} submitLabel="Create agent" busy={busy} submitDisabled={!name.trim() || !team}>
      <Select label="Team" items={teams.map((t) => ({ value: t.slug, label: t.name }))} value={team} onValueChange={setTeam} />
      <Select
        label="Template"
        items={(templates.data ?? []).map((t) => ({ value: t.slug, label: t.name, hint: t.description }))}
        value={template}
        onValueChange={setTemplate}
      />
      <Field label="Name">
        <Input value={name} onValueChange={setName} maxLength={100} required startIcon={<Bot aria-hidden />} />
      </Field>
      <Field label="Description" description="Optional. Defaults to the template's.">
        <Input value={description} onValueChange={setDescription} maxLength={2000} />
      </Field>
    </FormDialog>
  );
}
