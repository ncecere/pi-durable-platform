/*
 * The "Model" select of templates and agents: a default choice ("" — the
 * platform's default or the template's model) and the models the team may
 * use. A current model that isn't among them any more (turned off,
 * unpublished) stays listed, marked unavailable, so the select shows it.
 */
import type { ReactNode } from "react";
import { Field } from "@/components/ui/field/field";
import { NativeSelect } from "@/components/ui/input/input";

export type ModelChoice = { id: string; name: string; providerName: string };

/** "Fake One · Provider". */
export const modelLabel = (m: Pick<ModelChoice, "name" | "providerName">) => `${m.name} · ${m.providerName}`;

type Props = {
  label?: string;
  /** Short muted text after the label, e.g. "Optional". */
  labelHint?: ReactNode;
  description?: ReactNode;
  /** The models to offer. */
  models: ModelChoice[] | undefined;
  loading?: boolean;
  /** The chosen model's id, or "" for the default. */
  value: string;
  onChange: (id: string) => void;
  /** The "" choice: "Platform default" or "Template's model". */
  defaultLabel: string;
  /** The model in use now, kept as a choice when it isn't in `models`. */
  current?: ModelChoice | null;
  disabled?: boolean;
  error?: ReactNode;
};

export function ModelSelect({ label = "Model", labelHint, description, models, loading, value, onChange, defaultLabel, current, disabled, error }: Props) {
  const list = models ?? [];
  const missing = current && !list.some((m) => m.id === current.id) ? current : null;
  return (
    <Field label={label} labelHint={labelHint} description={description} error={error}>
      <NativeSelect value={value} disabled={disabled || loading} onChange={(e) => onChange(e.target.value)}>
        <option value="">{loading ? "Loading models…" : defaultLabel}</option>
        {missing ? <option value={missing.id}>{`${modelLabel(missing)} (unavailable)`}</option> : null}
        {list.map((m) => (
          <option key={m.id} value={m.id}>
            {modelLabel(m)}
          </option>
        ))}
      </NativeSelect>
    </Field>
  );
}
