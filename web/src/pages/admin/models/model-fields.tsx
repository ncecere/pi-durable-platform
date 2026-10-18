/* The model form's fields: the model and its limits, prices, and who may use it. */
import { useQuery } from "@tanstack/react-query";
import type { Schemas } from "@/api/client";
import { teamChoicesQuery } from "@/api/models";
import { FormSection } from "@/components/templates/form-page";
import { Checkbox } from "@/components/ui/checkbox/checkbox";
import { Combobox } from "@/components/ui/combobox/combobox";
import { Field } from "@/components/ui/field/field";
import { Input } from "@/components/ui/input/input";
import { NumberInput } from "@/components/ui/number-input/number-input";
import { RadioGroup } from "@/components/ui/radio-group/radio-group";
import { Switch } from "@/components/ui/switch/switch";
import { personalHint, personalLabel } from "./labels";
import type { ModelForm, ModelProblems } from "./model-form-state";
import m from "./models.module.css";

type Props = {
  form: ModelForm;
  setForm: (f: ModelForm) => void;
  isNew: boolean;
  /** The teams it's published to now (listed even when not among the loaded choices). */
  currentTeams: Schemas["ModelTeam"][];
  errors: ModelProblems;
};

export function ModelFields({ form, setForm, isNew, currentTeams, errors }: Props) {
  const set = (patch: Partial<ModelForm>) => setForm({ ...form, ...patch });
  const choices = useQuery({ ...teamChoicesQuery(), enabled: form.visibility === "teams" });
  const teams = new Map<string, string>(currentTeams.map((t) => [t.slug, t.name]));
  for (const s of choices.data ?? []) teams.set(s.team.slug, s.team.name);
  return (
    <>
      <FormSection title="Model">
        <Field label="Model ID" description={isNew ? "As the provider names it, for example gpt-5 or claude-sonnet-4-5." : "The provider's ID for it. It can't be changed."} error={errors.upstreamId}>
          <Input value={form.upstreamId} disabled={!isNew} spellCheck={false} autoComplete="off" maxLength={200} onValueChange={(v) => set({ upstreamId: v })} />
        </Field>
        <Field label="Name" labelHint={isNew ? "Optional" : undefined} description={isNew ? "What teams see. Defaults to the model ID." : "What teams see."} error={errors.name}>
          <Input value={form.name} autoComplete="off" maxLength={100} onValueChange={(v) => set({ name: v })} />
        </Field>
        <Field label="Context window" description="Tokens the model reads at once." error={errors.contextWindow}>
          <NumberInput value={form.contextWindow} maximumFractionDigits={0} unit="tokens" onValueChange={(v) => set({ contextWindow: v })} />
        </Field>
        <Field label="Max output tokens" description="The longest reply agents ask for." error={errors.maxTokens}>
          <NumberInput value={form.maxTokens} maximumFractionDigits={0} unit="tokens" onValueChange={(v) => set({ maxTokens: v })} />
        </Field>
        <Switch label="Accepts images" checked={form.images} onCheckedChange={(v) => set({ images: v })} />
        <Switch label="Reasoning" description="It thinks before it answers." checked={form.reasoning} onCheckedChange={(v) => set({ reasoning: v })} />
      </FormSection>
      <FormSection title="Price">
        <Field label="Input price" labelHint="Optional" description="US dollars per million input tokens." error={errors.inputPrice}>
          <NumberInput value={form.inputPrice} maximumFractionDigits={4} unit="USD" onValueChange={(v) => set({ inputPrice: v })} />
        </Field>
        <Field label="Output price" labelHint="Optional" description="US dollars per million output tokens." error={errors.outputPrice}>
          <NumberInput value={form.outputPrice} maximumFractionDigits={4} unit="USD" onValueChange={(v) => set({ outputPrice: v })} />
        </Field>
      </FormSection>
      <FormSection title="Who may use it">
        <div className={m.wide}>
          <Switch
            label="Enabled"
            description="Turned off, teams can't choose it, and agents running on it don't start until they're moved to another model."
            checked={form.enabled}
            onCheckedChange={(v) => set({ enabled: v })}
          />
        </div>
        <RadioGroup
          className={m.wide}
          legend="Published to"
          value={form.visibility}
          onValueChange={(v) => set({ visibility: v })}
          options={[
            { value: "all", label: "All teams" },
            { value: "teams", label: "Selected teams" },
          ]}
        />
        {form.visibility === "teams" ? (
          <Field className={m.wide} label="Teams" description="Only these teams' templates and agents can use it." error={errors.teams}>
            <Combobox
              multiple
              items={[...teams].map(([value, label]) => ({ value, label, hint: value }))}
              value={form.teams}
              onValueChange={(v) => set({ teams: v })}
              placeholder={choices.isLoading ? "Loading teams…" : "Choose teams"}
              listLabel="Teams"
              chipsLabel="Selected teams"
            />
          </Field>
        ) : null}
        <Checkbox
          className={m.wide}
          label={personalLabel}
          description={personalHint}
          checked={form.personal}
          onCheckedChange={(v) => set({ personal: v })}
        />
      </FormSection>
    </>
  );
}
