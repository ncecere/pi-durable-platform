/*
 * Admin → Users: everyone who has signed in, as a ListPage with role and
 * status facets and search in the URL (filtered by the server), a Teams count
 * and a relative last sign-in.
 */
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Users } from "lucide-react";
import { api, unwrap, type Schemas } from "@/api/client";
import { PersonCell } from "@/components/person-cell";
import { ListPage, timeColumn, useListFilters } from "@/components/templates/list-page";
import { Badge } from "@/components/ui/badge/badge";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import type { Facet } from "@/components/ui/filter-bar/filter-bar";
import { TextLink } from "@/components/ui/text-link/text-link";
import s from "../../shared.module.css";
import { one, useDebounced } from "../hooks";
import { platformRoleLabels, UserStatusBadge } from "./common";

type User = Schemas["User"];

const facets: Facet<User>[] = [
  {
    id: "role",
    label: "Platform role",
    type: "toggle",
    allLabel: "All",
    options: (Object.keys(platformRoleLabels) as Schemas["PlatformRole"][]).map((v) => ({ value: v, label: v === "none" ? "No role" : platformRoleLabels[v] })),
  },
  {
    id: "status",
    label: "Status",
    type: "toggle",
    allLabel: "All",
    options: [
      { value: "active", label: "Active" },
      { value: "suspended", label: "Suspended" },
    ],
  },
];

const columns: DataTableColumn<User>[] = [
  {
    id: "name",
    header: "Name",
    accessor: (u) => u.displayName || u.email,
    rowHeader: true,
    hideable: false,
    cell: (u) => (
      <PersonCell name={u.displayName || u.email}>
        <CellText primary={<TextLink render={<Link to="/admin/users/$userId" params={{ userId: u.id }} />}>{u.displayName || u.email}</TextLink>} secondary={u.email} />
      </PersonCell>
    ),
  },
  {
    id: "role",
    header: "Platform role",
    accessor: (u) => platformRoleLabels[u.platformRole],
    cell: (u) => (u.platformRole === "none" ? <span className={s.muted}>—</span> : <Badge tone="info">{platformRoleLabels[u.platformRole]}</Badge>),
  },
  { id: "status", header: "Status", accessor: "status", cell: (u) => <UserStatusBadge status={u.status} /> },
  { id: "teams", header: "Teams", accessor: (u) => u.teamCount ?? 0, numeric: true },
  { ...timeColumn<User>("lastLoginAt", "Last sign-in", (u) => u.lastLoginAt), sortable: false },
  { ...timeColumn<User>("createdAt", "First sign-in", (u) => u.createdAt), sortable: false, defaultHidden: true },
];

export function AdminUsersPage() {
  const { values, query } = useListFilters(facets);
  const q = useDebounced(query.trim());
  const role = one(values, "role") as Schemas["PlatformRole"] | "";
  const status = one(values, "status") as Schemas["UserStatus"] | "";
  const users = useInfiniteQuery({
    queryKey: ["admin", "users", q, role, status],
    queryFn: async ({ pageParam }) =>
      unwrap(await api.GET("/v1/admin/users", { params: { query: { q: q || undefined, role: role || undefined, status: status || undefined, cursor: pageParam, limit: 50 } } })),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined,
  });
  const items = users.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <ListPage<User>
      id="admin-users"
      title="Users"
      description="Everyone who has signed in. Suspending someone signs them out everywhere."
      caption="Users"
      columns={columns}
      data={items}
      getRowId={(u) => u.id}
      rowLabel={(u) => u.displayName || u.email}
      facets={facets}
      search={{ label: "Search users", placeholder: "Name or email" }}
      manual
      loading={users.isLoading}
      error={users.error}
      onRetry={() => void users.refetch()}
      empty={{ icon: <Users />, title: q || role || status ? "No users match." : "No users yet." }}
      tableProps={{ facetCounts: false, loadMore: { hasMore: Boolean(users.hasNextPage), loading: users.isFetchingNextPage, onLoadMore: () => void users.fetchNextPage() } }}
    />
  );
}
