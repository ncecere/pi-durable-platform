/*
 * Team › Models (/teams/$team/models): pill tabs Providers | Models (?tab=).
 * Providers: the team's own (proxy only, for this team alone); its owners and
 * admins add one while platform admins allow it (limits.canAddProviders),
 * else a note says why. Models: every model the team may use, the platform's
 * published to it and its own, with their source. Shared teams and personal
 * workspaces alike; other members and platform staff read.
 */
import { useQuery } from "@tanstack/react-query";
import { Boxes, Cpu, Plus } from "lucide-react";
import { useState } from "react";
import { type Model, providerKindsQuery, providersQuery, teamModelsQuery, teamScope } from "@/api/models";
import { workspaceLimitsQuery } from "@/api/personal";
import { ModelCatalogTabs } from "@/components/models/catalog-tabs";
import { Alert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import { TextLink } from "@/components/ui/text-link/text-link";
import { tokens } from "@/lib/format";
import { AddProviderDialog } from "../../admin/models/add-provider-dialog";
import { type ProviderLink, providerColumns } from "../../admin/models/columns";
import { price } from "../../admin/models/labels";
import { providerLinkFor } from "../../admin/models/scope";
import s from "../../shared.module.css";
import { ArchivedNotice } from "../layout";
import { useTeam } from "../common";
import { cannotAddReason, managesProviders, readOnlyNote } from "./access";

/** The team's models: its own linked to their provider, with where each comes from. */
function modelColumns(link: ProviderLink, own: string): DataTableColumn<Model>[] {
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
          primary={m.providerTeam ? <TextLink render={link(m.providerId)}>{m.name}</TextLink> : m.name}
          secondary={<span className={s.mono}>{m.upstreamId}</span>}
        />
      ),
    },
    { id: "provider", header: "Provider", accessor: (m) => m.providerName, sortable: true },
    {
      id: "source",
      header: "Source",
      accessor: (m) => (m.providerTeam ? own : "Platform"),
      sortable: true,
      cell: (m) => (m.providerTeam ? <Badge tone="info">{own}</Badge> : <span className={s.muted}>Platform</span>),
    },
    { id: "context", header: "Context", accessor: (m) => m.contextWindow, numeric: true, sortable: true, defaultHiddenNarrow: true, cell: (m) => tokens(m.contextWindow) },
    {
      id: "price",
      header: "Price in / out",
      accessor: (m) => m.inputPrice ?? -1,
      sortable: true,
      defaultHidden: true,
      cell: (m) => <span className={s.nowrap}>{m.inputPrice == null && m.outputPrice == null ? "—" : `${price(m.inputPrice)} / ${price(m.outputPrice)}`}</span>,
    },
  ];
}

/** /teams/$team/models */
export function TeamModelsPage() {
  const ctx = useTeam();
  const { slug, team, personal } = ctx;
  const scope = teamScope(slug);
  const link = providerLinkFor(scope);
  const kinds = useQuery(providerKindsQuery(scope));
  const providers = useQuery(providersQuery(scope));
  const models = useQuery(teamModelsQuery(slug));
  const limits = useQuery(workspaceLimitsQuery(slug));
  const [adding, setAdding] = useState(false);
  const manages = managesProviders(ctx);
  const canAdd = manages && limits.data?.canAddProviders === true;
  const where = personal ? "this workspace" : "this team";
  const own = personal ? "This workspace" : "This team";
  const add = canAdd && (
    <Button onClick={() => setAdding(true)}>
      <Plus aria-hidden /> Add provider
    </Button>
  );
  // Why there's no Add provider: turned off by platform admins, or not this person's to change.
  const note = canAdd ? null : manages ? (
    limits.data && (
      <Alert tone="info" title="Adding providers is turned off">
        {cannotAddReason(ctx)}
      </Alert>
    )
  ) : (
    <Alert tone="info">{readOnlyNote(ctx)}</Alert>
  );
  return (
    <ModelCatalogTabs
      title="Models"
      description={`The models ${team.name}'s agents may use: the platform's, published to ${where}, and models of ${where}'s own providers, which only ${where} can use.`}
      actions={{ providers: add || undefined }}
      notices={<ArchivedNotice />}
      tabNotices={{ providers: note }}
      providers={{
        id: "team-model-providers",
        caption: "Providers",
        columns: providerColumns(link, kinds.data, { access: false }),
        data: providers.data ?? [],
        getRowId: (p) => p.id,
        rowLabel: (p) => p.name,
        loading: providers.isLoading,
        error: providers.error,
        onRetry: () => void providers.refetch(),
        rowActions: (p) => [
          { label: "Open", render: link(p.id) },
          { label: "Settings", render: link(p.id, { tab: "settings" }) },
        ],
        empty: {
          icon: <Boxes />,
          title: `No providers of ${where}'s own.`,
          description: `A provider of its own gives ${where} more models: an OpenAI or Anthropic key, or a compatible server such as LiteLLM. Agents reach it through the platform, and only ${where} can use its models.`,
          action: add || undefined,
        },
      }}
      models={{
        id: "team-models",
        caption: "Models the team may use",
        columns: modelColumns(link, own),
        data: models.data ?? [],
        getRowId: (m) => m.id,
        rowLabel: (m) => m.name,
        search: { label: "Search models", placeholder: "Name, model ID or provider" },
        loading: models.isLoading,
        error: models.error,
        onRetry: () => void models.refetch(),
        rowActions: (m) => [{ label: "Open provider", hidden: !m.providerTeam, render: link(m.providerId) }],
        empty: { icon: <Cpu />, title: "No models yet.", description: `Platform admins publish models to ${where}; ${where}'s owners and admins can add providers of its own.` },
      }}
    >
      {adding && <AddProviderDialog scope={scope} onClose={() => setAdding(false)} />}
    </ModelCatalogTabs>
  );
}
