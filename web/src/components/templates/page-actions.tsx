/*
 * A page header's actions (D3): secondary buttons, the one primary action and
 * the "…" menu. On a phone (below 600px) only the primary stays a button; the
 * secondary actions move to the top of the "…" menu, so the header keeps to
 * one row (docs/v0.2.0.md §7, mobile).
 */
import type { ReactNode } from "react";
import { Button } from "@/components/ui/button/button";
import { NARROW_QUERY, useMediaQuery } from "@/lib/bitop-utils";
import { ActionMenu, type ActionItem } from "./action-menu";
import styles from "./templates.module.css";

export type PageActionsProps = {
  /** The one primary action, e.g. <Button>Publish</Button>. */
  primary?: ReactNode;
  /** Secondary buttons before it (a link with `render`, or `onSelect`); in the "…" menu on a phone. */
  secondary?: ActionItem[];
  /** The "…" menu (destructive actions last). */
  menu?: ActionItem[];
  menuLabel?: string;
};

function SecondaryButton({ action: a }: { action: ActionItem }) {
  const content = (
    <>
      {a.icon}
      {a.label}
    </>
  );
  if (a.render && !a.disabled) {
    return (
      <Button variant="secondary" render={a.render}>
        {content}
      </Button>
    );
  }
  return (
    <Button variant="secondary" onClick={a.onSelect} disabled={a.disabled} title={a.disabled ? a.disabledReason : undefined}>
      {content}
    </Button>
  );
}

/** Secondary buttons, the primary and the "…" menu; renders nothing without actions. */
export function PageActions({ primary, secondary = [], menu = [], menuLabel = "More actions" }: PageActionsProps) {
  const narrow = useMediaQuery(NARROW_QUERY);
  const shown = secondary.filter((a) => !a.hidden);
  const menuItems = narrow ? [...shown, ...menu] : menu;
  if (!primary && shown.length === 0 && !menu.some((a) => !a.hidden)) return null;
  return (
    <div className={styles.headerActions}>
      {!narrow && shown.map((a) => <SecondaryButton key={a.label} action={a} />)}
      {primary}
      <ActionMenu actions={menuItems} label={menuLabel} size="md" />
    </div>
  );
}
