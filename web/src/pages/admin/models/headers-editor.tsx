/* Extra headers sent with every request to a provider: rows of name and value (values are never shown back). */
import { Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button/button";
import { Field } from "@/components/ui/field/field";
import { Input } from "@/components/ui/input/input";
import type { HeaderRow } from "./labels";
import m from "./models.module.css";

type Props = { rows: HeaderRow[]; onChange: (rows: HeaderRow[]) => void; error?: string; disabled?: boolean };

export function HeadersEditor({ rows, onChange, error, disabled }: Props) {
  const set = (i: number, row: Partial<HeaderRow>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...row } : r)));
  return (
    <div className={m.headers}>
      {rows.map((r, i) => (
        <div key={i} className={m.headerRow}>
          <Field label={`Header ${i + 1} name`} hideLabel error={i === 0 ? error : undefined}>
            <Input placeholder="Name" value={r.name} disabled={disabled} spellCheck={false} autoComplete="off" maxLength={200} onValueChange={(v) => set(i, { name: v })} />
          </Field>
          <Field label={`Header ${i + 1} value`} hideLabel>
            <Input placeholder="Value" value={r.value} disabled={disabled} spellCheck={false} autoComplete="off" maxLength={4000} onValueChange={(v) => set(i, { value: v })} />
          </Field>
          <Button variant="ghost" iconOnly aria-label={`Remove header ${i + 1}`} disabled={disabled} onClick={() => onChange(rows.filter((_, j) => j !== i))}>
            <X aria-hidden />
          </Button>
        </div>
      ))}
      <div>
        <Button size="sm" variant="secondary" disabled={disabled || rows.length >= 20} onClick={() => onChange([...rows, { name: "", value: "" }])}>
          <Plus aria-hidden /> Add header
        </Button>
      </div>
    </div>
  );
}
