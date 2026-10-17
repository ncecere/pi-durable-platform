/*
 * API keys (/settings/api-keys), from the account menu next to Profile: the
 * person's own keys with their status, last use and expiry (revoked and
 * expired ones stay listed for 30 days). Create shows the secret once
 * (create-key-dialog.tsx); Revoke asks first and takes effect at once.
 * Expiry reads in days ("in 7 days", never "next week"), the exact date as
 * its tooltip; a revoked key has none (its status says so).
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { api, unwrap, type Schemas } from "../../api/client";
import { myApiKeysQuery } from "../../api/queries";
import { ConfirmMutationDialog } from "../../components/confirm-dialog";
import { ListPage, RelativeTime, timeColumn } from "../../components/templates/list-page";
import { formatDate, toDate } from "../../lib/bitop-format";
import { expiryText } from "../../lib/expiry";
import { useIntent } from "../../lib/intents";
import { terms } from "../../lib/terms";
import { Alert } from "@/components/ui/alert/alert";
import { StatusBadge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import type { Facet } from "@/components/ui/filter-bar/filter-bar";
import { toast } from "@/components/ui/toast/toast";
import s from "../shared.module.css";
import { CreateKeyDialog } from "./create-key-dialog";

export type ApiKey = Schemas["ApiKey"];

const statusLabels: Record<ApiKey["status"], string> = { active: "Active", expired: "Expired", revoked: "Revoked" };
const statusTone = { active: "success", expired: "neutral", revoked: "danger" } as const;

export { expiryText };

function Expiry({ value }: { value: string | null | undefined }) {
  const date = toDate(value);
  if (!date) return <span className={s.muted}>—</span>;
  return (
    <time className={s.nowrap} dateTime={date.toISOString()} title={formatDate(date, { style: "long" })}>
      {expiryText(value)}
    </time>
  );
}

const facets: Facet<ApiKey>[] = [
  {
    id: "status",
    label: "Status",
    type: "toggle",
    allLabel: "All",
    accessor: (k) => k.status,
    options: (["active", "expired", "revoked"] as const).map((v) => ({ value: v, label: statusLabels[v] })),
  },
];

const columns: DataTableColumn<ApiKey>[] = [
  { id: "name", header: "Name", accessor: "name", rowHeader: true, hideable: false, sortable: true, cell: (k) => <CellText primary={k.name} secondary={<span className={s.mono}>{k.prefix}…</span>} /> },
  { id: "status", header: "Status", accessor: (k) => statusLabels[k.status], sortable: true, cell: (k) => <StatusBadge tone={statusTone[k.status]}>{statusLabels[k.status]}</StatusBadge> },
  { ...timeColumn<ApiKey>("lastUsedAt", "Last used", (k) => k.lastUsedAt), cell: (k) => (k.lastUsedAt ? <span className={s.nowrap}><RelativeTime value={k.lastUsedAt} /></span> : <span className={s.muted}>Never</span>) },
  {
    id: "expiresAt",
    header: "Expires",
    accessor: (k) => (k.expiresAt ? new Date(k.expiresAt) : null),
    sortable: true,
    cell: (k) => (k.status === "revoked" ? <span className={s.muted}>—</span> : <Expiry value={k.expiresAt} />),
  },
  { ...timeColumn<ApiKey>("createdAt", "Created", (k) => k.createdAt), defaultHiddenNarrow: true },
];

export function ApiKeysPage() {
  const qc = useQueryClient();
  const keys = useQuery(myApiKeysQuery());
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<ApiKey | null>(null);
  useIntent("create-api-key", () => setCreating(true));
  const revoke = useMutation({
    mutationFn: async (k: ApiKey) => unwrap(await api.DELETE("/v1/me/api-keys/{keyId}", { params: { path: { keyId: k.id } } })),
    onSuccess: (_, k) => {
      setRevoking(null);
      toast.success(`${k.name} was revoked`);
    },
    onSettled: () => void qc.invalidateQueries({ queryKey: myApiKeysQuery().queryKey }),
  });
  const create = (
    <Button onClick={() => setCreating(true)}>
      <Plus aria-hidden /> Create API key
    </Button>
  );
  return (
    <>
      <ListPage<ApiKey>
        id="my-api-keys"
        title={terms.apiKeys}
        description="Personal keys for your scripts and tools. A key acts as you, with your access."
        primaryAction={create}
        notices={
          <Alert tone="info">
            Send a key as <code>Authorization: Bearer &lt;key&gt;</code>. For CI and shared automation, use a team&apos;s service account. Keep keys secret,
            and revoke a key you no longer use.
          </Alert>
        }
        caption={terms.apiKeys}
        columns={columns}
        data={keys.data ?? []}
        getRowId={(k) => k.id}
        rowLabel={(k) => k.name}
        facets={facets}
        loading={keys.isLoading}
        error={keys.error}
        onRetry={() => void keys.refetch()}
        rowActions={(k) => [{ label: "Revoke…", icon: <Trash2 aria-hidden />, danger: true, hidden: k.status !== "active", onSelect: () => setRevoking(k) }]}
        empty={{ icon: <KeyRound />, title: "No API keys yet.", description: "Create a key for a script or tool that acts as you.", action: create }}
        tableProps={{ columnsMenuMin: 6 }}
      />
      {creating && <CreateKeyDialog onClose={() => setCreating(false)} />}
      <ConfirmMutationDialog
        target={revoking}
        onClose={() => setRevoking(null)}
        mutation={revoke}
        onConfirm={(k) => revoke.mutate(k)}
        title={`Revoke ${revoking?.name ?? "key"}?`}
        description="Clients and scripts using this key stop working on their next request. This can't be undone."
        confirmLabel="Revoke key"
      />
    </>
  );
}
