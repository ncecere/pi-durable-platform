"use client";

import { Drawer as BaseDrawer } from "@base-ui/react/drawer";
import { X } from "lucide-react";
import { type ComponentPropsWithRef, createContext, useContext } from "react";
import { Button, IconButton, type ButtonProps } from "@/components/ui/button/button";
import { cx, dataFlag } from "@/lib/bitop-utils";
import styles from "./drawer.module.css";

/*
 * Drawer on Base UI Drawer: a panel that slides in from an edge and can be
 * swiped away toward that edge. Like Dialog it traps focus, closes on Escape
 * and outside click, returns focus to the trigger, and is labelled by its
 * DrawerTitle and described by its DrawerDescription.
 *
 *   <Drawer side="bottom">
 *     <DrawerTrigger render={<Button>Open</Button>} />
 *     <DrawerContent>
 *       <DrawerHeader>
 *         <DrawerTitle>Move goal</DrawerTitle>
 *         <DrawerDescription>Set your daily activity goal.</DrawerDescription>
 *       </DrawerHeader>
 *       <DrawerBody>…</DrawerBody>
 *       <DrawerFooter>
 *         <Button>Submit</Button>
 *         <DrawerClose>Cancel</DrawerClose>
 *       </DrawerFooter>
 *     </DrawerContent>
 *   </Drawer>
 *
 * Every DrawerContent needs a DrawerTitle (it names the dialog). Swiping is
 * a shortcut only: Escape, the backdrop and DrawerClose always close it.
 * If you don't need swipe gestures or snap points, Sheet is the simpler
 * edge-anchored dialog.
 */

export type DrawerSide = "bottom" | "top" | "left" | "right";

const SWIPE: Record<DrawerSide, NonNullable<BaseDrawer.Root.Props["swipeDirection"]>> = {
  bottom: "down",
  top: "up",
  left: "left",
  right: "right",
};

type DrawerContextValue = { side: DrawerSide; modal: BaseDrawer.Root.Props["modal"]; snapPoints: boolean };
const DrawerContext = createContext<DrawerContextValue>({ side: "bottom", modal: true, snapPoints: false });

export type DrawerProps = Omit<BaseDrawer.Root.Props, "swipeDirection"> & {
  /** The edge the drawer is attached to; it is swiped back toward this edge to dismiss. */
  side?: DrawerSide;
};

/** Root: holds open state (`open` / `defaultOpen` / `onOpenChange`), `modal` and `snapPoints`. */
export function Drawer({ side = "bottom", modal = true, snapPoints, ...props }: DrawerProps) {
  const hasSnapPoints = snapPoints !== undefined && snapPoints.length > 0;
  return (
    <DrawerContext.Provider value={{ side, modal, snapPoints: hasSnapPoints }}>
      <BaseDrawer.Root {...props} modal={modal} snapPoints={snapPoints} swipeDirection={SWIPE[side]} />
    </DrawerContext.Provider>
  );
}

export type DrawerTriggerProps = BaseDrawer.Trigger.Props;

/** Opens the drawer. Usually `render={<Button>…</Button>}`. */
export function DrawerTrigger(props: DrawerTriggerProps) {
  return <BaseDrawer.Trigger {...props} />;
}

export type DrawerContentProps = Omit<BaseDrawer.Popup.Props, "className"> & {
  className?: string;
  /**
   * Show the grab handle. Defaults to true for bottom and top drawers, where
   * it hints that the sheet can be dragged.
   */
  showHandle?: boolean;
  /** Show an × close button in the corner (Escape and the backdrop still close it). */
  showClose?: boolean;
  /** Accessible name of the × button. */
  closeLabel?: string;
};

/** The drawer panel: portal, backdrop, viewport and popup in one part. */
export function DrawerContent({
  className,
  showHandle,
  showClose = false,
  closeLabel = "Close",
  children,
  ...props
}: DrawerContentProps) {
  const { side, modal, snapPoints } = useContext(DrawerContext);
  const handle = showHandle ?? (side === "bottom" || side === "top");
  return (
    <BaseDrawer.Portal>
      {modal === true && <BaseDrawer.Backdrop className={styles.backdrop} />}
      <BaseDrawer.Viewport className={styles.viewport} data-modal={dataFlag(modal === true)}>
        <BaseDrawer.Popup
          {...props}
          className={cx(styles.popup, className)}
          data-side={side}
          data-snap-points={dataFlag(snapPoints)}
          data-closable={dataFlag(showClose)}
        >
          {handle && <div aria-hidden className={styles.handle} />}
          <BaseDrawer.Content className={styles.content}>
            {children}
            {/* Last in DOM order so initial focus lands on the first field, not on ×. */}
            {showClose && (
              <BaseDrawer.Close render={<IconButton size="sm" icon={<X aria-hidden />} label={closeLabel} className={styles.close} />} />
            )}
          </BaseDrawer.Content>
        </BaseDrawer.Popup>
      </BaseDrawer.Viewport>
    </BaseDrawer.Portal>
  );
}

export type DrawerHeaderProps = ComponentPropsWithRef<"div">;

/** Groups DrawerTitle and DrawerDescription. Centred on bottom/top drawers. */
export function DrawerHeader({ className, ...props }: DrawerHeaderProps) {
  return <div {...props} className={cx(styles.header, className)} />;
}

export type DrawerBodyProps = ComponentPropsWithRef<"div">;

/** Scrollable, padded content between the header and the footer. */
export function DrawerBody({ className, ...props }: DrawerBodyProps) {
  return <div {...props} className={cx(styles.body, className)} />;
}

export type DrawerFooterProps = ComponentPropsWithRef<"div">;

/** Actions pinned to the end of the drawer. Buttons stack full-width on bottom/top drawers. */
export function DrawerFooter({ className, ...props }: DrawerFooterProps) {
  return <div {...props} className={cx(styles.footer, className)} />;
}

export type DrawerTitleProps = Omit<BaseDrawer.Title.Props, "className"> & { className?: string };

/** Required heading; names the drawer (an `<h2>`). */
export function DrawerTitle({ className, ...props }: DrawerTitleProps) {
  return <BaseDrawer.Title {...props} className={cx(styles.title, className)} />;
}

export type DrawerDescriptionProps = Omit<BaseDrawer.Description.Props, "className"> & { className?: string };

/** Optional supporting text; describes the drawer. */
export function DrawerDescription({ className, ...props }: DrawerDescriptionProps) {
  return <BaseDrawer.Description {...props} className={cx(styles.description, className)} />;
}

/** A button that closes the surrounding drawer. Defaults to the secondary variant; takes Button props. */
export function DrawerClose({ variant = "secondary", ...props }: ButtonProps) {
  return <BaseDrawer.Close render={<Button variant={variant} {...(props as ButtonProps)} />} />;
}
