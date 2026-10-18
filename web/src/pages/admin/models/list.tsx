/*
 * Admin › Models: pill tabs Providers | Models (?tab=). Providers: the APIs
 * models come from (platform admins add them here). Models: every model of
 * every provider with who may use it. Auditors read both.
 */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Boxes, Cpu, Plus } from "lucide-react";
import { useState } from "react";
import { adminModelsQuery, providerKindsQuery, providersQuery } from "@/api/models";
import { ModelCatalogTabs } from "@/components/models/catalog-tabs";
import { Button } from "@/components/ui/button/button";
import { useIsPlatformAdmin } from "../hooks";
import { AddProviderDialog } from "./add-provider-dialog";
import { modelColumns, type ProviderLink, providerColumns } from "./columns";
import { whatIsAProvider } from "./labels";

export { AccessBadge } from "./columns";

const providerLink: ProviderLink = (providerId, search = {}) => <Link to="/admin/models/providers/$providerId" params={{ providerId }} search={search} />;

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
    <ModelCatalogTabs
      title="Models"
      description="The models agents run on, from the providers they come from. Publish each model to every team or to chosen teams."
      actions={{ providers: add || undefined }}
      providers={{
        id: "admin-model-providers",
        caption: "Providers",
        columns: providerColumns(providerLink, kinds.data),
        data: providers.data ?? [],
        getRowId: (p) => p.id,
        rowLabel: (p) => p.name,
        loading: providers.isLoading,
        error: providers.error,
        onRetry: () => void providers.refetch(),
        rowActions: (p) => [
          { label: "Open", render: providerLink(p.id) },
          { label: "Settings", render: providerLink(p.id, { tab: "settings" }) },
        ],
        empty: { icon: <Boxes />, title: "No providers yet.", description: whatIsAProvider, action: add || undefined },
        tableProps: { columnsMenuMin: 5 },
      }}
      models={{
        id: "admin-models-all",
        caption: "Every model",
        columns: modelColumns(providerLink),
        data: models.data ?? [],
        getRowId: (m) => m.id,
        rowLabel: (m) => m.name,
        search: { label: "Search models", placeholder: "Name, model ID or provider" },
        loading: models.isLoading,
        error: models.error,
        onRetry: () => void models.refetch(),
        rowActions: (m) => [
          { label: "Open provider", render: providerLink(m.providerId) },
          { label: "Edit", hidden: !isAdmin, render: providerLink(m.providerId, { form: m.id }) },
        ],
        empty: { icon: <Cpu />, title: "No models yet.", description: "Open a provider on the Providers tab to discover its models or add them by hand." },
      }}
    >
      {adding && <AddProviderDialog onClose={() => setAdding(false)} />}
    </ModelCatalogTabs>
  );
}
