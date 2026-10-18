/*
 * Admin › Models: the providers (platform admins add them here), then every
 * model of every provider with who may use it. Auditors read both.
 */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Boxes, Cpu, Plus } from "lucide-react";
import { useState } from "react";
import { type Model, type ModelProvider, adminModelsQuery, providerKindsQuery, providersQuery } from "@/api/models";
import { EnabledBadge } from "@/components/enabled-badge";
import { ListPage } from "@/components/templates/list-page";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import { Stack } from "@/components/ui/layout/layout";
import { TextLink } from "@/components/ui/text-link/text-link";
import { cx } from "@/lib/bitop-utils";
import s from "../../shared.module.css";
import { useIsPlatformAdmin } from "../hooks";
import { AddProviderDialog } from "./add-provider-dialog";
import { accessLabels, kindLabel, publishedTo, tokens, whatIsAProvider } from "./labels";

const providerLink = (providerId: string, search: Record<string, string> = {}) => (
  <Link to="/admin/models/providers/$providerId" params={{ providerId }} search={search} />
);

export function AccessBadge({ mode }: { mode: ModelProvider["accessMode"] }) {
  return <Badge tone={mode === "proxy" ? "info" : "warning"}>{accessLabels[mode]}</Badge>;
}

function providerColumns(kinds: Parameters<typeof kindLabel>[1]): DataTableColumn<ModelProvider>[] {
  return [
    {
      id: "name",
      header: "Provider",
      accessor: (p) => p.name,
      rowHeader: true,
      hideable: false,
      sortable: true,
      cell: (p) => <CellText className={s.compactCell} wrap="anywhere" primary={<TextLink render={providerLink(p.id)}>{p.name}</TextLink>} secondary={kindLabel(p.kind, kinds)} />,
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

const modelColumns: DataTableColumn<Model>[] = [
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
        primary={<TextLink render={providerLink(m.providerId)}>{m.name}</TextLink>}
        secondary={<span className={s.mono}>{m.upstreamId}</span>}
      />
    ),
  },
  { id: "provider", header: "Provider", accessor: (m) => m.providerName, sortable: true },
  { id: "published", header: "Published to", accessor: publishedTo, sortable: true },
  { id: "context", header: "Context", accessor: (m) => m.contextWindow, numeric: true, sortable: true, defaultHiddenNarrow: true, cell: (m) => tokens(m.contextWindow) },
  { id: "enabled", header: "Status", accessor: (m) => (m.enabled ? "Enabled" : "Disabled"), sortable: true, cell: (m) => <EnabledBadge enabled={m.enabled} /> },
];

/** /admin/models */
export function AdminModelsPage() {
  const isAdmin = useIsPlatformAdmin();
  const kinds = useQuery(providerKindsQuery());
  const providers = useQuery(providersQuery());
  const models = useQuery(adminModelsQuery());
  const [adding, setAdding] = useState(false);
  const add = isAdmin && (
    <Button onClick={() => setAdding(true)}>
      <Plus aria-hidden /> Add provider
    </Button>
  );
  return (
    <Stack gap={8}>
      <ListPage<ModelProvider>
        id="admin-model-providers"
        title="Models"
        description="The models agents run on, from the providers below. Publish each model to every team or to chosen teams."
        primaryAction={add}
        caption="Providers"
        columns={providerColumns(kinds.data)}
        data={providers.data ?? []}
        getRowId={(p) => p.id}
        rowLabel={(p) => p.name}
        loading={providers.isLoading}
        error={providers.error}
        onRetry={() => void providers.refetch()}
        rowActions={(p) => [
          { label: "Open", render: providerLink(p.id) },
          { label: "Settings", render: providerLink(p.id, { tab: "settings" }) },
        ]}
        empty={{ icon: <Boxes />, title: "No providers yet.", description: whatIsAProvider, action: add || undefined }}
        tableProps={{ columnsMenuMin: 5 }}
      />
      <ListPage<Model>
        id="admin-models-all"
        caption="Every model"
        columns={modelColumns}
        data={models.data ?? []}
        getRowId={(m) => m.id}
        rowLabel={(m) => m.name}
        search={{ label: "Search models", placeholder: "Name, model ID or provider" }}
        loading={models.isLoading}
        error={models.error}
        onRetry={() => void models.refetch()}
        rowActions={(m) => [
          { label: "Open provider", render: providerLink(m.providerId) },
          { label: "Edit", hidden: !isAdmin, render: providerLink(m.providerId, { form: m.id }) },
        ]}
        empty={{ icon: <Cpu />, title: "No models yet.", description: "Open a provider to discover its models or add them by hand." }}
      />
      {adding && <AddProviderDialog onClose={() => setAdding(false)} />}
    </Stack>
  );
}
