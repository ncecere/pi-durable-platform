/*
 * One of the team's agents (/teams/$team/agents/$agentId): Overview (status,
 * runtime and how an app reaches it), Test (a chat with it, for the team's
 * members) and Settings. Service-managed agents show to the team's owners
 * and admins without Test: their conversations belong to the app's users.
 * Platform staff auditing a personal workspace see Overview and Settings,
 * read-only, without Test (the API keeps conversations from them).
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { LayoutDashboard, MessageSquare, Moon, Settings2 } from "lucide-react";
import { agentQuery } from "../../../api/agents";
import { api, errorMessage, unwrap } from "../../../api/client";
import { isNotFound, NotFoundState } from "../../../components/not-found";
import { PageSkeleton } from "../../../components/page-skeleton";
import { RelativeTime } from "../../../components/templates/list-page";
import { DetailPage } from "../../../components/templates/detail-page";
import { agentTabs } from "../../../lib/tabs";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { toast } from "@/components/ui/toast/toast";
import { useTeam } from "../common";
import {
  type Agent,
  agentAbilities,
  agentStatus,
  storeAgent,
  whatIsServiceManaged,
} from "./labels";
import { ModelUnavailableNotice } from "./agent-model";
import { OverviewTab } from "./overview-tab";
import { SettingsTab } from "./settings-tab";
import { TestChat } from "./test-chat";

export function TeamAgentPage() {
  const { agentId } = useParams({ from: "/app/teams/$team/agents/$agentId" });
  const agent = useQuery({ ...agentQuery(agentId), refetchInterval: 15_000 });
  if (agent.isLoading) return <PageSkeleton />;
  if (isNotFound(agent.error)) return <NotFoundState what="object" />;
  if (!agent.data)
    return (
      <ErrorAlert
        error={agent.error}
        title="Couldn't load this agent"
        onRetry={() => void agent.refetch()}
      />
    );
  return <Record agent={agent.data} />;
}

function Record({ agent }: { agent: Agent }) {
  const ctx = useTeam();
  const qc = useQueryClient();
  const abilities = agentAbilities(ctx, agent);
  const status = agentStatus(agent);
  const hibernate = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST("/v1/agents/{agentId}/hibernate", {
          params: { path: { agentId: agent.id } },
        }),
      ),
    onSuccess: () => {
      storeAgent(qc, { ...agent, running: false });
      toast.success(
        `${agent.name} is hibernating`,
        "Its next message wakes it.",
      );
    },
    onError: (err) =>
      toast.error("Couldn't hibernate the agent", errorMessage(err)),
  });
  const props = { agent, abilities };
  return (
    <DetailPage
      title={agent.name}
      meta={
        <Badge dot tone={status.tone}>
          {status.label}
        </Badge>
      }
      description={agent.description || undefined}
      facts={[
        { label: "Template", value: agent.templateName },
        {
          label: "Model",
          value: agent.model ? agent.model.name : "Platform default",
        },
        {
          label: "Managed by",
          value: agent.externalKey ? "A service" : ctx.team.name,
        },
        {
          label: "Last used",
          value: agent.lastUsedAt ? (
            <span>
              Used <RelativeTime value={agent.lastUsedAt} />
            </span>
          ) : (
            "Never used"
          ),
        },
      ]}
      primaryAction={
        // Platform staff auditing a personal workspace's agent only look.
        agent.running && ctx.role ? (
          <Button
            variant="secondary"
            loading={hibernate.isPending}
            onClick={() => hibernate.mutate()}
          >
            <Moon aria-hidden /> Hibernate
          </Button>
        ) : undefined
      }
      notices={
        <>
          {!ctx.role ? (
            <Alert tone="info" title="You're viewing this agent as platform staff">
              You can see its settings, status and usage, not its conversations.
            </Alert>
          ) : null}
          {agent.externalKey ? (
            <Alert tone="info" title="Service-managed">
              {whatIsServiceManaged}
            </Alert>
          ) : null}
          <ModelUnavailableNotice agent={agent} canEdit={abilities.canEdit} />
          {!agent.enabled ? (
            <Alert tone="warning" title="Turned off">
              It refuses new messages until it&apos;s turned on again in
              Settings. Its conversations are kept.
            </Alert>
          ) : null}
        </>
      }
      tabIds={agentTabs}
      tabsLabel="Agent sections"
      tabs={[
        {
          value: "overview",
          label: "Overview",
          icon: <LayoutDashboard aria-hidden />,
          content: <OverviewTab {...props} />,
        },
        ...(abilities.canUse
          ? [
              {
                value: "test" as const,
                label: "Test",
                icon: <MessageSquare aria-hidden />,
                content: <TestChat agent={agent} />,
              },
            ]
          : []),
        {
          value: "settings",
          label: "Settings",
          icon: <Settings2 aria-hidden />,
          content: <SettingsTab {...props} />,
        },
      ]}
    />
  );
}
