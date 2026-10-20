/*
 * Admin › Settings › App secrets (OAuth Connect): secrets platform admins
 * issue to trusted apps, so an app can call the app-level endpoints as
 * itself (one event stream for everyone who connected it, and their usage
 * by person) without holding anyone's token. A secret works only while its
 * app is on the trusted list (removing the app deletes its secrets); two at a
 * time, to rotate. The secret is shown
 * once, when issued; the list shows its first characters. Auditors read.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { api, unwrap } from "@/api/client";
import { type AppSecret, type AppSecretCreated, appSecretsQuery, type TrustedClient, trustedClientsQuery } from "@/api/oauth";
import { RelativeTime } from "@/components/templates/list-page";
import { ActionMenu } from "@/components/templates/action-menu";
import { SettingsSection } from "@/components/templates/settings-page";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { CopyField } from "@/components/ui/copy-field/copy-field";
import { CellText } from "@/components/ui/data-table/data-table";
import { AlertDialog, Dialog, DialogClose } from "@/components/ui/dialog/dialog";
import { Field, Form } from "@/components/ui/field/field";
import { NativeSelect } from "@/components/ui/input/input";
import { Stack } from "@/components/ui/layout/layout";
import { SkeletonText } from "@/components/ui/skeleton/skeleton";
import { Table, Td, Tr } from "@/components/ui/table/table";
import { toast } from "@/components/ui/toast/toast";
import s from "../../shared.module.css";
import { useIsPlatformAdmin } from "../hooks";

/** At most this many secrets per app (the API's limit). */
export const maxSecretsPerApp = 2;

const description =
  "A secret lets a trusted app call the platform as itself: one event stream for the conversations of everyone who connected it, and their usage by person, without holding anyone's token. It works only while the app is trusted: removing an app from the trusted apps deletes its secrets, and trusting it again needs new ones. Two per app at most, so you can rotate: issue a new one, move the app to it, delete the old one.";

const appName = (clientId: string, trusted: TrustedClient[]) => trusted.find((c) => c.clientId === clientId)?.label || clientId;

export function AppSecretsSettings() {
  const isAdmin = useIsPlatformAdmin();
  const secrets = useQuery(appSecretsQuery());
  const trusted = useQuery(trustedClientsQuery());
  const [issuing, setIssuing] = useState(false);
  const [deleting, setDeleting] = useState<AppSecret | null>(null);
  const qc = useQueryClient();
  const remove = useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/v1/admin/settings/oauth-clients/secrets/{secretId}", { params: { path: { secretId: id } } })),
    onSuccess: () => {
      setDeleting(null);
      void qc.invalidateQueries({ queryKey: appSecretsQuery().queryKey });
      toast.success("App secret deleted", "It stopped working at once.");
    },
  });
  const apps = trusted.data?.clients ?? [];
  const list = secrets.data ?? [];
  const issuable = apps.filter((c) => list.filter((x) => x.clientId === c.clientId).length < maxSecretsPerApp);
  const action = isAdmin && (
    <Button size="sm" variant="secondary" disabled={issuable.length === 0} onClick={() => setIssuing(true)}>
      <KeyRound aria-hidden /> Issue secret
    </Button>
  );
  return (
    <>
      <SettingsSection title="App secrets" description={description} actions={action || undefined}>
        {secrets.isLoading ? (
          <div role="status" aria-label="Loading…">
            <SkeletonText lines={2} />
          </div>
        ) : secrets.error ? (
          <ErrorAlert error={secrets.error} title="Couldn't load the app secrets" onRetry={() => void secrets.refetch()} />
        ) : (
          <Table
            caption="App secrets"
            columns={["App", "Secret", "Issued", "Last used", ...(isAdmin ? [{ label: "", stickyEnd: true }] : [])]}
            stack
            empty={list.length === 0 ? <p className={s.muted}>None: no app calls the platform as itself.</p> : undefined}
          >
            {list.map((x) => (
              <Tr key={x.id}>
                <Td>
                  <CellText
                    wrap="anywhere"
                    primary={appName(x.clientId, apps)}
                    secondary={x.trusted ? undefined : <Badge tone="warning">Not trusted: doesn&apos;t work</Badge>}
                  />
                </Td>
                <Td>
                  <span className={s.mono}>{x.prefix}…</span>
                </Td>
                <Td>
                  <RelativeTime value={x.createdAt} />
                </Td>
                <Td>{x.lastUsedAt ? <RelativeTime value={x.lastUsedAt} /> : <span className={s.muted}>Never</span>}</Td>
                {isAdmin && (
                  <Td stickyEnd>
                    <ActionMenu
                      label={`Actions for the secret ${x.prefix}…`}
                      actions={[{ label: "Delete…", icon: <Trash2 aria-hidden />, danger: true, onSelect: () => setDeleting(x) }]}
                    />
                  </Td>
                )}
              </Tr>
            ))}
          </Table>
        )}
      </SettingsSection>
      <AlertDialog
        open={deleting !== null}
        onOpenChange={(o) => {
          if (!o) {
            setDeleting(null);
            remove.reset();
          }
        }}
        title="Delete this app secret?"
        description={
          deleting ? `${appName(deleting.clientId, apps)} can no longer use ${deleting.prefix}…: its event stream ends and its calls are refused at once.` : ""
        }
        confirmLabel="Delete secret"
        busy={remove.isPending}
        error={remove.error}
        onConfirm={() => deleting && remove.mutate(deleting.id)}
      />
      {issuing && <IssueSecretDialog apps={issuable} onClose={() => setIssuing(false)} />}
    </>
  );
}

