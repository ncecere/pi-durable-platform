/*
 * Connected apps: the apps a person allowed to act as them with OAuth
 * Connect (one per app), with the host that vouches for the app (or
 * "Unverified" for one that registered itself), when it was connected and
 * last used, and Disconnect (asked first; its tokens stop at once).
 * Account › Connected apps shows the person's own; Admin › Users › person
 * shows anyone's, where platform admins disconnect and auditors read.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plug, Unplug } from "lucide-react";
import { type ReactNode, useState } from "react";
import { grantsKey, type GrantsOwner, grantsQuery, type OAuthGrant, revokeGrant } from "@/api/oauth";
import type { DataTableColumn } from "@/components/ui/data-table/data-table";
import { toast } from "@/components/ui/toast/toast";
import s from "../../pages/shared.module.css";
import { ConfirmMutationDialog } from "../confirm-dialog";
import { ListPage, RelativeTime, timeColumn } from "../templates/list-page";
import { AppCell } from "./client-info";

function useDisconnect(owner: GrantsOwner, onDone: () => void) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (g: OAuthGrant) => revokeGrant(owner, g.id),
    onSuccess: (_, g) => {
      onDone();
      toast.success(`${g.client.name} was disconnected`);
    },
    onSettled: () => void qc.invalidateQueries({ queryKey: grantsKey(owner) }),
  });
}

const columns: DataTableColumn<OAuthGrant>[] = [
  {
    id: "client",
    header: "App",
    accessor: (g) => g.client.name,
    rowHeader: true,
    hideable: false,
    sortable: true,
    cell: (g) => <AppCell client={g.client} />,
  },
  timeColumn<OAuthGrant>("createdAt", "Connected", (g) => g.createdAt),
  {
    ...timeColumn<OAuthGrant>("lastUsedAt", "Last used", (g) => g.lastUsedAt),
    cell: (g) =>
      g.lastUsedAt ? (
        <span className={s.nowrap}>
          <RelativeTime value={g.lastUsedAt} />
        </span>
      ) : (
        <span className={s.muted}>Not used yet</span>
      ),
  },
];

type Props = {
  owner: GrantsOwner;
  canDisconnect: boolean;
  /** A page of its own (Account › Connected apps): its title and description. Without one, only the list. */
  title?: string;
  description?: ReactNode;
  /** Alerts under the page's header. */
  notices?: ReactNode;
};

export function ConnectedApps({ owner, canDisconnect, title, description, notices }: Props) {
  const grants = useQuery(grantsQuery(owner));
  const [target, setTarget] = useState<OAuthGrant | null>(null);
  const disconnect = useDisconnect(owner, () => setTarget(null));
  const who = owner.self ? "you" : owner.name;
  return (
    <>
      <ListPage<OAuthGrant>
        id={owner.self ? "my-connected-apps" : "admin-user-connected-apps"}
        title={title}
        description={description}
        notices={notices}
        caption={owner.self ? "Your connected apps" : `Apps ${owner.name} connected`}
        columns={columns}
        data={grants.data ?? []}
        getRowId={(g) => g.id}
        rowLabel={(g) => g.client.name}
        loading={grants.isLoading}
        error={grants.error}
        onRetry={() => void grants.refetch()}
        rowActions={canDisconnect ? (g) => [{ label: "Disconnect…", icon: <Unplug aria-hidden />, danger: true, onSelect: () => setTarget(g) }] : undefined}
        empty={{
          icon: <Plug />,
          title: "No connected apps.",
          description: `Apps ${owner.self ? "you connect" : `${owner.name} connects`} with ${owner.self ? "your" : "their"} platform account, such as a chat app, are listed here. Each one acts as ${who}, with ${owner.self ? "your" : "their"} access to agents.`,
        }}
      />
      <ConfirmMutationDialog
        target={target}
        onClose={() => setTarget(null)}
        mutation={disconnect}
        onConfirm={(g) => disconnect.mutate(g)}
        title={`Disconnect ${target?.client.name ?? "this app"}?`}
        description={`It can't act as ${who} any more: its sign-in stops working on its next request. ${
          owner.self ? "To use it again, connect it again and allow it." : `${owner.name} can connect it again by signing in and allowing it.`
        }`}
        confirmLabel="Disconnect"
      />
    </>
  );
}
