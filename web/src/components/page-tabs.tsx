/*
 * Page sections as pill tabs, the one tab style in the app. The active tab is
 * kept in the URL (?tab=) so it can be linked to and back/forward move between
 * tabs; the first tab is the default and leaves the URL clean.
 *
 * A route that uses tabs declares them with tabSearch() (lib/tabs.ts) in its validateSearch.
 */
import { useNavigate, useRouterState, useSearch } from "@tanstack/react-router";
import { type ReactNode, useEffect } from "react";
import { Tab, Tabs, TabsList, TabsPanel } from "@/components/ui/tabs/tabs";
import { useCrumbTail } from "./layout/crumb-tail";
import t from "./page-tabs.module.css";

type SetTab<T extends string> = (next: T, opts?: { replace?: boolean }) => void;

/**
 * The active tab (from ?tab=) and a way to change it. Changing tabs pushes a
 * history entry, so Back returns to the previous tab, and keeps only the
 * page-wide parameters in `keep` (e.g. a date range above the tabs).
 */
export function useUrlTab<T extends string>(tabs: readonly T[], opts: { keep?: readonly string[] } = {}): [T, SetTab<T>] {
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { tab?: string };
  // The address as typed (the route's validateSearch already dropped an unknown ?tab=), once the router has
  // settled on it: while it loads another address (a redirect), that one isn't this page's.
  const typed = useRouterState({
    select: (st) => (st.status === "idle" && st.resolvedLocation?.href === st.location.href ? new URLSearchParams(st.location.searchStr).get("tab") : null),
  });
  const tab = tabs.includes(search.tab as T) ? (search.tab as T) : (tabs[0] as T);
  const keep = opts.keep ?? [];
  const setTab: SetTab<T> = (next, o) =>
    void navigate({
      to: ".",
      search: ((prev: Record<string, unknown>) => {
        const kept = next === tab ? prev : Object.fromEntries(Object.entries(prev).filter(([k]) => keep.includes(k)));
        return { ...kept, tab: next === tabs[0] ? undefined : next };
      }) as never,
      replace: o?.replace ?? false,
    });
  // An unknown ?tab= (or the default tab spelled out): the address is rewritten to the tab shown.
  const unknown = typed !== null && (!tabs.includes(typed as T) || typed === tabs[0]);
  useEffect(() => {
    if (unknown) void navigate({ to: ".", search: ((prev: Record<string, unknown>) => ({ ...prev, tab: undefined })) as never, replace: true });
  }, [unknown, navigate]);
  return [tab, setTab];
}

export type PageTab<T extends string> = {
  value: T;
  label: ReactNode;
  icon?: ReactNode;
  count?: number;
  /** Rendered only while the tab is active. */
  content: ReactNode;
  /** Leave the tab out (e.g. the viewer can't use it). */
  hidden?: boolean;
  /** The breadcrumb for this tab (default: the label when it's text); "" for none yet, while its name is still loading. */
  crumb?: string;
};

type PageTabsProps<T extends string> = {
  /** Accessible name of the tab list, e.g. "Source sections". */
  label: string;
  tabs: PageTab<T>[];
  value: T;
  /** Usually useUrlTab's setter; called with `replace` to move off a tab the viewer doesn't get. */
  onValueChange: (next: T, opts?: { replace?: boolean }) => void;
  /**
   * Whether the tabs' `hidden` flags are final (default true). While a flag
   * waits for data (a tab shown only once a setting has loaded), pass false,
   * so a link to that tab isn't rewritten to the first one in the meantime.
   */
  ready?: boolean;
  /**
   * Which tabs add their name to the breadcrumbs (F-12): "after-first"
   * (default; the first tab is the page itself), "always", or "none" (for
   * tabs nested inside another tabbed page).
   */
  crumb?: "after-first" | "always" | "none";
};

/** Pill tabs over a page's sections. Pair with useUrlTab. */
export function PageTabs<T extends string>({ label, tabs, value, onValueChange, crumb = "after-first", ready = true }: PageTabsProps<T>) {
  const shown = tabs.filter((x) => !x.hidden);
  const active = shown.some((x) => x.value === value) ? value : shown[0]?.value;
  // A tab the viewer doesn't get (e.g. ?tab=audit for a member): the address moves to the tab shown.
  const moveTo = ready && active !== undefined && active !== value ? active : undefined;
  useEffect(() => {
    if (moveTo !== undefined) onValueChange(moveTo, { replace: true });
    // onValueChange is a new function on every render; the move only depends on where to go.
  }, [moveTo]); // eslint-disable-line react-hooks/exhaustive-deps
  const current = shown.find((x) => x.value === active);
  const crumbText = current?.crumb ?? (typeof current?.label === "string" ? current.label : undefined);
  const showCrumb = crumb === "always" || (crumb === "after-first" && current !== shown[0]);
  useCrumbTail(showCrumb ? crumbText : undefined);
  return (
    <Tabs value={active} onValueChange={(v) => onValueChange(v as T)} className={t.tabs}>
      <TabsList aria-label={label} variant="pills" className={t.list}>
        {shown.map((x) => (
          <Tab key={x.value} value={x.value} icon={x.icon} count={x.count}>
            {x.label}
          </Tab>
        ))}
      </TabsList>
      {shown.map((x) => (
        <TabsPanel key={x.value} value={x.value} className={t.panel}>
          {x.content}
        </TabsPanel>
      ))}
    </Tabs>
  );
}
