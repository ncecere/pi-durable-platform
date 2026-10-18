/* Who may attach an MCP server: every team, or chosen teams (a combobox of the active teams), and personal workspaces. */
import { useQuery } from "@tanstack/react-query";
import type { Schemas } from "@/api/client";
import { teamChoicesQuery } from "@/api/models";
import { Checkbox } from "@/components/ui/checkbox/checkbox";
import { Combobox } from "@/components/ui/combobox/combobox";
import { Field } from "@/components/ui/field/field";
import { RadioGroup } from "@/components/ui/radio-group/radio-group";
import { personalHint, personalLabel } from "../models/labels";
import type { McpVisibility } from "./labels";

type Props = {
  visibility: McpVisibility;
  /** Team slugs (visibility "teams"). */
  teams: string[];
  /** Also available in personal workspaces. */
  personal: boolean;
  onChange: (next: { visibility?: McpVisibility; teams?: string[]; personal?: boolean }) => void;
  /** The teams it's published to now (listed even when not among the loaded choices). */
  currentTeams?: Schemas["ModelTeam"][] | null;
  disabled?: boolean;
  error?: string;
};

export function PublishFields({ visibility, teams, personal, onChange, currentTeams, disabled, error }: Props) {
  const choices = useQuery({ ...teamChoicesQuery(), enabled: visibility === "teams" && !disabled });
  const names = new Map<string, string>((currentTeams ?? []).map((t) => [t.slug, t.name]));
  for (const c of choices.data ?? []) names.set(c.team.slug, c.team.name);
  return (
    <>
      <RadioGroup
        legend="Published to"
        disabled={disabled}
        value={visibility}
        onValueChange={(v) => onChange({ visibility: v })}
        options={[
          { value: "all", label: "All teams" },
          { value: "teams", label: "Selected teams" },
        ]}
      />
      {visibility === "teams" ? (
        <Field label="Teams" description="Only these teams' templates can give their agents this server." error={error}>
          <Combobox
            multiple
            disabled={disabled}
            items={[...names].map(([value, label]) => ({ value, label, hint: value }))}
            value={teams}
            onValueChange={(v) => onChange({ teams: v })}
            placeholder={choices.isLoading ? "Loading teams…" : "Choose teams"}
            listLabel="Teams"
            chipsLabel="Selected teams"
          />
        </Field>
      ) : null}
      <Checkbox label={personalLabel} description={personalHint} disabled={disabled} checked={personal} onCheckedChange={(v) => onChange({ personal: v })} />
    </>
  );
}
