/*
 * DateRangeFilter (Q13): date-range presets (Today · 7 · 30 · 90 days ·
 * Custom) instead of native mm/dd/yyyy inputs, kept in the URL
 * (?range=30d or ?range=2026-09-01/2026-09-26) so a filtered view can be
 * linked and survives a reload.
 *
 *   const range = useDateRangeParam({ defaultPreset: "30d" });
 *   useQuery(analyticsQuery({ from: range.fromDay, to: range.toDay }));
 *   <DateRangeFilter range={range} />
 *
 * `from`/`toExclusive` are local-midnight Dates (for RFC 3339 API params);
 * `fromDay`/`toDay` are YYYY-MM-DD (inclusive) for date-only APIs.
 */
import { cx } from "@/lib/bitop-utils";
import styles from "./templates.module.css";
import { useState } from "react";
import { startOfDay } from "@/components/ui/calendar/calendar";
import {
  type DateRangePreset,
  dateRangePresets,
  DateRangePresets,
  type DateRangeSelection,
  parseDateRangeSelection,
  serializeDateRangeSelection,
} from "@/components/ui/date-picker/date-picker";
import { useSearchParams } from "@/lib/url-search";

export type DateRangeParam = {
  param: string;
  presets: DateRangePreset[];
  /** The selection, or null for "any time". */
  selection: DateRangeSelection | null;
  from?: Date;
  /** The day after the last day (exclusive upper bound). */
  toExclusive?: Date;
  fromDay?: string;
  toDay?: string;
  set: (next: DateRangeSelection | null) => void;
  /** A default applies: the filter can't be cleared to "any time". */
  hasDefault: boolean;
};

const day = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;

export function useDateRangeParam(opts: { param?: string; presets?: DateRangePreset[]; defaultPreset?: string } = {}): DateRangeParam {
  const { param = "range", presets = dateRangePresets, defaultPreset } = opts;
  const [params, setParams] = useSearchParams();
  const raw = params.get(param) ?? defaultPreset ?? null;
  const selection = parseDateRangeSelection(raw, presets);
  const range = selection?.range;
  const from = range ? startOfDay(range.from) : undefined;
  const last = range ? startOfDay(range.to ?? range.from) : undefined;
  const toExclusive = last ? new Date(last.getFullYear(), last.getMonth(), last.getDate() + 1) : undefined;
  const set = (next: DateRangeSelection | null) =>
    setParams((p) => {
      const out = new URLSearchParams(p);
      const text = serializeDateRangeSelection(next);
      // The default stays out of the URL; a custom range without both ends isn't written yet.
      if (!text || text === defaultPreset) out.delete(param);
      else out.set(param, text);
      return out;
    });
  return {
    param,
    presets,
    selection,
    from,
    toExclusive,
    fromDay: from && day(from),
    toDay: last && day(last),
    set,
    hasDefault: Boolean(defaultPreset),
  };
}

export type DateRangeFilterProps = {
  range: DateRangeParam;
  /** Names the preset group (default "Date range"). */
  label?: string;
  /** Latest selectable day for Custom (default today). */
  max?: Date;
  size?: "sm" | "md";
  className?: string;
};

export function DateRangeFilter({ range, label = "Date range", max, size = "sm", className }: DateRangeFilterProps) {
  // "Custom" before both ends are picked has nothing to put in the URL yet: the
  // first click only sets the start (kept here), the second the end.
  const [pending, setPending] = useState<DateRangeSelection | null>(null);
  return (
    <DateRangePresets
      aria-label={label}
      presets={range.presets}
      value={pending ?? range.selection}
      onValueChange={(next) => {
        const incomplete = next?.preset === "custom" && !next.range?.to;
        setPending(incomplete ? next : null);
        if (!incomplete) range.set(next);
      }}
      clearable={!range.hasDefault}
      size={size}
      className={cx(styles.dateRange, className)}
      pickerProps={{ max: max ?? new Date() }}
    />
  );
}
