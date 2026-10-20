/*
 * Move an agent, with its conversations, to another team or the person's
 * personal workspace (Settings › Danger zone). The person picks a team where
 * they are an owner or admin; the platform's dry run (GET …/move) says what
 * changes and what stops the move, and the move is confirmed from there.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { type AgentMovePlan, agentsKey, moveAgent, movePlanQuery } from "../../../api/agents";
import { modelLabel } from "../../../components/model-select";
import { DangerAction } from "../../../components/templates/settings-page";
import { useCurrentUser } from "../../../session";
import { plural } from "../../../lib/format";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { DescriptionList, type DescriptionEntry } from "@/components/ui/description-list/description-list";
import { Dialog, DialogClose } from "@/components/ui/dialog/dialog";
import { Field } from "@/components/ui/field/field";
import { NativeSelect } from "@/components/ui/input/input";
import { Loader } from "@/components/ui/loader/loader";
import { toast } from "@/components/ui/toast/toast";
import { type Agent, storeAgent } from "./labels";

/** The teams the person may move the agent to: active, where they are an owner or admin, other than its own. */
function useTargets(agent: Agent) {
  const me = useCurrentUser();
  return me.teams.filter((t) => t.status === "active" && (t.role === "owner" || t.role === "admin") && t.id !== agent.teamId);
}

export function MoveAgent({ agent }: { agent: Agent }) {
  const targets = useTargets(agent);
  const [open, setOpen] = useState(false);
  const reason = agent.serviceManaged
    ? "A service-managed agent holds its app's users' conversations and stays in the app's team."
    : targets.length === 0
      ? "You aren't an owner or admin of another team or workspace."
      : undefined;
  return (
    <>
      <DangerAction
        title="Move to another team"
        description={`Moves ${agent.name} and its conversations to another team or your personal workspace, whose people and rules apply from then on.`}
        disabledReason={reason}
        action={
          <Button variant="secondary" disabled={reason !== undefined} onClick={() => setOpen(true)}>
            Move agent…
          </Button>
        }
      />
      {open && <MoveDialog agent={agent} onClose={() => setOpen(false)} />}
    </>
  );
}

function MoveDialog({ agent, onClose }: { agent: Agent; onClose: () => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const targets = useTargets(agent);
  const [team, setTeam] = useState("");
  const plan = useQuery({ ...movePlanQuery(agent.id, team), enabled: team !== "" });
  const move = useMutation({
    mutationFn: () => moveAgent(agent, team),
    onSuccess: (moved) => {
      storeAgent(qc, moved);
      toast.success(`${moved.name} moved to ${moved.teamName}`);
      onClose();
      void navigate({
        to: "/teams/$team/agents/$agentId",
        params: { team: moved.teamSlug, agentId: moved.id },
        replace: true,
      }).then(() => qc.invalidateQueries({ queryKey: agentsKey }));
    },
  });
  const p = team !== "" ? plan.data : undefined;
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && !move.isPending && onClose()}
      title={`Move ${agent.name}`}
      description="Its storage stays where it is: only the team it belongs to changes. Review what changes before moving it."
      size="lg"
      footer={
        <>
          <DialogClose>Cancel</DialogClose>
          <Button variant="danger" loading={move.isPending} disabled={!p?.allowed} onClick={() => move.mutate()}>
            Move agent
          </Button>
        </>
      }
    >
      <Field label="Move to" description="Teams and workspaces where you are an owner or admin.">
        <NativeSelect
          value={team}
          onChange={(e) => {
            setTeam(e.target.value);
            move.reset();
          }}
        >
          <option value="">Choose a team…</option>
          {targets.map((t) => (
            <option key={t.id} value={t.slug}>
              {t.kind === "personal" ? "Your personal workspace" : t.name}
            </option>
          ))}
        </NativeSelect>
      </Field>
      {team !== "" && plan.isLoading && <Loader label="Checking what changes…" />}
      <ErrorAlert error={team !== "" ? plan.error : null} />
      {p && <PlanSummary plan={p} />}
      <ErrorAlert error={move.error} />
    </Dialog>
  );
}

const where = (t: AgentMovePlan["to"]) => (t.kind === "personal" ? "the personal workspace" : t.name);

/** The dry run in words: what stops the move, and what changes. */
export function PlanSummary({ plan }: { plan: AgentMovePlan }) {
  const to = where(plan.to);
  const items: DescriptionEntry[] = [
    {
      label: "Shared conversations",
      value: `${plural(plan.sharedThreads, "conversation")}, visible from now on to ${plural(plan.targetMembers, "person", "people")}${
        plan.targetServiceAccounts > 0 ? ` and ${plural(plan.targetServiceAccounts, "service account")}` : ""
      } in ${to}.`,
    },
    {
      label: "Private conversations",
      value:
        `${plural(plan.privateThreads, "conversation")}, still only their creators'.` +
        (plan.privateThreadsUnreachable > 0 ? ` ${plural(plan.privateThreadsUnreachable, "of them is", "of them are")} out of reach: their creators aren't in ${to}.` : ""),
    },
    {
      label: "Losing access",
      value:
        plan.peopleLosingAccess + plan.serviceAccountsLosingAccess === 0
          ? "Nobody."
          : `${plural(plan.peopleLosingAccess, "person", "people")} and ${plural(plan.serviceAccountsLosingAccess, "service account")} of ${where(plan.from)} (their keys stop reaching it).`,
    },
    { label: "Model", value: plan.model ? modelLabel(plan.model) : "Platform default" },
  ];
  if (plan.externalKey) items.push({ label: "External key", value: `“${plan.externalKey}” no longer finds it: the key is dropped.` });
  if (plan.threadMcpServers > 0)
    items.push({ label: "Apps' MCP servers", value: `${plural(plan.threadMcpServers, "server")} set on its conversations will be removed.` });
  if (plan.mcpServersDropped.length > 0)
    items.push({ label: "Tools", value: `${plan.mcpServersDropped.join(", ")} stop applying: ${to} may not use them.` });
  return (
    <>
      {!plan.allowed && (
        <Alert tone="danger" title={`It can't move to ${to} yet`}>
          <ul>
            {plan.blockers.map((b) => (
              <li key={b.code}>{b.message}</li>
            ))}
          </ul>
        </Alert>
      )}
      <DescriptionList size="sm" dividers items={items} />
    </>
  );
}
