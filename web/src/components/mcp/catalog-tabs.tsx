/*
 * An MCP server catalog page: Servers | Available as pill tabs (?tab=,
 * lib/tabs.ts mcpCatalogTabs), under one header whose actions follow the tab
 * (Add server on Servers), as ModelCatalogTabs does for models. Team › MCP
 * servers uses it: its own servers, and every server it may use.
 */
import type { ReactNode } from "react";
import type { McpServer } from "@/api/mcp";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { type McpCatalogTab, mcpCatalogTabs } from "@/lib/tabs";
import s from "../../pages/shared.module.css";
import { PageTabs, useUrlTab } from "../page-tabs";
import type { CatalogList } from "../models/catalog-tabs";
import { ListPage } from "../templates/list-page";

export type McpCatalogTabsProps = {
  title: ReactNode;
  description?: ReactNode;
  servers: CatalogList<McpServer>;
  available: CatalogList<McpServer>;
  /** The header's actions on each tab, e.g. { servers: <Button>Add server</Button> }. */
  actions?: Partial<Record<McpCatalogTab, ReactNode>>;
  /** Alerts under the header, on every tab. */
  notices?: ReactNode;
  /** Alerts under the header on one tab, e.g. { servers: <Alert>Why you can't add one</Alert> }. */
  tabNotices?: Partial<Record<McpCatalogTab, ReactNode>>;
  /** Dialogs and other content outside the tabs. */
  children?: ReactNode;
};

const countOf = <T,>(list: CatalogList<T>) => (list.loading || list.error ? undefined : list.data.length);

export function McpCatalogTabs({ title, description, servers, available, actions, notices, tabNotices, children }: McpCatalogTabsProps) {
  const [tab, setTab] = useUrlTab(mcpCatalogTabs);
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader title={title} description={description} actions={actions?.[tab] || undefined} />
      {notices}
      {tabNotices?.[tab]}
      <PageTabs<McpCatalogTab>
        label="MCP server catalog sections"
        value={tab}
        onValueChange={setTab}
        tabs={[
          { value: "servers", label: "Servers", count: countOf(servers), content: <ListPage<McpServer> {...servers} /> },
          { value: "available", label: "Available", crumb: "Available servers", count: countOf(available), content: <ListPage<McpServer> {...available} /> },
        ]}
      />
      {children}
    </Stack>
  );
}
