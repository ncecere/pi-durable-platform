/*
 * Add or edit one trusted app (a staged change: the section's Save sends the
 * whole list). Its client ID (a metadata document's https address) and an
 * optional label; typos and duplicates are caught here, and the first field
 * with a problem takes the focus.
 */
import { useRef, useState } from "react";
import type { TrustedClient } from "@/api/oauth";
import { FormDialog } from "@/components/form-dialog";
import { Field } from "@/components/ui/field/field";
import { Input } from "@/components/ui/input/input";
import { clientIdProblem, maxClientId, maxLabel, normalApp } from "./trusted-apps";

type Props = {
  /** The app being edited (undefined: a new one). */
  app?: TrustedClient;
  /** The other apps, to catch a duplicate. */
  others: TrustedClient[];
  onSave: (c: TrustedClient) => void;
  onClose: () => void;
};

export function TrustedAppDialog({ app, others, onSave, onClose }: Props) {
  const [clientId, setClientId] = useState(app?.clientId ?? "");
  const [label, setLabel] = useState(app?.label ?? "");
  const [submitted, setSubmitted] = useState(false);
  const idRef = useRef<HTMLInputElement>(null);
  const labelRef = useRef<HTMLInputElement>(null);
  const duplicate = others.some((o) => o.clientId.trim() === clientId.trim());
  const problem = clientIdProblem(clientId) ?? (duplicate ? "This app is already on the list." : undefined);
  const labelProblem = label.trim().length > maxLabel ? `Use at most ${maxLabel} characters.` : undefined;
  const submit = () => {
    setSubmitted(true);
    if (problem || labelProblem) {
      // After the errors render: the first invalid field takes the focus, so its message is read out.
      const first = problem ? idRef : labelRef;
      requestAnimationFrame(() => first.current?.focus());
      return;
    }
    onSave(normalApp({ clientId, label }));
  };
  return (
    <FormDialog
      title={app ? "Edit trusted app" : "Add trusted app"}
      description="People connecting this app skip the approval screen. They still sign in, and it still gets only their access."
      onClose={onClose}
      onSubmit={submit}
      submitLabel={app ? "Update app" : "Add app"}
      formProps={{ noValidate: true }}
    >
      <Field
        label="Client ID"
        description="The https address of the app's metadata document (its OAuth client_id), such as https://chat.example.com/oauth/client-metadata.json."
        error={submitted ? problem : undefined}
      >
        <Input ref={idRef} autoComplete="off" spellCheck={false} inputMode="url" maxLength={maxClientId} value={clientId} onChange={(e) => setClientId(e.target.value)} />
      </Field>
      <Field label="Label" labelHint="Optional" description="A name for this list, such as “Team chat”." error={submitted ? labelProblem : undefined}>
        <Input ref={labelRef} maxLength={maxLabel} value={label} onChange={(e) => setLabel(e.target.value)} />
      </Field>
    </FormDialog>
  );
}
