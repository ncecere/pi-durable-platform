/*
 * The MCP server lists' columns. A page passes how a server is linked (Admin
 * opens /admin/mcp-servers/…, a team its own) and which columns it adds:
 * Admin's Owner (linking to Admin › Teams) and Published to, a team's Last
 * read; a team's own servers have no Access (proxy only). Team › MCP servers'
 * Available tab adds its Source column itself.
 */
import type { McpServer } from "@/api/mcp";
import { EnabledBadge } from "@/components/enabled-badge";
import { RelativeTime } from "@/components/templates/list-page";
import { Badge } from "@/components/ui/badge/badge";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import { TextLink } from "@/components/ui/text-link/text-link";
import { cx } from "@/lib/bitop-utils";
import s from "../../shared.module.css";
import { AccessBadge, OwnerCell } from "../models/columns";
import { ownerLabel } from "../models/labels";
import { mcpPublishedTo, teamOnly, toolCount } from "./labels";
import x from "./mcp.module.css";
import type { ServerLink } from "./scope";

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

type Options = {
  /** Link the name (false: plain text, e.g. a platform server in a team's list). */
  linked?: (m: McpServer) => boolean;
  /** Admin: who owns it. */
  owner?: boolean;
  /** Platform servers: how agents reach it (a team's are proxy only). */
  access?: boolean;
  /** Who may use it. */
  published?: boolean;
  /** When the tools were last read. */
  refreshed?: boolean;
};

export function mcpColumns(link: ServerLink, { linked = () => true, owner = false, access = true, published = true, refreshed = false }: Options = {}): DataTableColumn<McpServer>[] {
  const columns: (DataTableColumn<McpServer> | false)[] = [
    {
      id: "name",
      header: "Server",
      accessor: (m) => m.name,
      rowHeader: true,
      hideable: false,
      sortable: true,
      cell: (m) => (
        <CellText
          className={s.compactCell}
          wrap="anywhere"
          primary={linked(m) ? <TextLink render={link(m.id)}>{m.name}</TextLink> : m.name}
          secondary={<span className={s.mono}>{m.slug}</span>}
        />
      ),
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
    owner && { id: "owner", header: "Owner", accessor: (m) => ownerLabel(m.team), sortable: true, cell: (m) => <OwnerCell owner={m.team} /> },
    access && { id: "access", header: "Access", accessor: (m) => m.accessMode, sortable: true, defaultHidden: true, cell: (m) => <AccessBadge mode={m.accessMode} /> },
    { id: "tools", header: "Tools", accessor: (m) => m.tools.length, numeric: true, sortable: true, cell: toolCount },
    published && { id: "published", header: "Published to", accessor: (m) => (m.team ? teamOnly(m) : mcpPublishedTo(m)), sortable: true },
    refreshed && {
      id: "refreshed",
      header: "Last read",
      accessor: (m) => m.toolsRefreshedAt ?? "",
      sortable: true,
      defaultHiddenNarrow: true,
      cell: (m) => (m.toolsRefreshedAt ? <RelativeTime value={m.toolsRefreshedAt} /> : <span className={s.muted}>Never</span>),
    },
    { id: "status", header: "Status", accessor: (m) => (m.toolsError ? "Error" : m.enabled ? "Enabled" : "Disabled"), sortable: true, cell: (m) => <McpStatus server={m} /> },
  ];
  return columns.filter((c): c is DataTableColumn<McpServer> => Boolean(c));
}
