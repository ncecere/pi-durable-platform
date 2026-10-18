/*
 * A model catalog page: Providers | Models as pill tabs (?tab=, lib/tabs.ts
 * modelCatalogTabs), under one header whose actions follow the tab (Add
 * provider on Providers). Admin › Models uses it; a team's models page can
 * pass its own lists, columns and actions (an Owner column, its own links).
 *
 *   <ModelCatalogTabs title="Models" description="…"
 *     providers={{ id: "admin-model-providers", columns, data, … }}
 *     models={{ id: "admin-models-all", columns, data, … }}
 *     actions={{ providers: <Button>Add provider</Button> }} />
 */
import type { ReactNode } from "react";
import type { Model, ModelProvider } from "@/api/models";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { type ModelCatalogTab, modelCatalogTabs } from "@/lib/tabs";
import s from "../../pages/shared.module.css";
import { PageTabs, useUrlTab } from "../page-tabs";
import { ListPage, type ListPageProps } from "../templates/list-page";

/** One tab's list: everything ListPage takes but its header (the page has one). */
export type CatalogList<T> = Omit<ListPageProps<T>, "title" | "description" | "primaryAction" | "notices">;

export type ModelCatalogTabsProps = {
  title: ReactNode;
  description?: ReactNode;
  providers: CatalogList<ModelProvider>;
  models: CatalogList<Model>;
  /** The header's actions on each tab, e.g. { providers: <Button>Add provider</Button> }. */
  actions?: Partial<Record<ModelCatalogTab, ReactNode>>;
  /** Alerts under the header, on every tab. */
  notices?: ReactNode;
  /** Dialogs and other content outside the tabs. */
  children?: ReactNode;
};

/** The count on a tab once its list has loaded. */
const countOf = <T,>(list: CatalogList<T>) => (list.loading || list.error ? undefined : list.data.length);

export function ModelCatalogTabs({ title, description, providers, models, actions, notices, children }: ModelCatalogTabsProps) {
  const [tab, setTab] = useUrlTab(modelCatalogTabs);
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader title={title} description={description} actions={actions?.[tab] || undefined} />
      {notices}
      <PageTabs<ModelCatalogTab>
        label="Model catalog sections"
        value={tab}
        onValueChange={setTab}
        tabs={[
          { value: "providers", label: "Providers", count: countOf(providers), content: <ListPage<ModelProvider> {...providers} /> },
          { value: "models", label: "Models", crumb: "All models", count: countOf(models), content: <ListPage<Model> {...models} /> },
        ]}
      />
      {children}
    </Stack>
  );
}
