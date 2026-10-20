/*
 * Admin › Settings › Trusted apps (OAuth Connect): the apps that skip the
 * approval screen. People still sign in, and a trusted app still acts only
 * as them, never with admin powers. Only apps identified by a Client ID
 * Metadata Document (an https client_id) can be trusted. Platform admins
 * edit the list as staged changes and save it at once (If-Match on its
 * revision; edits survive a change made elsewhere) after a confirmation
 * naming what's added and removed; auditors read.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { ApiError, api, ifMatch, unwrap } from "@/api/client";
import { appSecretsQuery, type TrustedClient, type TrustedClients, trustedClientsQuery } from "@/api/oauth";
import { ActionMenu } from "@/components/templates/action-menu";
import { useRevisionForm } from "@/components/templates/revision-form";
import { SettingsPage, SettingsSection } from "@/components/templates/settings-page";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { CellText } from "@/components/ui/data-table/data-table";
import { AlertDialog } from "@/components/ui/dialog/dialog";
import { SkeletonText } from "@/components/ui/skeleton/skeleton";
import { Table, Td, Tr } from "@/components/ui/table/table";
import { toast } from "@/components/ui/toast/toast";
import s from "../../shared.module.css";
import { useIsPlatformAdmin } from "../hooks";
import { TrustedAppDialog } from "./trusted-app-dialog";
import { appsKey, clientHost, maxApps, normalApp, refusedReason } from "./trusted-apps";

const description =
  "These apps skip the approval screen when people connect them. People still sign in, and a trusted app still acts only as them, never with admin powers. Only apps identified by a metadata document (an https client ID) can be trusted: an app that registered itself always asks, marked Unverified.";

/** The section, once the list has loaded. */
export function TrustedAppsSettings() {
  const settings = useQuery(trustedClientsQuery());
  if (settings.data) return <TrustedAppsForm saved={settings.data} />;
  return (
    <SettingsSection title="Trusted apps" description={description}>
      {settings.isLoading ? (
        <div role="status" aria-label="Loading…">
          <SkeletonText lines={3} />
        </div>
      ) : (
        <ErrorAlert error={settings.error} title="Couldn't load the trusted apps" onRetry={() => void settings.refetch()} />
      )}
    </SettingsSection>
  );
}

/** A 422 invalid_client_id as the row it's about. */
function appProblem(err: unknown): { index: number; message: string } | undefined {
  if (!(err instanceof ApiError) || err.code !== "invalid_client_id" || typeof err.details?.index !== "number") return undefined;
  return { index: err.details.index, message: refusedReason(err.details.reason) };
}

const nameOf = (c: TrustedClient) => c.label || c.clientId;

