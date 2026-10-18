/*
 * One provider: its models (discover them, add one by hand, edit one on a
 * form page: ?form=new or ?form=<model id>) and its settings. Admin › Models
 * (platform admins change any provider, auditors read) and Team › Models (the
 * team's owners and admins change the team's own, its other members read)
 * share it through a CatalogScope. A team's provider shows its owner in Admin
 * and has no access mode or publishing (scope.tsx).
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { Cpu, Plus, Search, Settings2, Trash2 } from "lucide-react";
import { useState } from "react";
import { adminScope, deleteProvider, invalidateModels, type ModelProvider, providerKindsQuery, providerQuery } from "@/api/models";
import { ConfirmMutationDialog } from "@/components/confirm-dialog";
import { EnabledBadge } from "@/components/enabled-badge";
import { isNotFound, NotFoundState } from "@/components/not-found";
import { PageSkeleton } from "@/components/page-skeleton";
import { DetailPage } from "@/components/templates/detail-page";
import { useFormParam } from "@/components/templates/form-page";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { toast } from "@/components/ui/toast/toast";
import { plural } from "@/lib/format";
import { providerTabs } from "@/lib/tabs";
import { adminOnly } from "@/lib/terms";
import { useIsPlatformAdmin } from "../hooks";
import { AccessBadge, OwnerCell } from "./columns";
import { DiscoverDialog } from "./discover-dialog";
import { kindLabel } from "./labels";
import { ModelFormHost } from "./model-form";
import { ProviderModelsTab } from "./provider-models";
import { ProviderSettingsTab } from "./provider-settings";
import type { CatalogScope } from "./scope";
import { useCatalogNavigate } from "./scope";

/** /admin/models/providers/$providerId */
export function AdminModelProviderPage() {
  const { providerId } = useParams({ from: "/app/admin/models/providers/$providerId" });
  const isAdmin = useIsPlatformAdmin();
  return <ProviderPage providerId={providerId} catalog={{ scope: adminScope, canManage: isAdmin, readOnlyNote: adminOnly }} />;
}

/** A provider of the scope by id: loading, not found (404), or its page. */
export function ProviderPage({ providerId, catalog }: { providerId: string; catalog: CatalogScope }) {
  const provider = useQuery(providerQuery(catalog.scope, providerId));
  if (provider.isLoading) return <PageSkeleton />;
  if (isNotFound(provider.error)) return <NotFoundState what="object" />;
  if (!provider.data) return <ErrorAlert error={provider.error} title="Couldn't load this provider" onRetry={() => void provider.refetch()} />;
  return <Record provider={provider.data} catalog={catalog} />;
}

function Record({ provider, catalog }: { provider: ModelProvider; catalog: CatalogScope }) {
  const { scope, canManage } = catalog;
  const qc = useQueryClient();
  const go = useCatalogNavigate(scope);
  const kinds = useQuery(providerKindsQuery(scope));
  const form = useFormParam();
  const [discovering, setDiscovering] = useState(false);
  const [deleting, setDeleting] = useState<ModelProvider | null>(null);
  const del = useMutation({
    mutationFn: async () => deleteProvider(scope, provider.id),
    onSuccess: () => {
      setDeleting(null);
      toast.success(`${provider.name} was deleted`, "Its models were removed with it.");
      void go.list().then(() => invalidateModels(qc, scope));
    },
  });
  const actions = {
    discover: () => setDiscovering(true),
    addModel: () => form.open("new"),
    remove: () => setDeleting(provider),
  };
  return (
    <>
      <DetailPage
        title={provider.name}
        meta={
          <>
            <EnabledBadge enabled={provider.enabled} /> <AccessBadge mode={provider.accessMode} />
          </>
        }
        facts={[
          // In Admin: whose it is (a team's own, or the platform's).
          ...(scope.kind === "admin" ? [{ label: "Owner", value: <OwnerCell owner={provider.team} /> }] : []),
          { label: "Type", value: kindLabel(provider.kind, kinds.data) },
          { label: "Models", value: plural(provider.modelCount, "model") },
          { label: "Base URL", value: <code>{provider.baseUrl}</code> },
        ]}
        primaryAction={
          canManage ? (
            <Button onClick={actions.discover}>
              <Search aria-hidden /> Discover models
            </Button>
          ) : undefined
        }
        menuActions={[
          { label: "Add model manually", icon: <Plus aria-hidden />, hidden: !canManage, onSelect: actions.addModel },
          { label: "Delete provider…", icon: <Trash2 aria-hidden />, danger: true, hidden: !canManage, onSelect: actions.remove },
        ]}
        notices={
          !provider.enabled && (
            <Alert tone="warning" title="Turned off">
              Its models take no new use, and agents running on them don&apos;t start until it&apos;s turned on again.
            </Alert>
          )
        }
        tabIds={providerTabs}
        tabsLabel="Provider sections"
        tabs={[
          {
            value: "models",
            label: "Models",
            icon: <Cpu aria-hidden />,
            count: provider.modelCount,
            content: <ProviderModelsTab scope={scope} provider={provider} canManage={canManage} onDiscover={actions.discover} onAdd={actions.addModel} onEdit={form.open} />,
          },
          {
            value: "settings",
            label: "Settings",
            icon: <Settings2 aria-hidden />,
            content: <ProviderSettingsTab catalog={catalog} provider={provider} onDelete={actions.remove} />,
          },
        ]}
      />
      {discovering && <DiscoverDialog scope={scope} provider={provider} onClose={() => setDiscovering(false)} />}
      {canManage && form.id && <ModelFormHost scope={scope} provider={provider} id={form.id} onClose={form.close} />}
      <ConfirmMutationDialog
        target={deleting}
        onClose={() => setDeleting(null)}
        mutation={del}
        onConfirm={() => del.mutate()}
        title={`Delete ${provider.name}?`}
        description="Its key and its models are deleted with it. A provider whose models templates or agents still use can't be deleted: move them to other models first."
        confirmLabel="Delete provider"
      />
    </>
  );
}
