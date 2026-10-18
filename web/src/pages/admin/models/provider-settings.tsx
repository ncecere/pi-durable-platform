/*
 * A provider's Settings: name, base URL, access mode and the switch (If-Match
 * on its revision), and the write-only parts: a new API key and the extra
 * headers, sent only when filled in. Delete is in the Danger zone. A team's
 * own provider has no access mode (proxy only), and a refused address shows
 * on the URL field.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ApiError } from "@/api/client";
import { invalidateModels, type ModelProvider, providerQuery, updateProvider } from "@/api/models";
import { useRevisionForm } from "@/components/templates/revision-form";
import { DangerAction, DangerZone, SettingsPage, SettingsSection } from "@/components/templates/settings-page";
import { Button } from "@/components/ui/button/button";
import { Field } from "@/components/ui/field/field";
import { Input } from "@/components/ui/input/input";
import { RadioGroup } from "@/components/ui/radio-group/radio-group";
import { Switch } from "@/components/ui/switch/switch";
import { toast } from "@/components/ui/toast/toast";
import s from "../../shared.module.css";
import { HeadersSetting } from "./headers-editor";
import { type AccessMode, accessLabels, accessOptions, type HeaderRow, headersOf, headersProblem, teamAccessNote, urlProblem, urlRefusal } from "./labels";
import { type CatalogScope, isTeamOwned } from "./scope";

type Form = { name: string; baseUrl: string; accessMode: AccessMode; enabled: boolean };
const formOf = (p: ModelProvider): Form => ({ name: p.name, baseUrl: p.baseUrl, accessMode: p.accessMode, enabled: p.enabled });
const labels = { name: "Name", baseUrl: "Base URL", accessMode: "Access", enabled: "Enabled" };

type Props = { catalog: CatalogScope; provider: ModelProvider; onDelete: () => void };

export function ProviderSettingsTab({ catalog, provider, onDelete }: Props) {
  const { scope, canManage, readOnlyNote } = catalog;
  const teamOwned = isTeamOwned(scope, provider);
  const qc = useQueryClient();
  const saved = formOf(provider);
  const [form, setForm, revision] = useRevisionForm(saved, provider.revision, { labels, choices: { accessMode: accessLabels } });
  // Write-only: never loaded, so kept out of the revision form. null headers: keep the current ones.
  const [apiKey, setApiKey] = useState("");
  const [headers, setHeaders] = useState<HeaderRow[] | null>(null);
  const [submitted, setSubmitted] = useState(false);
  const problems = {
    name: !form.name.trim() ? "Enter a name." : undefined,
    baseUrl: urlProblem(form.baseUrl, true, teamOwned),
    headers: headers ? headersProblem(headers) : undefined,
  };
  const invalid = submitted && Object.values(problems).some(Boolean);
  const save = useMutation({
    mutationFn: async () =>
      updateProvider(scope, provider, {
        name: form.name.trim(),
        baseUrl: form.baseUrl.trim(),
        accessMode: teamOwned ? undefined : form.accessMode,
        enabled: form.enabled,
        apiKey: apiKey.trim() || undefined,
        headers: headers ? headersOf(headers) : undefined,
      }),
    onSuccess: (updated) => {
      qc.setQueryData(providerQuery(scope, updated.id).queryKey, updated);
      void invalidateModels(qc, scope);
      setApiKey("");
      setHeaders(null);
      setSubmitted(false);
      toast.success("Provider saved", apiKey.trim() ? "Agents use the new key from their next request." : undefined);
    },
  });
  const taken = save.error instanceof ApiError && save.error.code === "provider_name_taken";
  const refused = urlRefusal(save.error);
  const dirty = JSON.stringify(form) !== JSON.stringify(saved) || apiKey !== "" || headers !== null;
  const shown = (p: string | undefined) => (submitted ? p : undefined);
  return (
    <SettingsPage
      revision={revision}
      dirty={dirty}
      canEdit={canManage}
      readOnlyNote={readOnlyNote}
      saving={save.isPending}
      error={taken || refused ? undefined : save.error}
      message={invalid || taken || refused ? "Not saved: fix the highlighted field" : undefined}
      onSave={() => {
        setSubmitted(true);
        if (Object.values(problems).some(Boolean)) return;
        save.mutate();
      }}
      onDiscard={() => {
        setForm(saved);
        setApiKey("");
        setHeaders(null);
        setSubmitted(false);
        save.reset();
      }}
    >
      <SettingsSection title="General">
        <Field label="Name" error={shown(problems.name) ?? (taken ? "Another provider has this name." : undefined)}>
          <Input maxLength={100} disabled={!canManage} value={form.name} onValueChange={(v) => setForm((f) => ({ ...f, name: v }))} />
        </Field>
        <Field
          label="Base URL"
          description={
            teamOwned
              ? "The platform calls this address for the team's agents. It must be https, on a public address (or a private network platform admins allow)."
              : "Agents in direct mode call this address; in proxy mode the platform does."
          }
          error={shown(problems.baseUrl) ?? refused}
        >
          <Input
            maxLength={2000}
            spellCheck={false}
            disabled={!canManage}
            value={form.baseUrl}
            onValueChange={(v) => {
              if (refused) save.reset();
              setForm((f) => ({ ...f, baseUrl: v }));
            }}
          />
        </Field>
        {teamOwned ? (
          <p className={s.note}>{teamAccessNote}</p>
        ) : (
          <RadioGroup legend="How agents reach it" disabled={!canManage} value={form.accessMode} onValueChange={(v) => setForm((f) => ({ ...f, accessMode: v }))} options={accessOptions} />
        )}
        <Switch
          label="Enabled"
          description="Turned off, its models take no new use, and agents running on them don't start until it's turned on again."
          disabled={!canManage}
          checked={form.enabled}
          onCheckedChange={(v) => setForm((f) => ({ ...f, enabled: v }))}
        />
      </SettingsSection>
      <SettingsSection title="Credentials" description="Stored encrypted and never shown, here or in the API.">
        {canManage ? (
          <Field label="Replace API key" labelHint="Optional" description="Leave empty to keep the current key.">
            <Input type="password" autoComplete="new-password" spellCheck={false} maxLength={4000} value={apiKey} onValueChange={setApiKey} />
          </Field>
        ) : null}
        <HeadersSetting has={provider.hasHeaders} rows={headers} onChange={setHeaders} error={shown(problems.headers)} isAdmin={canManage} />
      </SettingsSection>
      {canManage && (
        <DangerZone>
          <DangerAction
            title="Delete this provider"
            description="Deletes its key and its models. Refused while templates or agents use one of its models."
            action={
              <Button variant="danger" onClick={onDelete}>
                Delete provider
              </Button>
            }
          />
        </DangerZone>
      )}
    </SettingsPage>
  );
}
