/*
 * One MCP server: its tools (read again with Refresh tools) and its
 * settings. Admin › MCP servers (platform admins change any server, auditors
 * read) and Team › MCP servers (the team's owners and admins change the
 * team's own, its other members read) share it through an McpCatalogScope. A
 * team's server shows its owner in Admin and has no access mode or
 * publishing (scope.tsx).
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { RefreshCw, Settings2, Trash2, Wrench } from "lucide-react";
import { useState } from "react";
import { deleteMcpServer, invalidateMcp, type McpServer, mcpServerQuery } from "@/api/mcp";
import { adminScope } from "@/api/models";
import { ConfirmMutationDialog } from "@/components/confirm-dialog";
import { isNotFound, NotFoundState } from "@/components/not-found";
import { PageSkeleton } from "@/components/page-skeleton";
import { DetailPage } from "@/components/templates/detail-page";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { toast } from "@/components/ui/toast/toast";
import { mcpServerTabs } from "@/lib/tabs";
import { adminOnly } from "@/lib/terms";
import { useIsPlatformAdmin } from "../hooks";
import { AccessBadge, OwnerCell } from "../models/columns";
import { McpStatus } from "./columns";
import { mcpPublishedTo, teamOnly, toolCount } from "./labels";
import { isTeamOwned, type McpCatalogScope, useMcpNavigate } from "./scope";
import { McpSettingsTab } from "./server-settings";
import { McpToolsTab, useRefreshTools } from "./server-tools";

/** /admin/mcp-servers/$serverId */
export function AdminMcpServerPage() {
  const { serverId } = useParams({ from: "/app/admin/mcp-servers/$serverId" });
  const isAdmin = useIsPlatformAdmin();
  return <McpServerPage serverId={serverId} catalog={{ scope: adminScope, canManage: isAdmin, readOnlyNote: adminOnly }} />;
}

/** A server of the scope by id: loading, not found (404), or its page. */
export function McpServerPage({ serverId, catalog }: { serverId: string; catalog: McpCatalogScope }) {
  const server = useQuery(mcpServerQuery(catalog.scope, serverId));
  if (server.isLoading) return <PageSkeleton />;
  if (isNotFound(server.error)) return <NotFoundState what="object" />;
  if (!server.data) return <ErrorAlert error={server.error} title="Couldn't load this MCP server" onRetry={() => void server.refetch()} />;
  return <Record server={server.data} catalog={catalog} />;
}

function Record({ server, catalog }: { server: McpServer; catalog: McpCatalogScope }) {
  const { scope, canManage } = catalog;
  const teamOwned = isTeamOwned(scope, server);
  const qc = useQueryClient();
  const go = useMcpNavigate(scope);
  const refresh = useRefreshTools(scope, server);
  const [deleting, setDeleting] = useState<McpServer | null>(null);
  const del = useMutation({
    mutationFn: async () => deleteMcpServer(scope, server.id),
    onSuccess: () => {
      setDeleting(null);
      toast.success(`${server.name} was deleted`, "Its headers were deleted with it.");
      void go.list().then(() => invalidateMcp(qc, scope));
    },
  });
  const remove = () => setDeleting(server);
  return (
    <>
      <DetailPage
        title={server.name}
        meta={
          <>
            <McpStatus server={server} /> {teamOwned ? null : <AccessBadge mode={server.accessMode} />}
          </>
        }
        facts={[
          // In Admin: whose it is (a team's own, or the platform's).
          ...(scope.kind === "admin" ? [{ label: "Owner", value: <OwnerCell owner={server.team} /> }] : []),
          { label: "Tools", value: toolCount(server) },
          { label: teamOwned ? "Available to" : "Published to", value: teamOwned ? teamOnly(server) : mcpPublishedTo(server) },
          { label: "Slug", value: <code>{server.slug}</code> },
          { label: "URL", value: <code>{server.url}</code> },
        ]}
        menuActions={[
          { label: "Refresh tools", icon: <RefreshCw aria-hidden />, hidden: !canManage, disabled: refresh.isPending, onSelect: () => refresh.mutate() },
          { label: "Delete MCP server…", icon: <Trash2 aria-hidden />, danger: true, hidden: !canManage, onSelect: remove },
        ]}
        notices={
          !server.enabled && (
            <Alert tone="warning" title="Turned off">
              Templates can&apos;t pick it, and agents don&apos;t get its tools until it&apos;s turned on again.
            </Alert>
          )
        }
        tabIds={mcpServerTabs}
        tabsLabel="MCP server sections"
        tabs={[
          { value: "tools", label: "Tools", icon: <Wrench aria-hidden />, count: server.tools.length, content: <McpToolsTab scope={scope} server={server} canManage={canManage} /> },
          { value: "settings", label: "Settings", icon: <Settings2 aria-hidden />, content: <McpSettingsTab catalog={catalog} server={server} onDelete={remove} /> },
        ]}
      />
      <ConfirmMutationDialog
        target={deleting}
        onClose={() => setDeleting(null)}
        mutation={del}
        onConfirm={() => del.mutate()}
        title={`Delete ${server.name}?`}
        description="Its headers are deleted with it. A server that template versions use can't be deleted: turn it off instead."
        confirmLabel="Delete MCP server"
      />
    </>
  );
}
