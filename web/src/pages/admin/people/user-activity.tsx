/*
 * Admin user › Activity: the changes a person made, from the audit
 * log. Sign-ins are hidden by default (a one-line summary says when they
 * last signed in instead); there's no Who column since it's always them.
 */
import { useInfiniteQuery } from "@tanstack/react-query";
import { FileClock } from "lucide-react";
import { api, unwrap, type Schemas } from "@/api/client";
import { actionLabel, targetTypeLabel } from "@/components/audit/labels";
import { AuditTarget } from "@/components/audit/target";
import { ListPage, timeColumn } from "@/components/templates/list-page";
import { useRecordParam } from "@/components/templates/record-page";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import { Switch } from "@/components/ui/switch/switch";
import { useSearchParams } from "@/lib/url-search";
import s from "../../shared.module.css";
import { AuditEntryPage } from "../audit/audit-record";

type Entry = Schemas["AuditEntry"];
const SIGNINS = "signins";

const columns: DataTableColumn<Entry>[] = [
  { ...timeColumn<Entry>("occurredAt", "When", (e) => e.occurredAt), sortable: false },
  {
    id: "action",
    header: "Action",
    accessor: (e) => actionLabel(e.action, e),
    rowHeader: true,
    cell: (e) => <CellText primary={actionLabel(e.action, e)} secondary={<span className={s.mono}>{e.action}</span>} />,
  },
  {
    id: "target",
    header: "Target",
    accessor: (e) => e.targetLabel ?? targetTypeLabel(e.targetType),
    cell: (e) => (e.action.startsWith("auth.") ? <span className={s.muted}>—</span> : <AuditTarget entry={e} scope={{ kind: "platform" }} />),
  },
];

export function UserActivity({ user }: { user: Schemas["User"] }) {
  const [params, setParams] = useSearchParams();
  const show = params.get(SIGNINS) === "show";
  const record = useRecordParam();
  const log = useInfiniteQuery({
    queryKey: ["admin", "audit", "user", user.id, show],
    queryFn: async ({ pageParam }) =>
      unwrap(await api.GET("/v1/admin/audit", { params: { query: { actorUserId: user.id, excludeAction: show ? undefined : "auth.", cursor: pageParam, limit: 50 } } })),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined,
  });
  const items = log.data?.pages.flatMap((p) => p.items) ?? [];
  const open = items.find((e) => String(e.id) === record.id);
  return (
    <>
      <ListPage<Entry>
        id="admin-user-activity"
        caption={`Changes by ${user.displayName || user.email}`}
        columns={columns}
        data={items}
        getRowId={(e) => String(e.id)}
        rowLabel={(e) => actionLabel(e.action, e)}
        manual
        loading={log.isLoading}
        error={log.error}
        onRetry={() => void log.refetch()}
        onRowClick={(e) => record.open(String(e.id))}
        empty={{ icon: <FileClock />, title: show ? "No activity recorded." : "No changes recorded.", description: show ? undefined : "Sign-ins are hidden." }}
        tableProps={{
          loadMore: { hasMore: Boolean(log.hasNextPage), loading: log.isFetchingNextPage, onLoadMore: () => void log.fetchNextPage() },
          toolbar: (
            <Switch
              label="Hide sign-ins"
              checked={!show}
              onCheckedChange={(hide) =>
                setParams((p) => {
                  const out = new URLSearchParams(p);
                  if (hide) out.delete(SIGNINS);
                  else out.set(SIGNINS, "show");
                  return out;
                })
              }
            />
          ),
        }}
      />
      <AuditEntryPage id={record.id} listed={open} onClose={record.close} />
    </>
  );
}
