/* A provider's Models tab: each model with its limits, prices and who may use it (a platform model); those who manage it edit (a form page) and delete. */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Cpu, Plus, Search } from "lucide-react";
import { useState } from "react";
import { deleteModel, invalidateModels, type Model, type ModelProvider, type ModelScope, providerModelsQuery } from "@/api/models";
import { ConfirmMutationDialog } from "@/components/confirm-dialog";
import { EnabledBadge } from "@/components/enabled-badge";
import { ListPage } from "@/components/templates/list-page";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import { Inline } from "@/components/ui/layout/layout";
import { toast } from "@/components/ui/toast/toast";
import { tokens } from "@/lib/format";
import s from "../../shared.module.css";
import { price, publishedTo } from "./labels";
import m from "./models.module.css";
import { isTeamOwned } from "./scope";

const columns: DataTableColumn<Model>[] = [
  {
    id: "name",
    header: "Model",
    accessor: (x) => x.name,
    rowHeader: true,
    hideable: false,
    sortable: true,
    cell: (x) => <CellText className={s.compactCell} wrap="anywhere" primary={x.name} secondary={<span className={s.mono}>{x.upstreamId}</span>} />,
  },
  { id: "context", header: "Context", accessor: (x) => x.contextWindow, numeric: true, sortable: true, cell: (x) => tokens(x.contextWindow) },
  { id: "maxTokens", header: "Max output", accessor: (x) => x.maxTokens, numeric: true, sortable: true, defaultHidden: true, cell: (x) => tokens(x.maxTokens) },
  {
    id: "capabilities",
    header: "Accepts",
    accessor: (x) => [x.images && "Images", x.reasoning && "Reasoning"].filter(Boolean).join(", "),
    defaultHiddenNarrow: true,
    cell: (x) => (
      <span className={m.capabilities}>
        <Badge size="sm">Text</Badge>
        {x.images && <Badge size="sm">Images</Badge>}
        {x.reasoning && <Badge size="sm">Reasoning</Badge>}
      </span>
    ),
  },
  {
    id: "price",
    header: "Price in / out",
    accessor: (x) => x.inputPrice ?? -1,
    sortable: true,
    defaultHidden: true,
    cell: (x) => <span className={s.nowrap}>{x.inputPrice == null && x.outputPrice == null ? "—" : `${price(x.inputPrice)} / ${price(x.outputPrice)}`}</span>,
  },
  { id: "published", header: "Published to", accessor: publishedTo, sortable: true },
  { id: "enabled", header: "Status", accessor: (x) => (x.enabled ? "Enabled" : "Disabled"), sortable: true, cell: (x) => <EnabledBadge enabled={x.enabled} /> },
];

type Props = { scope: ModelScope; provider: ModelProvider; canManage: boolean; onDiscover: () => void; onAdd: () => void; onEdit: (id: string) => void };

export function ProviderModelsTab({ scope, provider, canManage, onDiscover, onAdd, onEdit }: Props) {
  const qc = useQueryClient();
  const models = useQuery(providerModelsQuery(scope, provider.id));
  const teamOwned = isTeamOwned(scope, provider);
  const [deleting, setDeleting] = useState<Model | null>(null);
  const del = useMutation({
    mutationFn: async (x: Model) => deleteModel(scope, x.id),
    onSuccess: (_, x) => {
      setDeleting(null);
      toast.success(`${x.name} was deleted`);
      void invalidateModels(qc, scope);
    },
  });
  const add = canManage && (
    <Inline gap={2}>
      <Button size="sm" onClick={onDiscover}>
        <Search aria-hidden /> Discover models
      </Button>
      <Button size="sm" variant="secondary" onClick={onAdd}>
        <Plus aria-hidden /> Add model manually
      </Button>
    </Inline>
  );
  return (
    <Card
      title="Models"
      description={`Prices are in US dollars per million tokens. ${teamOwned ? `Only ${provider.team?.name ?? "the team"} can use them, once enabled.` : "Teams see a model once it's enabled and published to them."}`}
      actions={(models.data?.length ?? 0) > 0 ? add : undefined}
    >
      <ListPage<Model>
        id={teamOwned ? "team-provider-models" : "admin-provider-models"}
        caption={`Models of ${provider.name}`}
        columns={teamOwned ? columns.filter((c) => c.id !== "published") : columns}
        data={models.data ?? []}
        getRowId={(x) => x.id}
        rowLabel={(x) => x.name}
        loading={models.isLoading}
        error={models.error}
        onRetry={() => void models.refetch()}
        onRowClick={canManage ? (x) => onEdit(x.id) : undefined}
        rowActions={
          canManage
            ? (x) => [
                { label: "Edit", onSelect: () => onEdit(x.id) },
                { label: "Delete…", danger: true, onSelect: () => setDeleting(x) },
              ]
            : undefined
        }
        empty={{ icon: <Cpu />, title: "No models yet.", description: canManage ? "Ask the provider which models it offers, or add one by its model ID." : undefined, action: add || undefined }}
        tableProps={{ columnsMenuMin: 5 }}
      />
      <ConfirmMutationDialog
        target={deleting}
        onClose={() => setDeleting(null)}
        mutation={del}
        onConfirm={(x) => del.mutate(x)}
        title={`Delete ${deleting?.name ?? "model"}?`}
        description={`${teamOwned ? "The team can't" : "Teams can't"} choose it any more. A model templates or agents still use can't be deleted: move them to another model, or turn it off instead.`}
        confirmLabel="Delete model"
      />
    </Card>
  );
}
