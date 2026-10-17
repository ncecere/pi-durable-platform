/*
 * The person's own assistant: created on their first visit (a durable Pi
 * agent only they can reach) and opened as a chat.
 */
import { useQuery } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { agentQuery, ensureAssistant } from "../../api/agents";
import { errorMessage } from "../../api/client";
import { AgentChat } from "./chat";
import { Alert } from "@/components/ui/alert/alert";
import { Loading } from "@/components/ui/spinner/spinner";

export function AssistantPage() {
  // POST is idempotent: the first visit creates the assistant, later ones return it.
  const assistant = useQuery({ queryKey: ["agents", "assistant"], queryFn: ensureAssistant, refetchInterval: 15_000 });
  if (assistant.isPending) return <Loading label="Opening your assistant…" />;
  if (assistant.isError) {
    return (
      <Alert tone="danger" title="Couldn't open your assistant">
        {errorMessage(assistant.error)}
      </Alert>
    );
  }
  return <AgentChat agent={assistant.data} />;
}

/** A team agent's chat. */
export function AgentPage() {
  const { agentId } = useParams({ from: "/app/agents/$agentId" });
  const agent = useQuery({ ...agentQuery(agentId), refetchInterval: 15_000 });
  if (agent.isPending) return <Loading label="Opening the agent…" />;
  if (agent.isError) {
    return (
      <Alert tone="danger" title="Couldn't open this agent">
        {errorMessage(agent.error)}
      </Alert>
    );
  }
  return <AgentChat key={agentId} agent={agent.data} />;
}
