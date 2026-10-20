/*
 * Admin › Usage's App filter, in the URL as ?app= (a connected app's
 * client_id): every request (no parameter), or only what an agent did
 * answering messages sent through that app (DESIGN.md › OAuth Connect). The
 * apps offered are those people have connected; an app in the address that
 * nobody connects now still shows, by its client ID.
 */
import { useQuery } from "@tanstack/react-query";
import { connectedAppsQuery } from "@/api/oauth";
import { Field } from "@/components/ui/field/field";
import { NativeSelect } from "@/components/ui/input/input";
import { useSearchParams } from "@/lib/url-search";

export const APP_PARAM = "app";

/** The app in the address ("" for all usage). */
export function useAppParam(): [string, (app: string) => void] {
  const [params, setParams] = useSearchParams();
  const app = params.get(APP_PARAM)?.trim() ?? "";
  const set = (next: string) =>
    setParams((p) => {
      const out = new URLSearchParams(p);
      if (next) out.set(APP_PARAM, next);
      else out.delete(APP_PARAM);
      return out;
    });
  return [app, set];
}

export function AppFilter({ value, onChange }: { value: string; onChange: (app: string) => void }) {
  const apps = useQuery(connectedAppsQuery());
  const list = apps.data ?? [];
  const unknown = value !== "" && !list.some((a) => a.clientId === value);
  return (
    <Field label="App" hideLabel>
      <NativeSelect size="sm" value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">All usage</option>
        {list.length > 0 || unknown ? (
          <optgroup label="Through a connected app">
            {list.map((a) => (
              <option key={a.clientId} value={a.clientId}>
                {a.name} ({a.people === 1 ? "1 person" : `${a.people} people`})
              </option>
            ))}
            {unknown ? <option value={value}>{value}</option> : null}
          </optgroup>
        ) : null}
      </NativeSelect>
    </Field>
  );
}
