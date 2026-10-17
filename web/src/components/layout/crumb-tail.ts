/*
 * The last breadcrumb, set by the page: the active tab of a tabbed page
 * ("QA Team › Team settings › Members") or a record's name. The shell's
 * breadcrumbs append it, so the trail always matches what's shown (F-12).
 *
 *   useCrumbTail(tab === tabs[0] ? undefined : "Members");
 *
 * PageTabs and the templates call it for you. Cleared when the page unmounts.
 */
import { useEffect, useSyncExternalStore } from "react";

let tail: string | undefined;
const listeners = new Set<() => void>();

function setTail(next: string | undefined) {
  if (tail === next) return;
  tail = next;
  for (const l of listeners) l();
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

/** Sets the breadcrumb tail while the calling component is mounted. */
export function useCrumbTail(label: string | undefined) {
  useEffect(() => {
    setTail(label);
    return () => setTail(undefined);
  }, [label]);
}

/** The current breadcrumb tail (the shell's breadcrumbs). */
export function useCurrentCrumbTail() {
  return useSyncExternalStore(subscribe, () => tail);
}

/* ---------------- record and form pages ---------------- */

/**
 * The open record and form pages (RecordPage, FormPage), bottom first: each
 * one's title is a crumb after the route's trail. `href` is the address
 * underneath it (without its own ?record= or ?form=), and `close` returns
 * there.
 */
export type PageCrumb = {
  id: string;
  label: string;
  close: () => void;
  href: string;
  /** How deep the page is nested in other pages (1: over the route's page); orders the stack. */
  depth?: number;
};

/**
 * Adds an open page to a stack, bottom first, after every page at its depth
 * or above it. Pages register in effects, and React runs a nested page's
 * effects before its parent's: when both mount together (Back to a result
 * over a run), registration order alone would put the result under the run.
 */
export function insertByDepth<T extends { depth?: number }>(stack: T[], item: T): T[] {
  const d = item.depth ?? 1;
  const at = stack.findIndex((x) => (x.depth ?? 1) > d);
  return at < 0 ? [...stack, item] : [...stack.slice(0, at), item, ...stack.slice(at)];
}

let pageCrumbs: PageCrumb[] = [];
const pageListeners = new Set<() => void>();

const subscribePage = (l: () => void) => {
  pageListeners.add(l);
  return () => pageListeners.delete(l);
};

function setPageCrumbs(next: PageCrumb[]) {
  pageCrumbs = next;
  for (const l of pageListeners) l();
}

/** Registers an open page's crumb while mounted; it keeps its place in the stack when it changes. */
export function usePageCrumb(crumb: PageCrumb | undefined) {
  useEffect(() => {
    if (!crumb) return;
    const i = pageCrumbs.findIndex((c) => c.id === crumb.id);
    setPageCrumbs(i < 0 ? insertByDepth(pageCrumbs, crumb) : pageCrumbs.map((c) => (c.id === crumb.id ? crumb : c)));
  }, [crumb]);
  const id = crumb?.id;
  useEffect(() => () => setPageCrumbs(pageCrumbs.filter((c) => c.id !== id)), [id]);
}

/** The open pages' crumbs, bottom first (the shell's breadcrumbs, the back links). */
export function useCurrentPageCrumbs() {
  return useSyncExternalStore(subscribePage, () => pageCrumbs);
}
