/* Usage reports: what a row is called, the URL state (period and grouping) and the days of a report. */
import type { UsageReport, UsageRow } from "@/api/usage";
import { type DateRangeParam, useDateRangeParam } from "@/components/templates/date-range-filter";
import { lastDaysPreset } from "@/components/ui/date-picker/date-picker";
import { useSearchParams } from "@/lib/url-search";

/** Last 7 · 30 · 90 days, and Custom (at most 366 days). */
export const usagePresets = [lastDaysPreset(7), lastDaysPreset(30), lastDaysPreset(90)];

/** The report's period (?range=), the last 30 days by default. */
export const useUsageRange = (): DateRangeParam => useDateRangeParam({ defaultPreset: "30d", presets: usagePresets });

export const GROUP_PARAM = "groupBy";

export type GroupOption<G extends string> = { value: G; label: string };

/** How the rows are grouped (?groupBy=); the first option is the default and stays out of the URL. */
export function useGroupByParam<G extends string>(options: readonly GroupOption<G>[]): [G, (next: G) => void] {
  const [params, setParams] = useSearchParams();
  const fallback = options[0]!.value;
  const raw = params.get(GROUP_PARAM);
  const value = options.find((o) => o.value === raw)?.value ?? fallback;
  const set = (next: G) =>
    setParams((p) => {
      const out = new URLSearchParams(p);
      if (next === fallback) out.delete(GROUP_PARAM);
      else out.set(GROUP_PARAM, next);
      return out;
    });
  return [value, set];
}

export const DEFAULT_MODEL = "Platform default model";
export const MANAGED_AGENTS = "Service-managed agents";
export const DELETED_AGENTS = "Deleted agents";

const utcDay = (day: string) => new Date(`${day}T00:00:00Z`);

/** "Fri, Oct 3, 2026" for a report day (a UTC date). */
export const dayLabel = (day: string | null | undefined) =>
  day ? utcDay(day).toLocaleDateString(undefined, { timeZone: "UTC", weekday: "short", month: "short", day: "numeric", year: "numeric" }) : "—";

/** "Oct 3" for a chart's axis. */
export const shortDay = (day: string) => utcDay(day).toLocaleDateString(undefined, { timeZone: "UTC", month: "short", day: "numeric" });

/** Every day from `from` to `to` (YYYY-MM-DD, inclusive). */
export function daysBetween(from: string, to: string): string[] {
  const out: string[] = [];
  const end = utcDay(to).getTime();
  for (let t = utcDay(from).getTime(); t <= end && out.length < 400; t += 86_400_000) out.push(new Date(t).toISOString().slice(0, 10));
  return out;
}

export const modelName = (r: UsageRow) => (r.modelId ? (r.modelName ?? "Unknown model") : DEFAULT_MODEL);

export const agentName = (r: UsageRow) => (r.agentId ? (r.agentName ?? "Unnamed agent") : r.managed ? MANAGED_AGENTS : DELETED_AGENTS);

export const teamName = (r: UsageRow) => r.teamName ?? r.teamSlug ?? "Unknown team";

/** A row's stable key in its grouping. */
export function rowId(groupBy: UsageReport["groupBy"], r: UsageRow): string {
  switch (groupBy) {
    case "day":
      return r.day ?? "";
    case "model":
      return r.modelId ?? "default";
    case "agent":
      return r.agentId ?? (r.managed ? "managed" : "deleted");
    case "team":
      return r.teamId ?? "";
  }
}

/** "3 requests used models without prices and aren't in the cost." */
export const unpricedText = (n: number) =>
  n === 1
    ? "1 request used a model without prices and isn't in the cost."
    : `${n.toLocaleString()} requests used models without prices and aren't in the cost.`;

export const costEstimate = "Estimated from the models' current prices.";
