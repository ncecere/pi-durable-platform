/*
 * The current URL's search parameters as URLSearchParams, for page state
 * that belongs in the address (filters, the open record, date ranges).
 *
 * The router parses the query string into an object (a repeated key becomes
 * an array, "12" becomes 12); this converts that object to and from
 * URLSearchParams, so bitop-ui's helpers (filterValuesToSearchParams /
 * filterValuesFromSearchParams, serializeDateRangeSelection) work on it and
 * the values survive a reload. Keys owned by the route (e.g. ?tab=) are kept.
 *
 * Routes whose validateSearch drops unknown keys (tabSearch) lose these
 * parameters; use tabSearch(tabs, { passthrough: true }) on such routes.
 */
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useCallback, useMemo } from "react";

type SearchObject = Record<string, unknown>;

const scalar = (v: unknown) => (typeof v === "string" ? v : typeof v === "number" || typeof v === "boolean" ? String(v) : undefined);

/** A router search object as URLSearchParams (arrays become repeated keys). */
export function toSearchParams(search: SearchObject): URLSearchParams {
  const out = new URLSearchParams();
  for (const [key, value] of Object.entries(search)) {
    for (const item of Array.isArray(value) ? value : [value]) {
      const text = scalar(item);
      if (text !== undefined && text !== "") out.append(key, text);
    }
  }
  return out;
}

/**
 * A value as the router would parse it: "405" \u2192 405, "true" \u2192 true. Handing the
 * router a string that looks like a number makes it JSON-quote it in the
 * address (?record=%22405%22); the parsed form keeps it plain (?record=405).
 */
function routerValue(text: string): unknown {
  if (text === "true" || text === "false") return text === "true";
  if (/^-?\d{1,15}$/.test(text) && String(Number(text)) === text) return Number(text);
  return text;
}

/** URLSearchParams as a router search object (repeated keys become arrays). */
export function fromSearchParams(params: URLSearchParams): SearchObject {
  const out: SearchObject = {};
  for (const key of new Set(params.keys())) {
    const all = params.getAll(key).map(routerValue);
    out[key] = all.length === 1 ? all[0] : all;
  }
  return out;
}

export type SetSearchParams = (update: (params: URLSearchParams) => URLSearchParams, opts?: { replace?: boolean }) => void;

/** The URL's search parameters and a setter (replaces the history entry by default). */
export function useSearchParams(): [URLSearchParams, SetSearchParams] {
  const search = useSearch({ strict: false }) as SearchObject;
  const navigate = useNavigate();
  const key = JSON.stringify(search);
  // Keyed on the serialised search, so a new object with the same values keeps the same params.
  const params = useMemo(() => toSearchParams(search), [key]);
  const set = useCallback<SetSearchParams>(
    (update, opts) =>
      void navigate({
        to: ".",
        search: ((prev: SearchObject) => fromSearchParams(update(toSearchParams(prev)))) as never,
        replace: opts?.replace ?? true,
      }),
    [navigate],
  );
  return [params, set];
}
