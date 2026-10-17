/*
 * An agent's Overview: its runtime and storage, and how the team's app
 * reaches it with a service account key.
 */
import { Link } from "@tanstack/react-router";
import { terms } from "../../../lib/terms";
import { Card } from "@/components/ui/card/card";
import { CodeBlock } from "@/components/ui/code-block/code-block";
import { DescriptionList } from "@/components/ui/description-list/description-list";
import { Stack } from "@/components/ui/layout/layout";
import { TextLink } from "@/components/ui/text-link/text-link";
import { Time } from "@/components/ui/time/time";
import s from "../../shared.module.css";
import { type Agent, type AgentAbilities, agentStatus } from "./labels";

type Props = { agent: Agent; abilities: AgentAbilities };

export function OverviewTab({ agent }: Props) {
  const status = agentStatus(agent);
  return (
    <Stack gap={4}>
      <Card
        title="Runtime"
        description="The agent runs in its own runtime while it's used and hibernates when idle; the next request wakes it."
      >
        <DescriptionList
          dividers
          items={[
            { label: "Status", value: status.label },
            { label: "Template", value: agent.templateName },
            {
              label: "Storage",
              value:
                "Its own schema and database role in the agents database. Conversations survive restarts and hibernation.",
            },
            ...(agent.externalKey
              ? [
                  {
                    label: "External key",
                    value: <span className={s.mono}>{agent.externalKey}</span>,
                  },
                ]
              : []),
            {
              label: "Agent ID",
              value: <span className={s.mono}>{agent.id}</span>,
            },
            { label: "Created", value: <Time value={agent.createdAt} /> },
            { label: "Last changed", value: <Time value={agent.updatedAt} /> },
          ]}
        />
      </Card>
      <UsageCard agent={agent} />
    </Stack>
  );
}

/** How an app talks to the agent: with a team service account's key, through the platform. */
function UsageCard({ agent }: { agent: Agent }) {
  const origin = globalThis.location?.origin ?? "https://platform.example.com";
  const base = `${origin}/v1/agents/${agent.id}`;
  const send = `curl -X POST ${base}/threads/my-thread/messages \\
  -H "Authorization: Bearer $PDP_KEY" -H "Content-Type: application/json" \\
  -d '{"text": "Hello!", "requestId": "msg-1"}'`;
  const stream = `curl -N ${base}/threads/my-thread/events -H "Authorization: Bearer $PDP_KEY"`;
  const perUser = `curl -X PUT ${origin}/v1/teams/${agent.teamSlug}/agents/by-key/USER_ID \\
  -H "Authorization: Bearer $PDP_KEY" -H "Content-Type: application/json" -d '{"name": "Assistant for USER_ID"}'`;
  return (
    <Card
      title="Use it from your app"
      description={
        <span>
          Apps reach agents through the platform with a key of one of the
          team&apos;s{" "}
          <TextLink
            render={
              <Link
                to="/teams/$team/service-accounts"
                params={{ team: agent.teamSlug }}
              />
            }
          >
            {terms.serviceAccounts.toLowerCase()}
          </TextLink>
          , never the runtime directly. Thread IDs are chosen by the app.
        </span>
      }
    >
      <Stack gap={3}>
        <CodeBlock
          code={send}
          language="bash"
          filename="Send a message (202: the reply follows on the thread)"
        />
        <CodeBlock
          code={stream}
          language="bash"
          filename="Follow the conversation (server-sent events)"
        />
        <CodeBlock
          code={perUser}
          language="bash"
          filename="One agent per user of your app (editor service accounts)"
        />
      </Stack>
    </Card>
  );
}
