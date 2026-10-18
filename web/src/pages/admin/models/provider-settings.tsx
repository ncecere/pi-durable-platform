/*
 * A provider's Settings: name, base URL, access mode and the switch (If-Match
 * on its revision), and the write-only parts: a new API key and the extra
 * headers, sent only when filled in. Delete is in the Danger zone.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, ApiError, ifMatch, unwrap } from "@/api/client";
import { invalidateModels, type ModelProvider, providerQuery } from "@/api/models";
import { useRevisionForm } from "@/components/templates/revision-form";
import { DangerAction, DangerZone, SettingsPage, SettingsSection } from "@/components/templates/settings-page";
import { Button } from "@/components/ui/button/button";
import { Field } from "@/components/ui/field/field";
import { Input } from "@/components/ui/input/input";
import { RadioGroup } from "@/components/ui/radio-group/radio-group";
import { Switch } from "@/components/ui/switch/switch";
import { toast } from "@/components/ui/toast/toast";
import { adminOnly } from "@/lib/terms";
import { HeadersSetting } from "./headers-editor";
import { type AccessMode, accessLabels, accessOptions, type HeaderRow, headersOf, headersProblem, urlProblem } from "./labels";

type Form = { name: string; baseUrl: string; accessMode: AccessMode; enabled: boolean };
const formOf = (p: ModelProvider): Form => ({ name: p.name, baseUrl: p.baseUrl, accessMode: p.accessMode, enabled: p.enabled });
const labels = { name: "Name", baseUrl: "Base URL", accessMode: "Access", enabled: "Enabled" };

type Props = { provider: ModelProvider; isAdmin: boolean; onDelete: () => void };

export function ProviderSettingsTab({ provider, isAdmin, onDelete }: Props) {
  const qc = useQueryClient();
  const saved = formOf(provider);
  const [form, setForm, revision] = useRevisionForm(saved, provider.revision, { labels, choices: { accessMode: accessLabels } });
  // Write-only: never loaded, so kept out of the revision form. null headers: keep the current ones.
  const [apiKey, setApiKey] = useState("");
  const [headers, setHeaders] = useState<HeaderRow[] | null>(null);
  const [submitted, setSubmitted] = useState(false);
  const problems = {
    name: !form.name.trim() ? "Enter a name." : undefined,
    baseUrl: urlProblem(form.baseUrl, true),
    headers: headers ? headersProblem(headers) : undefined,
  };
  const invalid = submitted && Object.values(problems).some(Boolean);
  const save = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.PATCH("/v1/admin/model-providers/{providerId}", {
          params: { path: { providerId: provider.id }, header: ifMatch(provider.revision) },
          body: {
            name: form.name.trim(),
            baseUrl: form.baseUrl.trim(),
            accessMode: form.accessMode,
            enabled: form.enabled,
            apiKey: apiKey.trim() || undefined,
            headers: headers ? headersOf(headers) : undefined,
          },
        }),
      ),
    onSuccess: (updated) => {
      qc.setQueryData(providerQuery(updated.id).queryKey, updated);
      void invalidateModels(qc);
      setApiKey("");
      setHeaders(null);
      setSubmitted(false);
      toast.success("Provider saved", apiKey.trim() ? "Agents use the new key from their next request." : undefined);
    },
  });
  const taken = save.error instanceof ApiError && save.error.code === "provider_name_taken";
  const dirty = JSON.stringify(form) !== JSON.stringify(saved) || apiKey !== "" || headers !== null;
  const shown = (p: string | undefined) => (submitted ? p : undefined);
  return (
    <SettingsPage
      revision={revision}
      dirty={dirty}
      canEdit={isAdmin}
      readOnlyNote={adminOnly}
      saving={save.isPending}
      error={taken ? undefined : save.error}
      message={invalid || taken ? "Not saved: fix the highlighted field" : undefined}
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
          <Input maxLength={100} disabled={!isAdmin} value={form.name} onValueChange={(v) => setForm((f) => ({ ...f, name: v }))} />
        </Field>
        <Field label="Base URL" description="Agents in direct mode call this address; in proxy mode the platform does." error={shown(problems.baseUrl)}>
          <Input maxLength={2000} spellCheck={false} disabled={!isAdmin} value={form.baseUrl} onValueChange={(v) => setForm((f) => ({ ...f, baseUrl: v }))} />
        </Field>
        <RadioGroup legend="How agents reach it" disabled={!isAdmin} value={form.accessMode} onValueChange={(v) => setForm((f) => ({ ...f, accessMode: v }))} options={accessOptions} />
        <Switch
          label="Enabled"
          description="Turned off, its models take no new use, and agents running on them don't start until it's turned on again."
          disabled={!isAdmin}
          checked={form.enabled}
          onCheckedChange={(v) => setForm((f) => ({ ...f, enabled: v }))}
        />
      </SettingsSection>
      <SettingsSection title="Credentials" description="Stored encrypted and never shown, here or in the API.">
        {isAdmin ? (
          <Field label="Replace API key" labelHint="Optional" description="Leave empty to keep the current key.">
            <Input type="password" autoComplete="new-password" spellCheck={false} maxLength={4000} value={apiKey} onValueChange={setApiKey} />
          </Field>
        ) : null}
        <HeadersSetting has={provider.hasHeaders} rows={headers} onChange={setHeaders} error={shown(problems.headers)} isAdmin={isAdmin} />
      </SettingsSection>
      {isAdmin && (
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
