/*
 * Admin › MCP servers: the tool servers agents can be given (platform admins
 * add them here), with their tools and who may attach them. Auditors read it.
 */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Plus, Unplug } from "lucide-react";
import { useState } from "react";
import { type McpServer, mcpServersQuery } from "@/api/mcp";
import { EnabledBadge } from "@/components/enabled-badge";
import { ListPage } from "@/components/templates/list-page";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import { TextLink } from "@/components/ui/text-link/text-link";
import { cx } from "@/lib/bitop-utils";
import s from "../../shared.module.css";
import { useIsPlatformAdmin } from "../hooks";
import { AccessBadge } from "../models/list";
import { AddMcpServerDialog } from "./add-server-dialog";
import { mcpPublishedTo, toolCount, whatIsAnMcpServer } from "./labels";
import x from "./mcp.module.css";

const serverLink = (serverId: string, search: Record<string, string> = {}) => <Link to="/admin/mcp-servers/$serverId" params={{ serverId }} search={search} />;

/** Enabled or Disabled, and a warning when the tools couldn't be read the last time. */
export function McpStatus({ server }: { server: McpServer }) {
  return (
    <span className={x.badges}>
      <EnabledBadge enabled={server.enabled} />
      {server.toolsError ? (
        <Badge tone="warning" title={server.toolsError}>
          Tools couldn&apos;t be read
        </Badge>
      ) : null}
    </span>
  );
}

const columns: DataTableColumn<McpServer>[] = [
  {
    id: "name",
    header: "Server",
    accessor: (m) => m.name,
    rowHeader: true,
    hideable: false,
    sortable: true,
    cell: (m) => <CellText className={s.compactCell} wrap="anywhere" primary={<TextLink render={serverLink(m.id)}>{m.name}</TextLink>} secondary={<span className={s.mono}>{m.slug}</span>} />,
  },
  {
    id: "url",
    header: "URL",
    accessor: (m) => m.url,
    defaultHiddenNarrow: true,
    cell: (m) => (
      <span className={cx(s.mono, s.truncate)} title={m.url}>
        {m.url}
      </span>
    ),
  },
  { id: "access", header: "Access", accessor: (m) => m.accessMode, sortable: true, defaultHidden: true, cell: (m) => <AccessBadge mode={m.accessMode} /> },
  { id: "tools", header: "Tools", accessor: (m) => m.tools.length, numeric: true, sortable: true, cell: toolCount },
  { id: "published", header: "Published to", accessor: mcpPublishedTo, sortable: true },
  { id: "status", header: "Status", accessor: (m) => (m.toolsError ? "Error" : m.enabled ? "Enabled" : "Disabled"), sortable: true, cell: (m) => <McpStatus server={m} /> },
];

/** /admin/mcp-servers */
export function AdminMcpServersPage() {
  const isAdmin = useIsPlatformAdmin();
  const servers = useQuery(mcpServersQuery());
  const [adding, setAdding] = useState(false);
  const add = isAdmin && (
    <Button onClick={() => setAdding(true)}>
      <Plus aria-hidden /> Add MCP server
    </Button>
  );
  return (
    <>
      <ListPage<McpServer>
        id="admin-mcp-servers"
        title="MCP servers"
        description="Tool servers agents can call. Publish each one to every team or to chosen teams; templates pick which servers and tools their agents get."
        primaryAction={add}
        caption="MCP servers"
        columns={columns}
        data={servers.data ?? []}
        getRowId={(m) => m.id}
        rowLabel={(m) => m.name}
        search={{ label: "Search MCP servers", placeholder: "Name or URL" }}
        loading={servers.isLoading}
        error={servers.error}
        onRetry={() => void servers.refetch()}
        rowActions={(m) => [
          { label: "Open", render: serverLink(m.id) },
          { label: "Settings", render: serverLink(m.id, { tab: "settings" }) },
        ]}
        empty={{ icon: <Unplug />, title: "No MCP servers yet.", description: whatIsAnMcpServer, action: add || undefined }}
        tableProps={{ columnsMenuMin: 5 }}
      />
      {adding && <AddMcpServerDialog onClose={() => setAdding(false)} />}
    </>
  );
}
