/*
 * Admin → Teams: every team as a ListPage (status facet and search in the
 * URL, filtered by the server) with its members and owners, so a team
 * without an owner stands out. Platform admins create teams here.
 */
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Plus, Settings2, Users, UsersRound } from "lucide-react";
import { useState } from "react";
import { api, unwrap, type Schemas } from "@/api/client";
import { PersonCell } from "@/components/person-cell";
import { ListPage, timeColumn, useListFilters } from "@/components/templates/list-page";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import type { Facet } from "@/components/ui/filter-bar/filter-bar";
import { TextLink } from "@/components/ui/text-link/text-link";
import { useIntent } from "@/lib/intents";
import s from "../../shared.module.css";
import { one, useDebounced, useIsPlatformAdmin } from "../hooks";
import { TeamStatusBadge } from "./common";
import { CreateTeamDialog } from "./create-team-dialog";

type Summary = Schemas["TeamSummary"];

const facets: Facet<Summary>[] = [
  {
    id: "status",
    label: "Status",
    type: "toggle",
    allLabel: "All",
    options: [
      { value: "active", label: "Active" },
      { value: "archived", label: "Archived" },
    ],
  },
];

const columns: DataTableColumn<Summary>[] = [
  {
    id: "team",
    header: "Team",
    accessor: (r) => r.team.name,
    rowHeader: true,
    hideable: false,
    cell: ({ team }) => (
      <PersonCell name={team.name} shape="square">
        <CellText
          primary={<TextLink render={<Link to="/admin/teams/$team" params={{ team: team.slug }} />}>{team.name}</TextLink>}
          secondary={<span className={s.mono}>{team.slug}</span>}
        />
      </PersonCell>
    ),
  },
  {
    id: "members",
    header: "Members",
    accessor: "memberCount",
    numeric: true,
    cell: (r) => (
      <>
        {r.memberCount}
        {/* An owner invited by email who hasn't signed in yet isn't "No owner". */}
        {r.ownerCount === 0 && (
          <span className={s.secondary}>
            <Badge tone={r.ownerInvites > 0 ? "info" : "warning"} size="sm">
              {r.ownerInvites > 0 ? "Owner invited" : "No owner"}
            </Badge>
          </span>
        )}
      </>
    ),
  },
  { id: "owners", header: "Owners", accessor: "ownerCount", numeric: true },
  { id: "status", header: "Status", accessor: (r) => r.team.status, cell: (r) => <TeamStatusBadge status={r.team.status} /> },
  { ...timeColumn<Summary>("createdAt", "Created", (r) => r.team.createdAt), sortable: false, defaultHidden: true },
];

export function AdminTeamsPage() {
  const isAdmin = useIsPlatformAdmin();
  const [creating, setCreating] = useState(false);
  useIntent("create-team", () => isAdmin && setCreating(true));
  const { values, query } = useListFilters(facets);
  const q = useDebounced(query.trim());
  const status = one(values, "status") as Schemas["TeamStatus"] | "";
  const teams = useInfiniteQuery({
    queryKey: ["admin", "teams", q, status],
    queryFn: async ({ pageParam }) => unwrap(await api.GET("/v1/admin/teams", { params: { query: { q: q || undefined, status: status || undefined, cursor: pageParam, limit: 50 } } })),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined,
  });
  const items = teams.data?.pages.flatMap((p) => p.items) ?? [];
  const create = isAdmin && (
    <Button onClick={() => setCreating(true)}>
      <Plus aria-hidden /> Create team
    </Button>
  );
  return (
    <>
      <ListPage<Summary>
        id="admin-teams"
        title="Teams"
        description="Teams own agents and service accounts, and decide who may use them. Only platform admins create teams."
        primaryAction={create}
        caption="Teams"
        columns={columns}
        data={items}
        getRowId={(r) => r.team.id}
        rowLabel={(r) => r.team.name}
        facets={facets}
        search={{ label: "Search teams", placeholder: "Name or slug" }}
        manual
        loading={teams.isLoading}
        error={teams.error}
        onRetry={() => void teams.refetch()}
        rowActions={(r) => [
          { label: "Open", icon: <Users aria-hidden />, render: <Link to="/admin/teams/$team" params={{ team: r.team.slug }} /> },
          { label: "Members", render: <Link to="/admin/teams/$team" params={{ team: r.team.slug }} search={{ tab: "members" }} /> },
          { label: "Settings", icon: <Settings2 aria-hidden />, render: <Link to="/admin/teams/$team" params={{ team: r.team.slug }} search={{ tab: "settings" }} /> },
        ]}
        empty={{ icon: <UsersRound />, title: q || status ? "No teams match." : "No teams yet.", action: create || undefined }}
        tableProps={{ facetCounts: false, loadMore: { hasMore: Boolean(teams.hasNextPage), loading: teams.isFetchingNextPage, onLoadMore: () => void teams.fetchNextPage() } }}
      />
      <CreateTeamDialog open={creating} onOpenChange={setCreating} />
    </>
  );
}
