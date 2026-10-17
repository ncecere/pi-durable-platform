/* Shared by the audit logs: the person options, the date facet's API window, and CSV export. */
import { useQuery } from "@tanstack/react-query";
import { Download, FileClock } from "lucide-react";
import { useState } from "react";
import { api, unwrap } from "@/api/client";
import { Button } from "@/components/ui/button/button";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import type { DateRangeFacet, FacetOption } from "@/components/ui/filter-bar/filter-bar";
import { toast } from "@/components/ui/toast/toast";

export { one } from "../hooks";
export { rangeWindow } from "@/lib/date-window";

/** Everyone who has signed in, as filter options (the first 200 by email). */
export function usePeopleOptions(): FacetOption[] {
  const users = useQuery({
    queryKey: ["admin", "users", "options"],
    queryFn: async () => unwrap(await api.GET("/v1/admin/users", { params: { query: { limit: 200 } } })),
    staleTime: 60_000,
  });
  return (users.data?.items ?? []).map((u) => ({ value: u.id, label: u.displayName ? `${u.displayName} (${u.email})` : u.email }));
}

/** The Date facet: nothing after today can be picked, since nothing is logged there yet. */
export function dateFacet<T>(): DateRangeFacet<T> {
  return { id: "range", label: "Date", type: "date-range", pickerProps: { max: new Date() } };
}

/** What a filter that matches nothing shows: one sentence in the log's own words. */
export const noMatches = <EmptyState size="compact" icon={<FileClock />} title="No entries match these filters." description="Clear a filter to see more." />;

/** Quotes a CSV field when needed (RFC 4180). */
export const csvField = (v: unknown) => {
  const s = v === null || v === undefined ? "" : typeof v === "string" ? v : JSON.stringify(v);
  return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
};

export const toCSV = (header: string[], rows: unknown[][]) => [header, ...rows].map((r) => r.map(csvField).join(",")).join("\r\n") + "\r\n";

/** Most rows one export reads (pages of 200). */
export const exportCap = 10_000;

type Page<T> = { items: T[]; nextCursor?: string | null };

/**
 * An "Export CSV" button: reads every page with the current filters (up to
 * exportCap rows) and downloads them.
 */
export function ExportButton<T>({ fetchPage, header, row, filename }: { fetchPage: (cursor?: string) => Promise<Page<T>>; header: string[]; row: (e: T) => unknown[]; filename: string }) {
  const [busy, setBusy] = useState(false);
  const run = async () => {
    setBusy(true);
    try {
      const all: T[] = [];
      let cursor: string | undefined;
      do {
        const page = await fetchPage(cursor);
        all.push(...page.items);
        cursor = page.nextCursor ?? undefined;
      } while (cursor && all.length < exportCap);
      const blob = new Blob([toCSV(header, all.slice(0, exportCap).map(row))], { type: "text/csv" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = filename;
      a.click();
      URL.revokeObjectURL(url);
      toast.success(
        all.length > exportCap ? `Exported the first ${exportCap.toLocaleString()} entries` : `Exported ${all.length.toLocaleString()} ${all.length === 1 ? "entry" : "entries"}`,
      );
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Couldn't export the log");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Button size="sm" variant="secondary" loading={busy} onClick={() => void run()}>
      <Download aria-hidden /> Export CSV
    </Button>
  );
}
