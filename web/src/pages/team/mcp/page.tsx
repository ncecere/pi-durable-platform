/*
 * Team › MCP servers (/teams/$team/mcp-servers): pill tabs Servers |
 * Available (?tab=). Servers: the team's own (proxy only, for this team
 * alone), with their tools and when they were last read; its owners and
 * admins add one while platform admins allow it (limits.canAddMcpServers),
 * else a note says why. Available: every server the team may use, its own and
 * the platform's published to it, with their source. Shared teams and
 * personal workspaces alike; other members and platform staff read.
 */
import { useQuery } from "@tanstack/react-query";
import { Plus, Unplug } from "lucide-react";
import { useState } from "react";
import { type McpServer, mcpServersQuery, teamMcpServersQuery } from "@/api/mcp";
import { teamScope } from "@/api/models";
import { workspaceLimitsQuery } from "@/api/personal";
import { McpCatalogTabs } from "@/components/mcp/catalog-tabs";
import { Alert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import type { DataTableColumn } from "@/components/ui/data-table/data-table";
import { AddMcpServerDialog } from "../../admin/mcp/add-server-dialog";
import { mcpColumns } from "../../admin/mcp/columns";
import { serverLinkFor } from "../../admin/mcp/scope";
import s from "../../shared.module.css";
import { ArchivedNotice } from "../layout";
import { useTeam } from "../common";
import { cannotAddReason, managesMcpServers, readOnlyNote } from "./access";

/** Where an available server comes from: the team's own, or the platform's. */
const sourceColumn = (own: string): DataTableColumn<McpServer> => ({
  id: "source",
  header: "Source",
  accessor: (m) => (m.team ? own : "Platform"),
  sortable: true,
  cell: (m) => (m.team ? <Badge tone="info">{own}</Badge> : <span className={s.muted}>Platform</span>),
});

/** /teams/$team/mcp-servers */
export function TeamMcpServersPage() {
  const ctx = useTeam();
  const { slug, team, personal } = ctx;
  const scope = teamScope(slug);
  const link = serverLinkFor(scope);
  const servers = useQuery(mcpServersQuery(scope));
  const available = useQuery(teamMcpServersQuery(slug));
  const limits = useQuery(workspaceLimitsQuery(slug));
  const [adding, setAdding] = useState(false);
  const manages = managesMcpServers(ctx);
  const canAdd = manages && limits.data?.canAddMcpServers === true;
  const where = personal ? "this workspace" : "this team";
  const own = personal ? "This workspace" : "This team";
  const add = canAdd && (
    <Button onClick={() => setAdding(true)}>
      <Plus aria-hidden /> Add server
    </Button>
  );
  // Why there's no Add server: turned off by platform admins, or not this person's to change.
  const note = canAdd ? null : manages ? (
    limits.data && (
      <Alert tone="info" title="Adding MCP servers is turned off">
        {cannotAddReason(ctx)}
      </Alert>
    )
  ) : (
    <Alert tone="info">{readOnlyNote(ctx)}</Alert>
  );
  const availableColumns = mcpColumns(link, { linked: (m) => Boolean(m.team), access: false, published: false });
  availableColumns.splice(2, 0, sourceColumn(own));
  return (
    <McpCatalogTabs
      title="MCP servers"
      description={`Tool servers ${team.name}'s templates can give their agents: the platform's, published to ${where}, and ${where}'s own, which only ${where} can use.`}
      actions={{ servers: add || undefined }}
      notices={<ArchivedNotice />}
      tabNotices={{ servers: note }}
      servers={{
        id: "team-mcp-servers",
        caption: "MCP servers",
        columns: mcpColumns(link, { access: false, published: false, refreshed: true }),
        data: servers.data ?? [],
        getRowId: (m) => m.id,
        rowLabel: (m) => m.name,
        search: { label: "Search MCP servers", placeholder: "Name or URL" },
        loading: servers.isLoading,
        error: servers.error,
        onRetry: () => void servers.refetch(),
        rowActions: (m) => [
          { label: "Open", render: link(m.id) },
          { label: "Settings", render: link(m.id, { tab: "settings" }) },
        ],
        empty: {
          icon: <Unplug />,
          title: `No MCP servers of ${where}'s own.`,
          description: `A server of its own gives ${where}'s agents more tools, for example one with the team's own token. Agents reach it through the platform, and only ${where} can use it.`,
          action: add || undefined,
        },
        tableProps: { columnsMenuMin: 5 },
      }}
      available={{
        id: "team-mcp-available",
        caption: "MCP servers the team may use",
        columns: availableColumns,
        data: available.data ?? [],
        getRowId: (m) => m.id,
        rowLabel: (m) => m.name,
        search: { label: "Search MCP servers", placeholder: "Name or URL" },
        loading: available.isLoading,
        error: available.error,
        onRetry: () => void available.refetch(),
        rowActions: (m) => [{ label: "Open", hidden: !m.team, render: link(m.id) }],
        empty: { icon: <Unplug />, title: "No MCP servers yet.", description: `Platform admins publish MCP servers to ${where}; ${where}'s owners and admins can add servers of its own.` },
        tableProps: { columnsMenuMin: 5 },
      }}
    >
      {adding && <AddMcpServerDialog scope={scope} onClose={() => setAdding(false)} />}
    </McpCatalogTabs>
  );
}
