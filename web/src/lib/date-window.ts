/* A list's date facet as the API's time window. */
import type { DateRangeSelection } from "@/components/ui/date-picker/date-picker";
import type { FilterValues } from "@/components/ui/filter-bar/filter-bar";

/** The date facet as an RFC 3339 window [from, to), local days. */
export function rangeWindow(values: FilterValues, id = "range"): { from?: string; to?: string } {
  const sel = values[id] as DateRangeSelection | undefined;
  const r = sel && !Array.isArray(sel) ? sel.range : null;
  if (!r) return {};
  const start = new Date(r.from.getFullYear(), r.from.getMonth(), r.from.getDate());
  const last = r.to ?? r.from;
  const end = new Date(last.getFullYear(), last.getMonth(), last.getDate() + 1);
  return { from: start.toISOString(), to: end.toISOString() };
}
