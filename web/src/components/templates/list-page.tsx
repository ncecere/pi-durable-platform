/*
 * ListPage (D5): every list and log. A DataTable with the filters and search
 * text in the URL (linkable, Back-able), a row "…" menu with delete last,
 * pagination, remembered column visibility and an empty state.
 *
 *   <ListPage id="team-sources" title="Data sources" primaryAction={<Button>New source</Button>}
 *     caption="Data sources" columns={columns} data={sources} getRowId={(r) => r.id}
 *     rowLabel={(r) => r.name} facets={facets} search={{ label: "Search sources" }}
 *     rowActions={(r) => [{ label: "Open", render: <Link … /> }, { label: "Delete", danger: true, onSelect: … }]}
 *     onRowClick={(r) => record.open(r.id)}   // rows that open a RecordPage
 *     empty={{ icon: <Database />, title: "No data sources yet.", action: <Button>New source</Button> }} />
 *
 * Without `title` it renders only the list (for a list inside a tab or card).
 * For server-side filtering read the same URL state with useListFilters(facets)
 * and pass `manual` plus the already-filtered rows.
 */
import type { ReactNode } from "react";
import { type DataTableColumn, DataTable, type DataTableProps } from "@/components/ui/data-table/data-table";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import { type Facet, type FilterValues, filterValuesFromSearchParams, filterValuesToSearchParams } from "@/components/ui/filter-bar/filter-bar";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { Time } from "@/components/ui/time/time";
import { formatDate, toDate } from "@/lib/bitop-format";
import { needsCalendarDay, relativeDay } from "@/lib/relative-day";
import { useSearchParams } from "@/lib/url-search";
import s from "../../pages/shared.module.css";
import { ActionMenu, type ActionItem } from "./action-menu";

/** The search-text URL parameter. */
export const SEARCH_PARAM = "q";

/** Facet values and search text from the URL, and setters that write them back (replacing history). */
export function useListFilters<T>(facets: Facet<T>[] = []) {
  const [params, setParams] = useSearchParams();
  const values = filterValuesFromSearchParams(facets, params);
  const query = params.get(SEARCH_PARAM) ?? "";
  const setValues = (next: FilterValues) => setParams((p) => filterValuesToSearchParams(facets, next, p));
  const setQuery = (next: string) =>
    setParams((p) => {
      const out = new URLSearchParams(p);
      if (next) out.set(SEARCH_PARAM, next);
      else out.delete(SEARCH_PARAM);
      return out;
    });
  return { values, setValues, query, setQuery };
}

/** A relative date cell ("3 hours ago"), with the full date as a tooltip. */
export function RelativeTime({ value }: { value: string | Date | null | undefined }) {
  // Days count by the calendar ("yesterday" is the day before today, v0.4.2 US-07).
  const date = needsCalendarDay(value) ? toDate(value) : undefined;
  if (date)
    return (
      <time dateTime={date.toISOString()} title={formatDate(date, { style: "long" })}>
        {relativeDay(date)}
      </time>
    );
  return <Time value={value} format="relative" fallback="—" />;
}

/** A sortable column showing a relative date. */
export function timeColumn<T>(id: string, header: string, get: (row: T) => string | null | undefined): DataTableColumn<T> {
  return {
    id,
    header,
    accessor: (row) => {
      const v = get(row);
      return v ? new Date(v) : null;
    },
    sortable: true,
    filterable: false,
    // One line: "3 hours ago" wrapping in a narrow column made rows twice as tall on a phone.
    cell: (row) => (
      <span className={s.nowrap}>
        <RelativeTime value={get(row)} />
      </span>
    ),
  };
}

export type ListEmpty = { icon?: ReactNode; title: ReactNode; description?: ReactNode; action?: ReactNode };

export type ListPageProps<T> = {
  /** Stable id: remembers the column choice (localStorage "pdp.columns.<id>"). */
  id: string;
  /** Page heading; omit to render just the list (inside a tab or card). */
  title?: ReactNode;
  description?: ReactNode;
  primaryAction?: ReactNode;
  /** Alerts between the header and the list. */
  notices?: ReactNode;
  caption: string;
  columns: DataTableColumn<T>[];
  data: T[];
  getRowId: (row: T) => string;
  /** Names a row for its menu and checkbox. */
  rowLabel: (row: T) => string;
  facets?: Facet<T>[];
  /**
   * A search box synced to ?q= (in-memory over the columns' accessors unless `manual`). Its label is for screen
   * readers unless `showLabel`, which shows it above the box like the filters' labels.
   */
  search?: { label: string; placeholder?: string; showLabel?: boolean };
  /** The row "…" menu; destructive actions are placed last. */
  rowActions?: (row: T) => ActionItem[];
  /**
   * Clicking a row (or Enter on the focused row) opens it, usually its
   * RecordPage: `onRowClick={(r) => record.open(r.id)}`. Keep "View
   * details" in the row menu too.
   */
  onRowClick?: (row: T) => void;
  /** Shown when there are no rows at all. */
  empty?: ListEmpty;
  /** Rows per page (default 25). */
  pageSize?: number;
  loading?: boolean;
  error?: unknown;
  onRetry?: () => void;
  /** Rows are already filtered by the server (use useListFilters for the values). */
  manual?: boolean;
  /** Anything else DataTable takes (loadMore, cursor, selectable, bulkActions, toolbar…). */
  tableProps?: Partial<DataTableProps<T>>;
};

export function ListPage<T>({
  id,
  title,
  description,
  primaryAction,
  notices,
  caption,
  columns,
  data,
  getRowId,
  rowLabel,
  facets,
  search,
  rowActions,
  onRowClick,
  empty,
  pageSize = 25,
  loading,
  error,
  onRetry,
  manual,
  tableProps,
}: ListPageProps<T>) {
  const filters = useListFilters(facets);
  const table = (
    <DataTable<T>
      caption={caption}
      columns={columns}
      data={data}
      getRowId={getRowId}
      rowLabel={rowLabel}
      columnsMenu
      columnsStorageKey={`pdp.columns.${id}`}
      facets={facets}
      facetValues={filters.values}
      onFacetValuesChange={filters.setValues}
      filterable={Boolean(search)}
      filterLabel={search?.label}
      showFilterLabel={search?.showLabel}
      filterPlaceholder={search?.placeholder}
      filter={search ? filters.query : undefined}
      onFilterChange={search ? filters.setQuery : undefined}
      pageSize={tableProps?.loadMore || tableProps?.cursor ? undefined : pageSize}
      loading={loading}
      error={error}
      onRetry={onRetry}
      manual={manual}
      rowActions={rowActions ? (row) => <ActionMenu actions={rowActions(row)} label={`Actions for ${rowLabel(row)}`} /> : undefined}
      onRowClick={onRowClick}
      // The row's button (in its row header) is named like its menu and checkbox.
      rowClickLabel={onRowClick ? rowLabel : undefined}
      empty={empty ? <EmptyState size="compact" icon={empty.icon} title={empty.title} description={empty.description} action={empty.action} /> : undefined}
      {...tableProps}
    />
  );
  if (title === undefined) return table;
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader title={title} description={description} actions={primaryAction} />
      {notices}
      {table}
    </Stack>
  );
}
