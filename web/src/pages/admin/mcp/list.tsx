/*
 * Admin › MCP servers: the tool servers agents can be given (platform admins
 * add the platform's here; teams may add their own, shown with their Owner),
 * with their tools and who may attach them. The Owner filter (?owner=)
 * narrows the list on the server. Auditors read it.
 */
import { useQuery } from "@tanstack/react-query";
import { Plus, Unplug } from "lucide-react";
import { useState } from "react";
import { type McpServer, mcpServersQuery } from "@/api/mcp";
import { adminScope } from "@/api/models";
import { ListPage } from "@/components/templates/list-page";
import { Button } from "@/components/ui/button/button";
import { useIsPlatformAdmin } from "../hooks";
import { OwnerSelect, useOwnerParam } from "../models/owner-filter";
import { AddMcpServerDialog } from "./add-server-dialog";
import { mcpColumns } from "./columns";
import { whatIsAnMcpServer } from "./labels";
import { serverLinkFor } from "./scope";

export { McpStatus } from "./columns";

const serverLink = serverLinkFor(adminScope);
const columns = mcpColumns(serverLink, { owner: true });

/** The Owner filter: the teams that own a server, from the unfiltered list. */
function McpOwnerFilter({ value, onChange }: { value: string; onChange: (owner: string) => void }) {
  const every = useQuery(mcpServersQuery(adminScope, "all"));
  return <OwnerSelect value={value} onChange={onChange} owners={(every.data ?? []).map((m) => m.team)} />;
}

/** /admin/mcp-servers */
export function AdminMcpServersPage() {
  const isAdmin = useIsPlatformAdmin();
  const [owner, setOwner] = useOwnerParam();
  const servers = useQuery(mcpServersQuery(adminScope, owner));
  const [adding, setAdding] = useState(false);
  const add = isAdmin && (
    <Button onClick={() => setAdding(true)}>
      <Plus aria-hidden /> Add MCP server
    </Button>
  );
  const empty =
    owner !== "all"
      ? { icon: <Unplug />, title: "None for this owner.", description: "Choose All owners to see every server." }
      : { icon: <Unplug />, title: "No MCP servers yet.", description: whatIsAnMcpServer, action: add || undefined };
  return (
    <>
      <ListPage<McpServer>
        id="admin-mcp-servers"
        title="MCP servers"
        description="Tool servers agents can call. Publish each platform server to every team or to chosen teams; templates pick which servers and tools their agents get. A team's own servers are for that team only."
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
        empty={empty}
        tableProps={{ columnsMenuMin: 5, toolbar: <McpOwnerFilter value={owner} onChange={setOwner} /> }}
      />
      {adding && <AddMcpServerDialog scope={adminScope} onClose={() => setAdding(false)} />}
    </>
  );
}
