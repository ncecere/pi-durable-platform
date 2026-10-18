/* A server's Tools tab: the tools the platform read from it, when, and why it couldn't the last time; those who manage it read them again. */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { RefreshCw, Wrench } from "lucide-react";
import { errorMessage } from "@/api/client";
import { invalidateMcp, type McpScope, type McpServer, mcpServerQuery, type McpTool, refreshMcpServer } from "@/api/mcp";
import { ListPage, RelativeTime } from "@/components/templates/list-page";
import { Alert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import type { DataTableColumn } from "@/components/ui/data-table/data-table";
import { Stack } from "@/components/ui/layout/layout";
import { toast } from "@/components/ui/toast/toast";
import s from "../../shared.module.css";
import x from "./mcp.module.css";

/** POST …/refresh: reads the server's tools again and stores the result. */
export function useRefreshTools(scope: McpScope, server: McpServer) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => refreshMcpServer(scope, server.id),
    onSuccess: (updated) => {
      qc.setQueryData(mcpServerQuery(scope, updated.id).queryKey, updated);
      void invalidateMcp(qc, scope);
      if (updated.toolsError) toast.warning("Couldn't read the tools", updated.toolsError);
      else toast.success("Tools refreshed", `${updated.name} offers ${updated.tools.length === 1 ? "1 tool" : `${updated.tools.length} tools`}.`);
    },
    onError: (err) => toast.error("Couldn't refresh the tools", errorMessage(err)),
  });
}

const columns: DataTableColumn<McpTool>[] = [
  { id: "name", header: "Tool", accessor: (t) => t.name, rowHeader: true, hideable: false, sortable: true, cell: (t) => <span className={s.mono}>{t.name}</span> },
  { id: "title", header: "Title", accessor: (t) => t.title ?? "", sortable: true, defaultHiddenNarrow: true, cell: (t) => t.title || "—" },
  {
    id: "description",
    header: "Description",
    accessor: (t) => t.description ?? "",
    cell: (t) =>
      t.description ? (
        <span className={x.toolDescription} title={t.description}>
          {t.description}
        </span>
      ) : (
        "—"
      ),
  },
];

type Props = { scope: McpScope; server: McpServer; canManage: boolean };

export function McpToolsTab({ scope, server, canManage }: Props) {
  const refresh = useRefreshTools(scope, server);
  const button = (
    <Button size="sm" variant={server.toolsError ? "primary" : "secondary"} loading={refresh.isPending} onClick={() => refresh.mutate()}>
      <RefreshCw aria-hidden /> Refresh tools
    </Button>
  );
  const read = server.toolsRefreshedAt ? (
    <>
      Last read <RelativeTime value={server.toolsRefreshedAt} />. Agents call them as <span className={s.mono}>mcp__{server.slug}__&lt;tool&gt;</span>.
    </>
  ) : (
    "The tools haven't been read yet."
  );
  return (
    <Stack gap={4}>
      {server.toolsError ? (
        <Alert tone="warning" title="The tools couldn't be read the last time" actions={canManage ? button : undefined}>
          {server.toolsError}
          {canManage ? " Check the URL and headers in Settings, then refresh the tools." : null}
          {server.tools.length ? " Agents still see the tools read before." : null}
        </Alert>
      ) : null}
      <Card title="Tools" description={read} actions={canManage && !server.toolsError ? button : undefined}>
        <ListPage<McpTool>
          id="admin-mcp-tools"
          caption={`Tools of ${server.name}`}
          columns={columns}
          data={server.tools}
          getRowId={(t) => t.name}
          rowLabel={(t) => t.name}
          search={server.tools.length > 10 ? { label: "Search tools", placeholder: "Name or description" } : undefined}
          empty={{ icon: <Wrench />, title: "No tools.", description: server.toolsError ? "Fix the problem above, then refresh the tools." : "The server listed no tools." }}
          tableProps={{ columnsMenuMin: 5 }}
        />
      </Card>
    </Stack>
  );
}
