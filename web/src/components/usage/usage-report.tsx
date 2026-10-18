/*
 * A usage report (Team › Usage, Admin › Usage): the period and "Group by"
 * controls (both in the URL), the totals, tokens per day as bars when grouped
 * by day, and a table of the rows. The pages pass the report query and the
 * table's first column (the day, model, agent or team).
 */
import type { UseQueryResult } from "@tanstack/react-query";
import { ArrowDownToLine, ArrowUpFromLine, ChartColumn, DollarSign, Send } from "lucide-react";
import { type ReactNode, useId } from "react";
import type { UsageReport, UsageRow } from "@/api/usage";
import { type DateRangeParam, DateRangeFilter } from "@/components/templates/date-range-filter";
import { ListPage } from "@/components/templates/list-page";
import { BarChart } from "@/components/ui/bar-chart/bar-chart";
import { Card } from "@/components/ui/card/card";
import type { DataTableColumn } from "@/components/ui/data-table/data-table";
import { Stack } from "@/components/ui/layout/layout";
import { Skeleton } from "@/components/ui/skeleton/skeleton";
import { StatCard } from "@/components/ui/stat-card/stat-card";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group/toggle-group";
import { tokens, usd } from "@/lib/format";
import s from "../../pages/shared.module.css";
import { numberColumns } from "./columns";
import { costEstimate, daysBetween, dayLabel, type GroupOption, rowId, shortDay, unpricedText } from "./labels";
import u from "./usage.module.css";

type ControlsProps<G extends string> = {
  range: DateRangeParam;
  groupBy: G;
  options: readonly GroupOption<G>[];
  onGroupBy: (next: G) => void;
};

/** Period presets (or a custom range) and Group by. */
export function UsageControls<G extends string>({ range, groupBy, options, onGroupBy }: ControlsProps<G>) {
  const periodId = useId();
  const groupId = useId();
  return (
    <div className={u.controls}>
      <div className={u.control}>
        <span id={periodId} className={u.label}>
          Period
        </span>
        <DateRangeFilter range={range} label="Period" />
      </div>
      <div className={u.control}>
        <span id={groupId} className={u.label}>
          Group by
        </span>
        <ToggleGroup
          aria-labelledby={groupId}
          size="sm"
          variant="outline"
          joined
          value={[groupBy]}
          onValueChange={(next: string[]) => {
            // Pressing the current choice again keeps it.
            const picked = options.find((o) => o.value === next[next.length - 1]);
            if (picked) onGroupBy(picked.value);
          }}
        >
          {options.map((o) => (
            <ToggleGroupItem key={o.value} value={o.value}>
              {o.label}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
      </div>
    </div>
  );
}

/** Requests, input and output tokens, and the estimated cost of the whole period. */
export function UsageTotals({ total, loading }: { total?: UsageRow; loading?: boolean }) {
  if (loading || !total) {
    return (
      <div className={s.stats} role="status" aria-label="Loading usage…">
        <Skeleton height="6rem" />
        <Skeleton height="6rem" />
        <Skeleton height="6rem" />
        <Skeleton height="6rem" />
      </div>
    );
  }
  return (
    <div className={s.stats}>
      <StatCard label="Requests" value={total.requests.toLocaleString()} icon={<Send />} />
      <StatCard
        label="Input tokens"
        value={tokens(total.inputTokens)}
        icon={<ArrowUpFromLine />}
        hint={total.cacheReadTokens > 0 ? `${tokens(total.cacheReadTokens)} read from cache` : undefined}
      />
      <StatCard label="Output tokens" value={tokens(total.outputTokens)} icon={<ArrowDownToLine />} />
      <StatCard label="Estimated cost" value={usd(total.costUsd)} icon={<DollarSign />} hint="At current model prices" />
    </div>
  );
}

/** The muted note under the totals when some requests have no price. */
export function UnpricedNote({ total }: { total?: UsageRow }) {
  if (!total || total.unpricedRequests <= 0) return null;
  return <p className={s.note}>{unpricedText(total.unpricedRequests)}</p>;
}

/** Input and output tokens per day, every day of the period (days without usage are empty). */
export function TokensPerDay({ report }: { report: UsageReport }) {
  const byDay = new Map(report.rows.map((r) => [r.day ?? "", r]));
  const data = daysBetween(report.from, report.to).map((day) => {
    const r = byDay.get(day);
    return { label: shortDay(day), values: { input: r?.inputTokens ?? 0, output: r?.outputTokens ?? 0 } };
  });
  const busiest = report.rows.reduce<UsageRow | undefined>(
    (best, r) => (!best || r.inputTokens + r.outputTokens > best.inputTokens + best.outputTokens ? r : best),
    undefined,
  );
  const sum = report.total.inputTokens + report.total.outputTokens;
  const summary = busiest
    ? `Tokens per day: ${tokens(sum)} in total, most on ${dayLabel(busiest.day)} (${tokens(busiest.inputTokens + busiest.outputTokens)})`
    : "Tokens per day: no usage in this period";
  return (
    <Card title="Tokens per day" description={`${dayLabel(report.from)} to ${dayLabel(report.to)} (UTC).`}>
      <BarChart
        data={data}
        series={[
          { key: "input", label: "Input tokens", tone: "primary" },
          { key: "output", label: "Output tokens", tone: "info" },
        ]}
        layout="stack"
        summary={summary}
        formatValue={tokens}
      />
    </Card>
  );
}

type ReportProps<G extends string> = ControlsProps<G> & {
  /** Remembers hidden columns, per grouping. */
  id: string;
  title: string;
  description: ReactNode;
  report: UseQueryResult<UsageReport>;
  /** The table's first column for the current grouping (the day, model, agent or team). */
  firstColumn: DataTableColumn<UsageRow>;
  rowLabel: (r: UsageRow) => string;
  /** Above the controls, e.g. "Only <agent>" with a way to clear it. */
  notices?: ReactNode;
};

/** A usage page: header, controls, totals, the daily bars (by day) and the rows. */
export function UsageReportPage<G extends string>({ id, title, description, report, firstColumn, rowLabel, notices, ...controls }: ReportProps<G>) {
  const data = report.data;
  const groupBy = controls.groupBy;
  // A report of another grouping (the previous one, while the new one loads) isn't shown with these columns.
  const current = data && data.groupBy === groupBy ? data : undefined;
  return (
    <ListPage<UsageRow>
      id={`${id}-${groupBy}`}
      title={title}
      description={description}
      notices={
        <Stack gap={4}>
          {notices}
          <UsageControls {...controls} />
          {!report.error && (
            <Stack gap={2}>
              <UsageTotals total={current?.total} loading={!current} />
              <UnpricedNote total={current?.total} />
            </Stack>
          )}
          {groupBy === "day" && current && current.rows.length > 0 && <TokensPerDay report={current} />}
        </Stack>
      }
      caption={`Usage by ${groupBy}`}
      columns={[firstColumn, ...numberColumns]}
      data={current?.rows ?? []}
      getRowId={(r) => rowId(groupBy as UsageReport["groupBy"], r)}
      rowLabel={rowLabel}
      loading={!current && !report.error}
      error={report.error}
      onRetry={() => void report.refetch()}
      empty={{ icon: <ChartColumn />, title: "No usage in this period.", description: `Requests to models in the period show here, with their tokens and cost. ${costEstimate}` }}
      tableProps={{ columnsMenuMin: 5 }}
    />
  );
}
