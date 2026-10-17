/*
 * One template (/templates/$templateId): Overview (what new agents get),
 * Versions, and Settings. Teams deploy it from here; its team's owners and
 * admins ask to publish it; platform admins approve or reject the request.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { History, LayoutDashboard, Rocket, Settings2 } from "lucide-react";
import { useState } from "react";
import { api, errorMessage, unwrap } from "../../api/client";
import { templateQuery, templateVersionsQuery } from "../../api/templates";
import { isNotFound, NotFoundState } from "../../components/not-found";
import { PageSkeleton } from "../../components/page-skeleton";
import { DetailPage } from "../../components/templates/detail-page";
import { templateTabs } from "../../lib/tabs";
import { useCurrentUser } from "../../session";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { CodeBlock } from "@/components/ui/code-block/code-block";
import { Stack } from "@/components/ui/layout/layout";
import { toast } from "@/components/ui/toast/toast";
import { DeployAgentDialog } from "../team/agents/deploy-dialog";
import {
  type AgentTemplate,
  ownerLabel,
  statusLabel,
  statusTone,
  storeTemplate,
  type TemplateAbilities,
  templateAbilities,
} from "./labels";
import { ReviewDialog } from "./review-dialog";
import { TemplateSettingsTab } from "./settings-tab";
import { VersionsTab } from "./versions-tab";

export function TemplatePage() {
  const { templateId } = useParams({ from: "/app/templates/$templateId" });
  const template = useQuery(templateQuery(templateId));
  if (template.isLoading) return <PageSkeleton />;
  if (isNotFound(template.error)) return <NotFoundState what="object" />;
  if (!template.data)
    return (
      <ErrorAlert
        error={template.error}
        title="Couldn't load this template"
        onRetry={() => void template.refetch()}
      />
    );
  return <Record template={template.data} />;
}

function Record({ template }: { template: AgentTemplate }) {
  const me = useCurrentUser();
  const qc = useQueryClient();
  const abilities = templateAbilities(me, template);
  const [deploying, setDeploying] = useState(false);
  const [reviewing, setReviewing] = useState(false);
  const publish = useMutation({
    mutationFn: async (action: "publish" | "unpublish") =>
      unwrap(
        await api.POST(
          action === "publish"
            ? "/v1/agent-templates/{templateId}/publish"
            : "/v1/agent-templates/{templateId}/unpublish",
          {
            params: { path: { templateId: template.id } },
          },
        ),
      ),
    onSuccess: (t) => {
      storeTemplate(qc, t);
      toast.success(
        t.status === "pending" ? "Sent for review" : "Taken out of the catalog",
        t.status === "pending"
          ? "A platform admin approves or rejects it."
          : "Agents deployed from it keep working.",
      );
    },
    onError: (err) =>
      toast.error("Couldn't change publishing", errorMessage(err)),
  });
  const primary = abilities.canReview ? (
    <Button onClick={() => setReviewing(true)}>Review request</Button>
  ) : abilities.deployTeams.length > 0 ? (
    <Button onClick={() => setDeploying(true)}>
      <Rocket aria-hidden /> Deploy agent
    </Button>
  ) : undefined;
  const menu = [
    {
      label: "Submit to the catalog",
      hidden:
        !abilities.canPublish ||
        template.status !== "private" ||
        template.archived,
      onSelect: () => publish.mutate("publish"),
    },
    {
      label:
        template.status === "pending"
          ? "Withdraw the request"
          : "Take out of the catalog",
      hidden: !abilities.canPublish || template.status === "private",
      onSelect: () => publish.mutate("unpublish"),
    },
  ];
  return (
    <>
      <DetailPage
        title={template.name}
        meta={
          template.archived ? (
            <Badge tone="neutral">Archived</Badge>
          ) : (
            <Badge tone={statusTone[template.status]}>
              {template.teamSlug ? statusLabel[template.status] : "Platform"}
            </Badge>
          )
        }
        description={template.description || undefined}
        facts={[
          { label: "Owner", value: ownerLabel(template) },
          { label: "Version", value: `v${template.currentVersion}` },
          {
            label: "Agents",
            value:
              template.agentCount != null
                ? `${template.agentCount} deployed`
                : undefined,
          },
        ]}
        primaryAction={primary}
        menuActions={menu}
        notices={<Notices template={template} abilities={abilities} />}
        tabIds={templateTabs}
        tabsLabel="Template sections"
        tabs={[
          {
            value: "overview",
            label: "Overview",
            icon: <LayoutDashboard aria-hidden />,
            content: <OverviewTab template={template} />,
          },
          {
            value: "versions",
            label: "Versions",
            icon: <History aria-hidden />,
            count: template.currentVersion,
            content: <VersionsTab template={template} abilities={abilities} />,
          },
          {
            value: "settings",
            label: "Settings",
            icon: <Settings2 aria-hidden />,
            content: (
              <TemplateSettingsTab template={template} abilities={abilities} />
            ),
          },
        ]}
      />
      {deploying && (
        <DeployAgentDialog
          teams={abilities.deployTeams}
          templateId={template.id}
          templateName={template.name}
          onClose={() => setDeploying(false)}
        />
      )}
      {reviewing && (
        <ReviewDialog template={template} onClose={() => setReviewing(false)} />
      )}
    </>
  );
}

function Notices({
  template,
  abilities,
}: {
  template: AgentTemplate;
  abilities: TemplateAbilities;
}) {
  if (template.archived) {
    return (
      <Alert tone="info" title="Archived">
        It takes no new deployments. Agents deployed from it keep their version.
      </Alert>
    );
  }
  if (template.status === "pending") {
    return (
      <Alert tone="warning" title="Waiting for review">
        {abilities.canReview
          ? "This team asked to publish the template to the catalog."
          : "A platform admin approves or rejects the request to publish it."}
      </Alert>
    );
  }
  if (template.reviewNote) {
    return (
      <Alert tone="danger" title="The request to publish was rejected">
        {template.reviewNote}
      </Alert>
    );
  }
  return null;
}

/** What new agents get: the current version's instructions. */
function OverviewTab({ template }: { template: AgentTemplate }) {
  const versions = useQuery(templateVersionsQuery(template.id));
  const current = versions.data?.find(
    (v) => v.version === template.currentVersion,
  );
  return (
    <Stack gap={4}>
      <Card
        title={`Instructions (v${template.currentVersion})`}
        description="The system prompt agents deployed now get. Agents keep their version until they're upgraded."
      >
        {current ? (
          <CodeBlock code={current.instructions} language="text" hideHeader />
        ) : null}
      </Card>
    </Stack>
  );
}
