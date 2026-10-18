/*
 * Admin's Owner filter (Models, MCP servers), in the URL as ?owner= (kept
 * with ?tab=, so both of Models' tabs show the same owner's): every owner (no
 * parameter), the platform's ("platform"), or one team's (its slug). The
 * teams offered are those that own something in the unfiltered list; a team
 * in the address that owns none still shows, by its slug.
 */
import { useQuery } from "@tanstack/react-query";
import { adminScope, type ModelOwner, providersQuery } from "@/api/models";
import { Field } from "@/components/ui/field/field";
import { NativeSelect } from "@/components/ui/input/input";
import { useSearchParams } from "@/lib/url-search";
import { ownerLabel } from "./labels";

export const OWNER_PARAM = "owner";

/** The owner in the address: "all", "platform" or a team's slug. */
export function useOwnerParam(): [string, (owner: string) => void] {
  const [params, setParams] = useSearchParams();
  const owner = params.get(OWNER_PARAM)?.trim() || "all";
  const set = (next: string) =>
    setParams((p) => {
      const out = new URLSearchParams(p);
      if (next && next !== "all") out.set(OWNER_PARAM, next);
      else out.delete(OWNER_PARAM);
      // Another owner's rows: start again at their first page.
      out.delete("page");
      return out;
    });
  return [owner, set];
}

type SelectProps = {
  value: string;
  onChange: (owner: string) => void;
  /** The owners of every row (null: the platform's), from the unfiltered list. */
  owners: (ModelOwner | null | undefined)[];
};

/** The select itself, given the owners to offer. */
export function OwnerSelect({ value, onChange, owners }: SelectProps) {
  const teams = new Map<string, ModelOwner>();
  for (const o of owners) if (o) teams.set(o.slug, o);
  const sorted = [...teams.values()].sort((a, b) => a.name.localeCompare(b.name));
  const unknown = value !== "all" && value !== "platform" && !teams.has(value);
  return (
    <Field label="Owner" hideLabel>
      <NativeSelect size="sm" value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="all">All owners</option>
        <option value="platform">Platform</option>
        {sorted.length > 0 || unknown ? (
          <optgroup label="Teams">
            {sorted.map((t) => (
              <option key={t.slug} value={t.slug}>
                {ownerLabel(t)}
              </option>
            ))}
            {unknown ? <option value={value}>{value}</option> : null}
          </optgroup>
        ) : null}
      </NativeSelect>
    </Field>
  );
}

/** Admin › Models' filter: the teams that own a provider. */
export function OwnerFilter({ value, onChange }: Omit<SelectProps, "owners">) {
  const every = useQuery(providersQuery(adminScope, "all"));
  return <OwnerSelect value={value} onChange={onChange} owners={(every.data ?? []).map((p) => p.team)} />;
}
