/*
 * Admin › Models › one provider: its models (discover them, add one by hand,
 * edit one on a form page: ?form=new or ?form=<model id>) and its settings.
 * Platform admins change it; auditors read it.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";
import { Cpu, Plus, Search, Settings2, Trash2 } from "lucide-react";
import { useState } from "react";
import { api, unwrap } from "@/api/client";
import { invalidateModels, type ModelProvider, providerKindsQuery, providerQuery } from "@/api/models";
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
import { useIsPlatformAdmin } from "../hooks";
import { DiscoverDialog } from "./discover-dialog";
import { kindLabel } from "./labels";
import { AccessBadge } from "./list";
import { ModelFormHost } from "./model-form";
import { ProviderModelsTab } from "./provider-models";
import { ProviderSettingsTab } from "./provider-settings";

export function AdminModelProviderPage() {
  const { providerId } = useParams({ from: "/app/admin/models/providers/$providerId" });
  const provider = useQuery(providerQuery(providerId));
  if (provider.isLoading) return <PageSkeleton />;
  if (isNotFound(provider.error)) return <NotFoundState what="object" />;
  if (!provider.data) return <ErrorAlert error={provider.error} title="Couldn't load this provider" onRetry={() => void provider.refetch()} />;
  return <Record provider={provider.data} />;
}

function Record({ provider }: { provider: ModelProvider }) {
  const isAdmin = useIsPlatformAdmin();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const kinds = useQuery(providerKindsQuery());
  const form = useFormParam();
  const [discovering, setDiscovering] = useState(false);
  const [deleting, setDeleting] = useState<ModelProvider | null>(null);
  const del = useMutation({
    mutationFn: async () => unwrap(await api.DELETE("/v1/admin/model-providers/{providerId}", { params: { path: { providerId: provider.id } } })),
    onSuccess: () => {
      setDeleting(null);
      toast.success(`${provider.name} was deleted`, "Its models were removed with it.");
      void navigate({ to: "/admin/models", replace: true }).then(() => invalidateModels(qc));
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
          { label: "Type", value: kindLabel(provider.kind, kinds.data) },
          { label: "Models", value: plural(provider.modelCount, "model") },
          { label: "Base URL", value: <code>{provider.baseUrl}</code> },
        ]}
        primaryAction={
          isAdmin ? (
            <Button onClick={actions.discover}>
              <Search aria-hidden /> Discover models
            </Button>
          ) : undefined
        }
        menuActions={[
          { label: "Add model manually", icon: <Plus aria-hidden />, hidden: !isAdmin, onSelect: actions.addModel },
          { label: "Delete provider…", icon: <Trash2 aria-hidden />, danger: true, hidden: !isAdmin, onSelect: actions.remove },
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
            content: <ProviderModelsTab provider={provider} isAdmin={isAdmin} onDiscover={actions.discover} onAdd={actions.addModel} onEdit={form.open} />,
          },
          {
            value: "settings",
            label: "Settings",
            icon: <Settings2 aria-hidden />,
            content: <ProviderSettingsTab provider={provider} isAdmin={isAdmin} onDelete={actions.remove} />,
          },
        ]}
      />
      {discovering && <DiscoverDialog provider={provider} onClose={() => setDiscovering(false)} />}
      {isAdmin && form.id && <ModelFormHost provider={provider} id={form.id} onClose={form.close} />}
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
