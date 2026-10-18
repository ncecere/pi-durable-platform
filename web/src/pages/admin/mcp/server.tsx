/*
 * Admin › MCP servers › one server: its tools (read again with Refresh
 * tools) and its settings. Platform admins change it; auditors read it.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";
import { RefreshCw, Settings2, Trash2, Wrench } from "lucide-react";
import { useState } from "react";
import { api, unwrap } from "@/api/client";
import { invalidateMcp, type McpServer, mcpServerQuery } from "@/api/mcp";
import { ConfirmMutationDialog } from "@/components/confirm-dialog";
import { isNotFound, NotFoundState } from "@/components/not-found";
import { PageSkeleton } from "@/components/page-skeleton";
import { DetailPage } from "@/components/templates/detail-page";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { toast } from "@/components/ui/toast/toast";
import { mcpServerTabs } from "@/lib/tabs";
import { useIsPlatformAdmin } from "../hooks";
import { AccessBadge } from "../models/list";
import { mcpPublishedTo, toolCount } from "./labels";
import { McpStatus } from "./list";
import { McpSettingsTab } from "./server-settings";
import { McpToolsTab, useRefreshTools } from "./server-tools";

export function AdminMcpServerPage() {
  const { serverId } = useParams({ from: "/app/admin/mcp-servers/$serverId" });
  const server = useQuery(mcpServerQuery(serverId));
  if (server.isLoading) return <PageSkeleton />;
  if (isNotFound(server.error)) return <NotFoundState what="object" />;
  if (!server.data) return <ErrorAlert error={server.error} title="Couldn't load this MCP server" onRetry={() => void server.refetch()} />;
  return <Record server={server.data} />;
}

function Record({ server }: { server: McpServer }) {
  const isAdmin = useIsPlatformAdmin();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const refresh = useRefreshTools(server);
  const [deleting, setDeleting] = useState<McpServer | null>(null);
  const del = useMutation({
    mutationFn: async () => unwrap(await api.DELETE("/v1/admin/mcp-servers/{serverId}", { params: { path: { serverId: server.id } } })),
    onSuccess: () => {
      setDeleting(null);
      toast.success(`${server.name} was deleted`, "Its headers were deleted with it.");
      void navigate({ to: "/admin/mcp-servers", replace: true }).then(() => invalidateMcp(qc));
    },
  });
  const remove = () => setDeleting(server);
  return (
    <>
      <DetailPage
        title={server.name}
        meta={
          <>
            <McpStatus server={server} /> <AccessBadge mode={server.accessMode} />
          </>
        }
        facts={[
          { label: "Tools", value: toolCount(server) },
          { label: "Published to", value: mcpPublishedTo(server) },
          { label: "Slug", value: <code>{server.slug}</code> },
          { label: "URL", value: <code>{server.url}</code> },
        ]}
        menuActions={[
          { label: "Refresh tools", icon: <RefreshCw aria-hidden />, hidden: !isAdmin, disabled: refresh.isPending, onSelect: () => refresh.mutate() },
          { label: "Delete MCP server…", icon: <Trash2 aria-hidden />, danger: true, hidden: !isAdmin, onSelect: remove },
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
          { value: "tools", label: "Tools", icon: <Wrench aria-hidden />, count: server.tools.length, content: <McpToolsTab server={server} isAdmin={isAdmin} /> },
          { value: "settings", label: "Settings", icon: <Settings2 aria-hidden />, content: <McpSettingsTab server={server} isAdmin={isAdmin} onDelete={remove} /> },
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
