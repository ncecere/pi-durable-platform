/*
 * DetailPage (D3): the one layout for a container's page (source, KB,
 * agent, team, user).
 *
 *   header   title · meta badges · secondary buttons · one primary action · "…" menu
 *            (on a phone the secondary buttons join the "…" menu: PageActions)
 *   facts    one FactsLine ("Web · 42 documents · Synced 3 h ago")
 *   notices  alerts about the object (archived, paused, errors)
 *   tabs     pill tabs in ?tab=: Overview · content · type-specific · Settings
 *
 * "settings" is always moved to the end, destructive menu actions come last,
 * and the breadcrumbs follow the active tab (PageTabs sets the crumb tail).
 */
import type { ReactNode } from "react";
import { type Fact, FactsLine } from "@/components/ui/description-list/description-list";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { type PageTab, PageTabs, useUrlTab } from "../page-tabs";
import s from "../../pages/shared.module.css";
import { type ActionItem } from "./action-menu";
import { PageActions } from "./page-actions";

export type DetailPageProps<T extends string> = {
  title: ReactNode;
  /** Status badges next to the title. */
  meta?: ReactNode;
  description?: ReactNode;
  /** The one fact line under the title. Empty values are dropped. */
  facts?: Fact[];
  /** The one contextual primary action, e.g. <Button>Sync now</Button>. */
  primaryAction?: ReactNode;
  /** Secondary buttons before the primary (Chat, Try it); on a phone they move into the "…" menu. */
  secondaryActions?: ActionItem[];
  /** Secondary actions in the "…" menu (Pause, Duplicate, Delete…); destructive ones last. */
  menuActions?: ActionItem[];
  /** Name of the "…" button (default "More actions"). */
  menuLabel?: string;
  /** Alerts under the header. */
  notices?: ReactNode;
  /** The tab ids, in the route's order (lib/tabs.ts); the first is the default. */
  tabIds: readonly T[];
  /** The tabs' content; ids missing from `tabs` are skipped. "settings" goes last. */
  tabs: PageTab<T>[];
  /** Accessible name of the tab list, e.g. "Source sections". */
  tabsLabel: string;
  /** Content before the tabs (rare: prefer an Overview tab). */
  children?: ReactNode;
};

/** Tabs in display order: as given, with "settings" moved last. */
export function orderTabs<T extends string>(tabs: PageTab<T>[]): PageTab<T>[] {
  return [...tabs.filter((t) => t.value !== "settings"), ...tabs.filter((t) => t.value === "settings")];
}

const hasValue = (f: Fact) => f.value !== undefined && f.value !== null && f.value !== "" && f.value !== false;

export function DetailPage<T extends string>({
  title,
  meta,
  description,
  facts,
  primaryAction,
  secondaryActions,
  menuActions = [],
  menuLabel = "More actions",
  notices,
  tabIds,
  tabs,
  tabsLabel,
  children,
}: DetailPageProps<T>) {
  const [tab, setTab] = useUrlTab(tabIds);
  const shownFacts = (facts ?? []).filter(hasValue);
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader
        title={title}
        meta={meta}
        description={description}
        facts={shownFacts.length > 0 ? <FactsLine items={shownFacts} /> : undefined}
        actions={
          primaryAction || [...(secondaryActions ?? []), ...menuActions].some((a) => !a.hidden) ? (
            <PageActions primary={primaryAction} secondary={secondaryActions} menu={menuActions} menuLabel={menuLabel} />
          ) : undefined
        }
      />
      {notices}
      {children}
      <PageTabs label={tabsLabel} value={tab} onValueChange={setTab} tabs={orderTabs(tabs)} />
    </Stack>
  );
}
