"use client";

import { Dialog as BaseDialog } from "@base-ui/react/dialog";
import { X } from "lucide-react";
import { type ReactElement, type ReactNode, useEffect, useId, useRef, useState } from "react";
import { Button, IconButton, type ButtonProps } from "@/components/ui/button/button";
import { cx } from "@/lib/bitop-utils";
import styles from "./sheet.module.css";

/*
 * Sheet: a modal dialog attached to an edge of the screen, for secondary
 * tasks (filters, details, settings) that keep the page in view. Built on
 * Base UI Dialog with the same conventions as bitop's Dialog: focus is
 * trapped and returns to the trigger, Escape and the backdrop close it, the
 * title names it and the description describes it. Use Drawer instead if you
 * need swipe-to-dismiss or snap points.
 *
 *   <Sheet
 *     trigger={<Button variant="secondary">Filters</Button>}
 *     title="Filters"
 *     description="Narrow the list of deployments."
 *     footer={<><SheetClose>Cancel</SheetClose><Button>Apply</Button></>}
 *   >
 *     …fields…
 *   </Sheet>
 *
 * On a phone, a split view's side panel can open as a full-screen sheet
 * instead: `size="full"` covers the viewport (no backdrop shows around it),
 * with its close button at the start of the header, like a page's back
 * button (`closeIcon` and `closeLabel` can say so). A sheet of text to read
 * rather than fields to fill can take focus on its title
 * (`initialFocus="title"`). When the body scrolls and holds nothing
 * focusable, keyboard users can still scroll it: it then takes focus itself
 * (a region named by the title).
 */

export type SheetSide = "right" | "left" | "top" | "bottom";
export type SheetSize = "sm" | "md" | "lg" | "xl" | "full";

export type SheetProps = {
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Heading; names the sheet. */
  title: ReactNode;
  /** Describes the sheet's purpose; read after the title by screen readers. */
  description: ReactNode;
  /** Optional trigger element, e.g. `<Button>Open</Button>`. */
  trigger?: ReactElement;
  /** Footer pinned to the bottom, usually buttons. Use <SheetClose> for Cancel. */
  footer?: ReactNode;
  /** The edge the sheet slides in from. */
  side?: SheetSide;
  /**
   * Width of left/right sheets (top/bottom sheets span the viewport width).
   * `full` covers the whole screen from any side, e.g. a side panel's
   * stand-in on a phone; keep the close button (or give another way out).
   */
  size?: SheetSize;
  /** Hide the × close button (Escape still closes). */
  hideClose?: boolean;
  /** The close button's accessible name (default "Close"), e.g. "Close the source". */
  closeLabel?: string;
  /** The close button's icon (default ×), e.g. an arrow back for a full-screen sheet. Mark it aria-hidden. */
  closeIcon?: ReactNode;
  /**
   * Element to focus when opened (default: first focusable); `"title"`
   * focuses the title, for a sheet of text to read rather than fields to fill.
   */
  initialFocus?: BaseDialog.Popup.Props["initialFocus"] | "title";
  /** Element to focus when closed (default: the trigger). */
  finalFocus?: BaseDialog.Popup.Props["finalFocus"];
  className?: string;
  children?: ReactNode;
};

export function Sheet({
  open,
  defaultOpen,
  onOpenChange,
  title,
  description,
  trigger,
  footer,
  side = "right",
  size = "md",
  hideClose,
  closeLabel = "Close",
  closeIcon,
  initialFocus,
  finalFocus,
  className,
  children,
}: SheetProps) {
  const titleId = useId();
  const titleRef = useRef<HTMLHeadingElement>(null);
  const [body, setBody] = useState<HTMLDivElement | null>(null);
  const scrolls = useScrollsWithoutFocus(body);
  const full = size === "full";
  const close = !hideClose && (
    <BaseDialog.Close render={<IconButton size="sm" icon={closeIcon ?? <X aria-hidden />} label={closeLabel} className={styles.close} />} />
  );
  return (
    <BaseDialog.Root open={open} defaultOpen={defaultOpen} onOpenChange={onOpenChange ? (o) => onOpenChange(o) : undefined}>
      {trigger && <BaseDialog.Trigger render={trigger} />}
      <BaseDialog.Portal>
        <BaseDialog.Backdrop className={styles.backdrop} />
        <BaseDialog.Popup
          aria-modal="true"
          className={cx(styles.popup, className)}
          data-side={side}
          data-size={size}
          data-closable={hideClose ? undefined : ""}
          initialFocus={initialFocus === "title" ? titleRef : initialFocus}
          finalFocus={finalFocus}
        >
          {/* A full-screen sheet's close comes first, where it shows (a page's back button). */}
          {full && close}
          <div className={styles.header}>
            <BaseDialog.Title id={titleId} ref={titleRef} tabIndex={initialFocus === "title" ? -1 : undefined} className={styles.title}>
              {title}
            </BaseDialog.Title>
            <BaseDialog.Description className={styles.description}>{description}</BaseDialog.Description>
          </div>
          {children !== undefined && (
            <div
              ref={setBody}
              className={styles.body}
              {...(scrolls ? { tabIndex: 0, role: "region", "aria-labelledby": titleId } : {})}
            >
              {children}
            </div>
          )}
          {footer && <div className={styles.footer}>{footer}</div>}
          {/* Otherwise last in DOM order so initial focus lands on the first field, not on ×. */}
          {!full && close}
        </BaseDialog.Popup>
      </BaseDialog.Portal>
    </BaseDialog.Root>
  );
}

/**
 * Whether the body scrolls with nothing focusable in it: keyboard users then
 * couldn't scroll it (axe: scrollable-region-focusable), so it takes focus.
 */
function useScrollsWithoutFocus(el: HTMLElement | null) {
  const [scrolls, setScrolls] = useState(false);
  useEffect(() => {
    if (!el) return;
    const check = () =>
      setScrolls(el.scrollHeight > el.clientHeight + 1 && !el.querySelector("a[href], button:not([disabled]), input, select, textarea, [tabindex]:not([tabindex='-1'])"));
    check();
    const resize = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(check);
    resize?.observe(el);
    const mutation = typeof MutationObserver === "undefined" ? undefined : new MutationObserver(check);
    mutation?.observe(el, { childList: true, subtree: true, characterData: true });
    return () => {
      resize?.disconnect();
      mutation?.disconnect();
    };
  }, [el]);
  return scrolls;
}

/** A button that closes the surrounding Sheet. Defaults to the secondary variant; takes Button props. */
export function SheetClose({ variant = "secondary", ...props }: ButtonProps) {
  return <BaseDialog.Close render={<Button variant={variant} {...(props as ButtonProps)} />} />;
}