function TrustedAppsForm({ saved }: { saved: TrustedClients }) {
  const qc = useQueryClient();
  const isAdmin = useIsPlatformAdmin();
  const [editing, setEditing] = useState<number | "new" | null>(null);
  const [confirming, setConfirming] = useState(false);
  const base = { clients: saved.clients.map(normalApp) };
  const [form, setForm, revision] = useRevisionForm(base, saved.revision, { labels: { clients: "Trusted apps" } });
  const save = useMutation({
    mutationFn: async () => unwrap(await api.PUT("/v1/admin/settings/oauth-clients", { params: { header: ifMatch(saved.revision) }, body: { clients: form.clients } })),
    // A failure shows above the save bar, which also handles a change made elsewhere (412).
    onSettled: () => setConfirming(false),
    onSuccess: (updated) => {
      qc.setQueryData(trustedClientsQuery().queryKey, updated);
      // Removing an app deletes its secrets.
      void qc.invalidateQueries({ queryKey: appSecretsQuery().queryKey });
      toast.success("Trusted apps saved", "New connections from them skip the approval screen.");
    },
  });
  const apps = form.clients;
  const setApps = (next: TrustedClient[]) => {
    if (save.error) save.reset();
    setForm({ clients: next });
  };
  const ids = (list: TrustedClient[]) => new Set(list.map((c) => c.clientId));
  const added = apps.filter((c) => !ids(base.clients).has(c.clientId));
  const removed = base.clients.filter((c) => !ids(apps).has(c.clientId));
  const add = isAdmin && (
    <Button size="sm" variant="secondary" disabled={apps.length >= maxApps} onClick={() => setEditing("new")}>
      <Plus aria-hidden /> Add app
    </Button>
  );
  // The dialogs sit outside the settings form: a submit inside one must not reach it.
  return (
    <>
      <SettingsPage
        revision={revision}
        canEdit={isAdmin}
        readOnlyNote={null}
        dirty={appsKey(apps) !== appsKey(base.clients)}
        saving={save.isPending}
        error={save.error}
        saveLabel="Save trusted apps"
        onSave={() => setConfirming(true)}
        onDiscard={() => setApps(base.clients)}
      >
        <SettingsSection title="Trusted apps" description={`${description} At most ${maxApps}.`} actions={add || undefined}>
          <AppsTable
            apps={apps}
            empty={isAdmin ? "None: every app asks for approval. Add an app to let people connect it without the approval screen." : "None: every app asks for approval."}
            onEdit={isAdmin ? (i) => setEditing(i) : undefined}
            onRemove={isAdmin ? (i) => setApps(apps.filter((_, j) => j !== i)) : undefined}
            problem={appProblem(save.error)}
          />
        </SettingsSection>
      </SettingsPage>
      <AlertDialog
        open={confirming}
        onOpenChange={(o) => {
          if (!o) setConfirming(false);
        }}
        tone="primary"
        title="Save the trusted apps?"
        description={`${apps.length} ${apps.length === 1 ? "app" : "apps"}. People connecting a trusted app won't see the approval screen: they only sign in.`}
        confirmLabel="Save trusted apps"
        busy={save.isPending}
        onConfirm={() => save.mutate()}
      >
        {added.length > 0 && <p>Trusted from now on: {added.map(nameOf).join(", ")}.</p>}
        {removed.length > 0 && (
          <p>
            Asks for approval again, and its app secrets are deleted (trusting it again needs new ones): {removed.map(nameOf).join(", ")}.
          </p>
        )}
      </AlertDialog>
      {editing !== null && (
        <TrustedAppDialog
          app={editing === "new" ? undefined : apps[editing]}
          others={apps.filter((_, j) => j !== editing)}
          onClose={() => setEditing(null)}
          onSave={(c) => {
            setApps(editing === "new" ? [...apps, c] : apps.map((x, j) => (j === editing ? c : x)));
            setEditing(null);
          }}
        />
      )}
    </>
  );
}

type TableProps = {
  apps: TrustedClient[];
  empty: string;
  onEdit?: (index: number) => void;
  onRemove?: (index: number) => void;
  /** A server refusal for one app, shown on its row. */
  problem?: { index: number; message: string };
};

/** The trusted apps: client ID (with the host that vouches for it) and label, and for platform admins Edit and Remove. */
function AppsTable({ apps, empty, onEdit, onRemove, problem }: TableProps) {
  const editable = Boolean(onEdit && onRemove);
  return (
    <Table
      caption="Trusted apps"
      columns={["Client ID", "Label", ...(editable ? [{ label: "", stickyEnd: true }] : [])]}
      stack
      empty={apps.length === 0 ? <p className={s.muted}>{empty}</p> : undefined}
    >
      {apps.map((c, i) => {
        const host = clientHost(c.clientId);
        return (
          <Tr key={c.clientId}>
            <Td>
              <CellText wrap="anywhere" primary={<span className={s.mono}>{c.clientId}</span>} secondary={host ? `Vouched for by ${host}` : undefined} />
              {problem?.index === i && <span className={s.dangerText}>{problem.message}</span>}
            </Td>
            <Td>{c.label ? <CellText wrap="anywhere" primary={c.label} /> : <span className={s.muted}>—</span>}</Td>
            {editable && (
              <Td stickyEnd>
                <ActionMenu
                  label={`Actions for ${nameOf(c)}`}
                  actions={[
                    { label: "Edit…", icon: <Pencil aria-hidden />, onSelect: () => onEdit?.(i) },
                    { label: "Remove", icon: <Trash2 aria-hidden />, danger: true, onSelect: () => onRemove?.(i) },
                  ]}
                />
              </Td>
            )}
          </Tr>
        );
      })}
    </Table>
  );
}
