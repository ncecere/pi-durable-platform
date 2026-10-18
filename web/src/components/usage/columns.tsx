/* The columns of a usage table: the day or the model first, then the numbers. */
import type { UsageRow } from "@/api/usage";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import { tokens, usd } from "@/lib/format";
import s from "../../pages/shared.module.css";
import { dayLabel, modelName } from "./labels";

/** The numbers of a row, after its first column. */
export const numberColumns: DataTableColumn<UsageRow>[] = [
  { id: "requests", header: "Requests", accessor: (r) => r.requests, numeric: true, sortable: true, cell: (r) => r.requests.toLocaleString() },
  { id: "input", header: "Input tokens", accessor: (r) => r.inputTokens, numeric: true, sortable: true, cell: (r) => tokens(r.inputTokens) },
  { id: "output", header: "Output tokens", accessor: (r) => r.outputTokens, numeric: true, sortable: true, cell: (r) => tokens(r.outputTokens) },
  {
    id: "cacheRead",
    header: "Cache reads",
    accessor: (r) => r.cacheReadTokens,
    numeric: true,
    sortable: true,
    defaultHidden: true,
    cell: (r) => tokens(r.cacheReadTokens),
  },
  {
    id: "cacheWrite",
    header: "Cache writes",
    accessor: (r) => r.cacheWriteTokens,
    numeric: true,
    sortable: true,
    defaultHidden: true,
    cell: (r) => tokens(r.cacheWriteTokens),
  },
  {
    id: "cost",
    header: "Estimated cost",
    accessor: (r) => r.costUsd,
    numeric: true,
    sortable: true,
    cell: (r) => (
      <span title={r.unpricedRequests > 0 ? `${r.unpricedRequests.toLocaleString()} unpriced requests not included` : undefined}>
        {r.costUsd === 0 && r.unpricedRequests === r.requests && r.requests > 0 ? "—" : usd(r.costUsd)}
        {r.unpricedRequests > 0 && <span className={s.secondary}>{r.unpricedRequests.toLocaleString()} unpriced</span>}
      </span>
    ),
  },
];

/** The day column of a report grouped by day. */
export const dayColumn: DataTableColumn<UsageRow> = {
  id: "day",
  header: "Day (UTC)",
  accessor: (r) => r.day ?? "",
  rowHeader: true,
  hideable: false,
  sortable: true,
  cell: (r) => <span className={s.nowrap}>{dayLabel(r.day)}</span>,
};

/** The model, with its provider; the platform's default model has neither. */
export const modelColumn: DataTableColumn<UsageRow> = {
  id: "model",
  header: "Model",
  accessor: modelName,
  rowHeader: true,
  hideable: false,
  sortable: true,
  cell: (r) => (
    <CellText
      className={s.compactCell}
      wrap="anywhere"
      primary={r.modelId ? modelName(r) : <span className={s.muted}>{modelName(r)}</span>}
      secondary={r.modelId ? (r.providerName ?? undefined) : undefined}
    />
  ),
};
