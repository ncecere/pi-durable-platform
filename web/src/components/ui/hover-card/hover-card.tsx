"use client";

import { PreviewCard as BasePreviewCard } from "@base-ui/react/preview-card";
import type { ReactElement, ReactNode } from "react";
import popup from "@/components/ui/styles/popup.module.css";
import { cx } from "@/lib/bitop-utils";
import styles from "./hover-card.module.css";

/*
 * Hover card on Base UI PreviewCard: a rich preview of a link's destination
 * (a profile, a repository, a document) shown when the link is hovered or
 * focused.
 *
 *   <HoverCard trigger={<TextLink href="/u/ada">@ada</TextLink>}>
 *     …avatar, name, bio…
 *   </HoverCard>
 *
 * The card is a visual enhancement only: its content is not reachable by
 * keyboard, touch or screen reader, so never put essential information or
 * controls in it. Everything it shows must also be available at the link's
 * destination.
 */

export type HoverCardProps = {
  /**
   * The link that opens the card, e.g. `<TextLink href="/u/ada">@ada</TextLink>`
   * or a router link. Rendered through Base UI's `render`, so it must accept
   * a ref and spread props; it stays an ordinary link.
   */
  trigger: ReactElement;
  /** The preview content. */
  children: ReactNode;
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Milliseconds of hover before the card opens. */
  delay?: number;
  /** Milliseconds before the card closes after the pointer leaves. */
  closeDelay?: number;
  side?: "top" | "bottom" | "left" | "right";
  align?: "start" | "center" | "end";
  /** Gap from the trigger in px. */
  sideOffset?: number;
  /** Class for the card. */
  className?: string;
};

export function HoverCard({
  trigger,
  children,
  open,
  defaultOpen,
  onOpenChange,
  delay = 600,
  closeDelay = 300,
  side = "bottom",
  align = "center",
  sideOffset = 8,
  className,
}: HoverCardProps) {
  return (
    <BasePreviewCard.Root open={open} defaultOpen={defaultOpen} onOpenChange={onOpenChange ? (o) => onOpenChange(o) : undefined}>
      <BasePreviewCard.Trigger render={trigger} delay={delay} closeDelay={closeDelay} />
      <BasePreviewCard.Portal>
        <BasePreviewCard.Positioner className={popup.positioner} side={side} align={align} sideOffset={sideOffset}>
          <BasePreviewCard.Popup className={cx(popup.popup, styles.card, className)}>{children}</BasePreviewCard.Popup>
        </BasePreviewCard.Positioner>
      </BasePreviewCard.Portal>
    </BasePreviewCard.Root>
  );
}
