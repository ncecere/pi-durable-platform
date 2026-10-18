/*
 * Admin › Models: pill tabs Providers | Models (?tab=). Providers: the APIs
 * models come from (platform admins add the platform's here; teams may add
 * their own, shown with their Owner). Models: every model of every provider
 * with its owner and who may use it. The Owner filter (?owner=) narrows both
 * on the server. Auditors read both.
 */
import { useQuery } from "@tanstack/react-query";
import { Boxes, Cpu, Plus } from "lucide-react";
import { useState } from "react";
import { adminModelsQuery, adminScope, providerKindsQuery, providersQuery } from "@/api/models";
import { ModelCatalogTabs } from "@/components/models/catalog-tabs";
import { Button } from "@/components/ui/button/button";
import { useIsPlatformAdmin } from "../hooks";
import { AddProviderDialog } from "./add-provider-dialog";
import { modelColumns, providerColumns } from "./columns";
import { whatIsAProvider } from "./labels";
import { OWNER_PARAM, OwnerFilter, useOwnerParam } from "./owner-filter";
import { providerLinkFor } from "./scope";

export { AccessBadge } from "./columns";

const providerLink = providerLinkFor(adminScope);

/** /admin/models */
export function AdminModelsPage() {
  const isAdmin = useIsPlatformAdmin();
  const [owner, setOwner] = useOwnerParam();
  const kinds = useQuery(providerKindsQuery(adminScope));
  const providers = useQuery(providersQuery(adminScope, owner));
  const models = useQuery(adminModelsQuery(owner));
  const [adding, setAdding] = useState(false);
  const filtered = owner !== "all";
  const add = isAdmin && (
    <Button onClick={() => setAdding(true)}>
      <Plus aria-hidden /> Add provider
    </Button>
  );
  const toolbar = <OwnerFilter value={owner} onChange={setOwner} />;
  const none = { title: "None for this owner.", description: "Choose All owners to see every provider's.", action: undefined };
  return (
    <ModelCatalogTabs
      title="Models"
      description="The models agents run on, from the providers they come from. Publish each platform model to every team or to chosen teams; a team's own providers are for that team only."
      actions={{ providers: add || undefined }}
      keep={[OWNER_PARAM]}
      providers={{
        id: "admin-model-providers",
        caption: "Providers",
        columns: providerColumns(providerLink, kinds.data, { owner: true }),
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
        empty: filtered ? { icon: <Boxes />, ...none } : { icon: <Boxes />, title: "No providers yet.", description: whatIsAProvider, action: add || undefined },
        tableProps: { columnsMenuMin: 5, toolbar },
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
        empty: filtered
          ? { icon: <Cpu />, ...none }
          : { icon: <Cpu />, title: "No models yet.", description: "Open a provider on the Providers tab to discover its models or add them by hand." },
        tableProps: { columnsMenuMin: 5, toolbar },
      }}
    >
      {adding && <AddProviderDialog scope={adminScope} onClose={() => setAdding(false)} />}
    </ModelCatalogTabs>
  );
}
