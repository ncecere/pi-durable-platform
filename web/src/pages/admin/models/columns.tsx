/*
 * The providers' and models' columns. A page passes how a provider is linked
 * (Admin › Models opens /admin/models/providers/…), so another catalog page
 * (a team's models) can reuse them with its own links and columns added.
 */
import type { ReactElement } from "react";
import type { Model, ModelProvider } from "@/api/models";
import { EnabledBadge } from "@/components/enabled-badge";
import { Badge } from "@/components/ui/badge/badge";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import { TextLink } from "@/components/ui/text-link/text-link";
import { cx } from "@/lib/bitop-utils";
import { tokens } from "@/lib/format";
import s from "../../shared.module.css";
import { accessLabels, kindLabel, publishedTo } from "./labels";

/** A link to a provider's page (rendered by TextLink and row menus). */
export type ProviderLink = (providerId: string, search?: Record<string, string>) => ReactElement;

export function AccessBadge({ mode }: { mode: ModelProvider["accessMode"] }) {
  return <Badge tone={mode === "proxy" ? "info" : "warning"}>{accessLabels[mode]}</Badge>;
}

export function providerColumns(link: ProviderLink, kinds: Parameters<typeof kindLabel>[1]): DataTableColumn<ModelProvider>[] {
  return [
    {
      id: "name",
      header: "Provider",
      accessor: (p) => p.name,
      rowHeader: true,
      hideable: false,
      sortable: true,
      cell: (p) => <CellText className={s.compactCell} wrap="anywhere" primary={<TextLink render={link(p.id)}>{p.name}</TextLink>} secondary={kindLabel(p.kind, kinds)} />,
    },
    { id: "kind", header: "Type", accessor: (p) => kindLabel(p.kind, kinds), sortable: true, defaultHidden: true },
    {
      id: "baseUrl",
      header: "Base URL",
      accessor: (p) => p.baseUrl,
      defaultHiddenNarrow: true,
      cell: (p) => (
        <span className={cx(s.mono, s.truncate)} title={p.baseUrl}>
          {p.baseUrl}
        </span>
      ),
    },
    { id: "access", header: "Access", accessor: (p) => accessLabels[p.accessMode], sortable: true, cell: (p) => <AccessBadge mode={p.accessMode} /> },
    { id: "enabled", header: "Status", accessor: (p) => (p.enabled ? "Enabled" : "Disabled"), sortable: true, cell: (p) => <EnabledBadge enabled={p.enabled} /> },
    { id: "models", header: "Models", accessor: (p) => p.modelCount, numeric: true, sortable: true },
  ];
}

export function modelColumns(link: ProviderLink): DataTableColumn<Model>[] {
  return [
    {
      id: "name",
      header: "Model",
      accessor: (m) => m.name,
      rowHeader: true,
      hideable: false,
      sortable: true,
      cell: (m) => (
        <CellText
          className={s.compactCell}
          wrap="anywhere"
          primary={<TextLink render={link(m.providerId)}>{m.name}</TextLink>}
          secondary={<span className={s.mono}>{m.upstreamId}</span>}
        />
      ),
    },
    { id: "provider", header: "Provider", accessor: (m) => m.providerName, sortable: true },
    { id: "published", header: "Published to", accessor: publishedTo, sortable: true },
    { id: "context", header: "Context", accessor: (m) => m.contextWindow, numeric: true, sortable: true, defaultHiddenNarrow: true, cell: (m) => tokens(m.contextWindow) },
    { id: "enabled", header: "Status", accessor: (m) => (m.enabled ? "Enabled" : "Disabled"), sortable: true, cell: (m) => <EnabledBadge enabled={m.enabled} /> },
  ];
}
