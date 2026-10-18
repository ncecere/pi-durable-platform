/* Add provider (platform admins): its type, name, base URL (prefilled for OpenAI and Anthropic), key, access mode and extra headers. */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { api, ApiError, errorMessage, unwrap } from "@/api/client";
import { invalidateModels, type ProviderKind, providerKindsQuery } from "@/api/models";
import { FormDialog } from "@/components/form-dialog";
import { Disclosure } from "@/components/ui/disclosure/disclosure";
import { Field, Fieldset } from "@/components/ui/field/field";
import { Input, NativeSelect } from "@/components/ui/input/input";
import { RadioGroup } from "@/components/ui/radio-group/radio-group";
import { toast } from "@/components/ui/toast/toast";
import { HeadersEditor } from "./headers-editor";
import { type AccessMode, accessOptions, type HeaderRow, headersOf, headersProblem, kindLabels, urlProblem } from "./labels";

export function AddProviderDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const kinds = useQuery(providerKindsQuery());
  const [picked, setPicked] = useState<ProviderKind | null>(null);
  const kind = picked ?? kinds.data?.[0]?.id ?? "openai";
  const defaultUrl = (k: string) => kinds.data?.find((x) => x.id === k)?.defaultBaseUrl ?? "";
  // undefined: follow the type's default until the person types an address.
  const [typedUrl, setTypedUrl] = useState<string | undefined>(undefined);
  const baseUrl = typedUrl ?? defaultUrl(kind);
  const [name, setName] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [accessMode, setAccessMode] = useState<AccessMode>("proxy");
  const [headers, setHeaders] = useState<HeaderRow[]>([]);
  const [submitted, setSubmitted] = useState(false);
  const [taken, setTaken] = useState(false);
  const [busy, setBusy] = useState(false);
  const urlRequired = !defaultUrl(kind);
  const problems = {
    name: !name.trim() ? "Enter a name." : undefined,
    baseUrl: urlProblem(baseUrl, urlRequired),
    apiKey: !apiKey.trim() ? "Enter the provider's API key." : undefined,
    headers: headersProblem(headers),
  };
  const shown = (p: string | undefined) => (submitted ? p : undefined);
  const submit = async () => {
    setSubmitted(true);
    if (Object.values(problems).some(Boolean)) return;
    setBusy(true);
    try {
      const extra = headersOf(headers);
      const created = unwrap(
        await api.POST("/v1/admin/model-providers", {
          body: {
            name: name.trim(),
            kind,
            baseUrl: baseUrl.trim() || undefined,
            apiKey: apiKey.trim(),
            accessMode,
            headers: Object.keys(extra).length ? extra : undefined,
          },
        }),
      );
      await invalidateModels(qc);
      toast.success(`${created.name} was added`, "Discover its models or add them by hand.");
      onClose();
      void navigate({ to: "/admin/models/providers/$providerId", params: { providerId: created.id } });
    } catch (err) {
      if (err instanceof ApiError && err.code === "provider_name_taken") setTaken(true);
      else toast.error("Couldn't add the provider", errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <FormDialog title="Add provider" description="The key is stored encrypted and never shown again." size="lg" onClose={onClose} onSubmit={() => void submit()} submitLabel="Add provider" busy={busy}>
      <Field label="Type">
        <NativeSelect
          value={kind}
          onChange={(e) => {
            const next = e.target.value as ProviderKind;
            // Keep a typed address unless it was empty or the old type's default.
            if (typedUrl !== undefined && ["", defaultUrl(kind)].includes(typedUrl.trim())) setTypedUrl(undefined);
            setPicked(next);
          }}
        >
          {(kinds.data ?? [{ id: "openai" as const, name: kindLabels.openai }]).map((k) => (
            <option key={k.id} value={k.id}>
              {k.name}
            </option>
          ))}
        </NativeSelect>
      </Field>
      <Field label="Name" description="How admins and teams see it, for example “OpenAI” or “LiteLLM gateway”." error={shown(problems.name) ?? (taken ? "Another provider has this name." : undefined)}>
        <Input
          value={name}
          maxLength={100}
          autoComplete="off"
          onValueChange={(v) => {
            setName(v);
            setTaken(false);
          }}
        />
      </Field>
      <Field
        label="Base URL"
        description={urlRequired ? "The server's API address, for example https://litellm.example.com/v1." : "The provider's API. Change it only to go through a gateway."}
        error={shown(problems.baseUrl)}
      >
        <Input value={baseUrl} spellCheck={false} autoComplete="off" maxLength={2000} placeholder={urlRequired ? "https://" : undefined} onValueChange={setTypedUrl} />
      </Field>
      <Field label="API key" error={shown(problems.apiKey)}>
        <Input type="password" value={apiKey} autoComplete="new-password" spellCheck={false} maxLength={4000} onValueChange={setApiKey} />
      </Field>
      <RadioGroup legend="How agents reach it" value={accessMode} onValueChange={setAccessMode} options={accessOptions} />
      <Disclosure title="Advanced" summary={headers.length ? `${headers.length} extra ${headers.length === 1 ? "header" : "headers"}` : undefined}>
        <Fieldset legend="Extra headers" description="Optional. Sent with every request to the provider, for example a gateway's tenant header. Stored encrypted, like the key.">
          <HeadersEditor rows={headers} onChange={setHeaders} error={shown(problems.headers)} />
        </Fieldset>
      </Disclosure>
    </FormDialog>
  );
}