/** Choose a trusted app, issue its secret, and show the secret once. */
function IssueSecretDialog({ apps, onClose }: { apps: TrustedClient[]; onClose: () => void }) {
  const qc = useQueryClient();
  const formId = useId();
  const [clientId, setClientId] = useState(apps[0]?.clientId ?? "");
  const issue = useMutation({
    mutationFn: async () => unwrap(await api.POST("/v1/admin/settings/oauth-clients/secrets", { body: { clientId } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: appSecretsQuery().queryKey }),
  });
  if (issue.data) {
    return (
      <Dialog open onOpenChange={(o) => !o && onClose()} title="App secret issued" size="lg" footer={<Button onClick={onClose}>Done</Button>}>
        <SecretOnce created={issue.data} name={appName(issue.data.clientId, apps)} />
      </Dialog>
    );
  }
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title="Issue an app secret"
      description="The secret is shown once, right after it's issued. Give it to the app's operators over a safe channel."
      footer={
        <>
          <DialogClose>Cancel</DialogClose>
          <Button type="submit" form={formId} loading={issue.isPending}>
            Issue secret
          </Button>
        </>
      }
    >
      <Form
        id={formId}
        onSubmit={(e) => {
          e.preventDefault();
          if (clientId) issue.mutate();
        }}
      >
        <Field label="App" description="A trusted app with fewer than two secrets.">
          <NativeSelect value={clientId} onChange={(e) => setClientId(e.target.value)}>
            {apps.map((c) => (
              <option key={c.clientId} value={c.clientId}>
                {c.label || c.clientId}
              </option>
            ))}
          </NativeSelect>
        </Field>
        <ErrorAlert error={issue.error} title="Couldn't issue the secret" />
      </Form>
    </Dialog>
  );
}

function SecretOnce({ created, name }: { created: AppSecretCreated; name: string }) {
  return (
    <Stack gap={5}>
      <Alert tone="warning" title="Copy this secret now.">
        It won&apos;t be shown again. Store it where {name} keeps its secrets. If it's lost, delete it and issue a new one.
      </Alert>
      <CopyField
        label="Secret"
        name="app secret"
        value={created.secret}
        description={`The app sends it as “Authorization: Bearer …” to /v1/app/events and /v1/app/usage. Starts with ${created.prefix}.`}
      />
    </Stack>
  );
}
