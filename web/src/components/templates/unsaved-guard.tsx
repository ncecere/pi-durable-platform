/*
 * Unsaved-changes guard (F-17): while `dirty`, leaving the page (a link, the
 * sidebar, Back) asks first, and closing or reloading the tab gets the
 * browser's own prompt. SettingsPage installs it; use it directly for other
 * pages with a SaveBar:
 *
 *   const guard = useUnsavedChangesGuard(changes.length > 0);
 *   return <>…<SaveBar …/>{guard}</>;
 *
 * Moving between tabs of the same page (?tab=) isn't blocked unless
 * `samePath` is set, since one form usually spans the tabs.
 */
import { useBlocker } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { AlertDialog } from "@/components/ui/dialog/dialog";

export type UnsavedGuardOptions = {
  /** Also ask when only the query string changes (the tabs hold separate forms). */
  samePath?: boolean;
  /** Dialog text. */
  title?: string;
  description?: string;
};

/** Returns the confirmation dialog to render (null while nothing is blocked). */
export function useUnsavedChangesGuard(dirty: boolean, opts: UnsavedGuardOptions = {}): ReactNode {
  const blocker = useBlocker({
    shouldBlockFn: ({ current, next }) => dirty && (opts.samePath || current.pathname !== next.pathname),
    enableBeforeUnload: () => dirty,
    withResolver: true,
  });
  if (blocker.status !== "blocked") return null;
  return (
    <AlertDialog
      open
      onOpenChange={(open) => {
        if (!open) blocker.reset();
      }}
      title={opts.title ?? "Leave without saving?"}
      description={opts.description ?? "Your changes on this page haven't been saved. If you leave now, they're discarded."}
      confirmLabel="Discard changes"
      cancelLabel="Keep editing"
      onConfirm={() => blocker.proceed()}
    />
  );
}

/** The guard as an element, for pages that render their own SaveBar: `<UnsavedChangesGuard dirty={changes > 0} />`. */
export function UnsavedChangesGuard({ dirty, ...opts }: UnsavedGuardOptions & { dirty: boolean }) {
  return <>{useUnsavedChangesGuard(dirty, opts)}</>;
}
