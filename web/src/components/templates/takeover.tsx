/*
 * Record and form pages (D4, revised 2026-09-28): a record, or a create or
 * edit form, opens as a page of its own in the main area instead of a side
 * sheet. The route's page stays mounted but hidden underneath, so closing
 * returns to the same list, filters and scroll position. The page's URL
 * parameter (?record=, ?form=) makes it linkable, and Back closes it.
 *
 *   shell:  <TakeoverHost backLabel={…}>{outlet}</TakeoverHost>
 *   page:   <TakeoverPage label="Audit entry" title=… description=… actions=… onBack={close}>…</TakeoverPage>
 *
 * Pages stack: an edit form opened from a record page covers it, and only
 * the top one is shown. Without a host (unit tests) a page renders in place.
 * RecordPage and FormPage build on this; use those.
 */
import { ArrowLeft } from "lucide-react";
import { createContext, type ReactNode, useCallback, useContext, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { cx } from "@/lib/bitop-utils";
import s from "../../pages/shared.module.css";
import { insertByDepth, useCurrentPageCrumbs, usePageCrumb } from "../layout/crumb-tail";
import styles from "./templates.module.css";

type Host = {
  slot: HTMLElement | null;
  stack: string[];
  push: (id: string, depth: number) => void;
  remove: (id: string) => void;
  backLabel?: ReactNode;
};

const HostContext = createContext<Host | null>(null);

/** How many pages the current one is nested in (0 outside any): a page opened from another's content stacks over it. */
const DepthContext = createContext(0);

/** The shell's main area: the route's page, hidden while a record or form page is open. */
export function TakeoverHost({ children, backLabel }: { children: ReactNode; backLabel?: ReactNode }) {
  const [slot, setSlot] = useState<HTMLElement | null>(null);
  const [entries, setEntries] = useState<{ id: string; depth: number }[]>([]);
  // Stable, so a page's register effect runs once, not on every stack change.
  const push = useCallback((id: string, depth: number) => setEntries((st) => (st.some((x) => x.id === id) ? st : insertByDepth(st, { id, depth }))), []);
  const remove = useCallback((id: string) => setEntries((st) => st.filter((x) => x.id !== id)), []);
  const stack = useMemo(() => entries.map((x) => x.id), [entries]);
  const value = useMemo<Host>(() => ({ slot, stack, backLabel, push, remove }), [slot, stack, backLabel, push, remove]);
  return (
    <HostContext.Provider value={value}>
      <div hidden={entries.length > 0}>{children}</div>
      <div ref={setSlot} />
    </HostContext.Provider>
  );
}

export type TakeoverPageProps = {
  /** Plain-text name: the last breadcrumb and the page region's name. */
  label: string;
  title: ReactNode;
  description?: ReactNode;
  /** Status badges next to the title. */
  meta?: ReactNode;
  /** Buttons on the right of the header. */
  actions?: ReactNode;
  /** Closes the page (the back link and the breadcrumb). */
  onBack: () => void;
  /** The URL parameter that opens this page (record, form, or a nested record's own): the back link's address is the current one without it. */
  param: string;
  /** What gets focus when the page opens: its heading (default), or its first field (a form). */
  initialFocus?: "heading" | "field";
  /** Where the back link leads when that isn't the page underneath, e.g. the page that linked here ("Back to Costs"). */
  back?: { label: string; href: string };
  children?: ReactNode;
};

function currentHrefWithout(param: string) {
  const url = new URL(globalThis.location?.href ?? "http://localhost/");
  url.searchParams.delete(param);
  return url.pathname + url.search;
}

export function TakeoverPage({ label, title, description, meta, actions, onBack, param, initialFocus = "heading", back: backTo, children }: TakeoverPageProps) {
  const host = useContext(HostContext);
  const depth = useContext(DepthContext) + 1;
  const id = useId();
  const ref = useRef<HTMLElement>(null);
  const isTop = !host || host.stack[host.stack.length - 1] === id;
  const hasHost = Boolean(host);
  const push = host?.push;
  const remove = host?.remove;

  // Register; remember where focus and scroll were, and restore them on close.
  useLayoutEffect(() => {
    const opener = document.activeElement as HTMLElement | null;
    const scrollY = globalThis.scrollY ?? 0;
    push?.(id, depth);
    return () => {
      remove?.(id);
      requestAnimationFrame(() => {
        if (push) globalThis.scrollTo?.(0, scrollY);
        if (opener?.isConnected && opener.offsetParent !== null) opener.focus({ preventScroll: true });
      });
    };
  }, [id, depth, push, remove]);

  // On becoming the top page: start at the top, focus the heading.
  useEffect(() => {
    if (!isTop || !hasHost) return;
    globalThis.scrollTo?.(0, 0);
    const field = initialFocus === "field" ? ref.current?.querySelector<HTMLElement>("form :is(input, textarea, select):not([disabled]):not([type=hidden])") : null;
    const h = ref.current?.querySelector<HTMLElement>("h1");
    if (field) field.focus({ preventScroll: true });
    else if (h) {
      h.tabIndex = -1;
      h.focus({ preventScroll: true });
    }
  }, [isTop, hasHost, initialFocus]);

  // A stable close for the crumb: callers pass inline functions, and a new
  // crumb on every render would re-render the shell in a loop.
  const onBackRef = useRef(onBack);
  onBackRef.current = onBack;
  const href = currentHrefWithout(param);
  const crumb = useMemo(() => (hasHost ? { id, label, close: () => onBackRef.current(), href, depth } : undefined), [hasHost, id, label, href, depth]);
  usePageCrumb(crumb);

  // The back link names what's underneath: the page below in the stack, or the route's page.
  const crumbs = useCurrentPageCrumbs();
  const below = crumbs[crumbs.findIndex((c) => c.id === id) - 1];
  const back = backTo?.label ?? (below ? below.label : host?.backLabel);
  const page = (
    <section ref={ref} aria-label={label} hidden={!isTop} className={cx(s.page, styles.takeover)}>
      <Stack gap={6}>
        <PageHeader
          title={title}
          meta={meta}
          description={description}
          actions={actions ? <div className={styles.headerActions}>{actions}</div> : undefined}
          breadcrumbs={
            <a
              href={backTo?.href ?? href}
              className={styles.backLink}
              onClick={(e) => {
                if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
                e.preventDefault();
                onBack();
              }}
            >
              <ArrowLeft aria-hidden size={14} />
              {back ? <>Back to {back}</> : "Back"}
            </a>
          }
        />
        <DepthContext.Provider value={depth}>{children}</DepthContext.Provider>
      </Stack>
    </section>
  );
  return host?.slot ? createPortal(page, host.slot) : host ? null : page;
}
