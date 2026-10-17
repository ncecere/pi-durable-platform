"use client";

import { useRender } from "@base-ui/react/use-render";
import { createContext, useContext, type ComponentPropsWithRef } from "react";
import { cx } from "@/lib/bitop-utils";
import styles from "./item.module.css";

/*
 * Item: a flexible row with media, a title, a description and actions, for
 * settings lists, file lists, people pickers and link lists.
 *
 *   <ItemGroup aria-label="Integrations">
 *     <Item variant="outline">
 *       <ItemMedia variant="icon"><Github /></ItemMedia>
 *       <ItemContent>
 *         <ItemTitle>GitHub</ItemTitle>
 *         <ItemDescription>Deploy on every push.</ItemDescription>
 *       </ItemContent>
 *       <ItemActions><Button size="sm">Connect</Button></ItemActions>
 *     </Item>
 *   </ItemGroup>
 *
 * A whole row can be a link: `<Item render={<a href="/settings/billing" />}>`
 * (or a router <Link />). Don't put other interactive controls inside a link
 * item. Inside an ItemGroup (a list), each item is wrapped in a listitem so
 * link items keep their link role.
 */

const InGroup = createContext(false);

export type ItemVariant = "default" | "outline" | "muted";
export type ItemSize = "md" | "sm" | "xs";

export type ItemGroupProps = ComponentPropsWithRef<"div">;

/** A list of items (role="list"). Name it with aria-label when the context isn't obvious. */
export function ItemGroup({ className, ...props }: ItemGroupProps) {
  return (
    <InGroup.Provider value={true}>
      <div role="list" {...props} className={cx(styles.group, className)} />
    </InGroup.Provider>
  );
}

export type ItemProps = ComponentPropsWithRef<"div"> & {
  variant?: ItemVariant;
  size?: ItemSize;
  /** Render another element, e.g. a link: `render={<a href="/x" />}` or a router <Link />. */
  render?: useRender.RenderProp;
};

export function Item({ variant = "default", size = "md", className, render, ref, ...props }: ItemProps) {
  const inGroup = useContext(InGroup);
  const element = useRender({
    render,
    defaultTagName: "div",
    ref,
    props: {
      ...props,
      className: cx(styles.item, className),
      "data-variant": variant,
      "data-size": size,
    },
  });
  return inGroup ? (
    <div role="listitem" className={styles.listItem}>
      {element}
    </div>
  ) : (
    element
  );
}

export type ItemMediaProps = ComponentPropsWithRef<"div"> & {
  /** `icon` sizes an icon in a tinted square; `image` crops an <img> (avatars, thumbnails). */
  variant?: "default" | "icon" | "image";
};

export function ItemMedia({ variant = "default", className, ...props }: ItemMediaProps) {
  return <div {...props} data-variant={variant} className={cx(styles.media, className)} />;
}

export function ItemContent({ className, ...props }: ComponentPropsWithRef<"div">) {
  return <div {...props} className={cx(styles.content, className)} />;
}

export function ItemTitle({ className, ...props }: ComponentPropsWithRef<"div">) {
  return <div {...props} className={cx(styles.title, className)} />;
}

export function ItemDescription({ className, ...props }: ComponentPropsWithRef<"p">) {
  return <p {...props} className={cx(styles.description, className)} />;
}

export function ItemActions({ className, ...props }: ComponentPropsWithRef<"div">) {
  return <div {...props} className={cx(styles.actions, className)} />;
}

/** A full-width row above the item's main line (e.g. a cover image or a label). */
export function ItemHeader({ className, ...props }: ComponentPropsWithRef<"div">) {
  return <div {...props} className={cx(styles.header, className)} />;
}

/** A full-width row below the item's main line (e.g. metadata or secondary actions). */
export function ItemFooter({ className, ...props }: ComponentPropsWithRef<"div">) {
  return <div {...props} className={cx(styles.footer, className)} />;
}

export type ItemSeparatorProps = Omit<ComponentPropsWithRef<"div">, "children">;

/** A decorative hairline between items (hidden from assistive technology). */
export function ItemSeparator({ className, ...props }: ItemSeparatorProps) {
  const inGroup = useContext(InGroup);
  const line = <div aria-hidden="true" {...props} className={cx(styles.separator, className)} />;
  // A list may only contain list items, so wrap the line in a presentational one.
  return inGroup ? (
    <div role="listitem" aria-hidden="true" className={styles.separatorItem}>
      {line}
    </div>
  ) : (
    line
  );
}

export type ItemTitleProps = ComponentPropsWithRef<"div">;
export type ItemContentProps = ComponentPropsWithRef<"div">;
export type ItemDescriptionProps = ComponentPropsWithRef<"p">;
export type ItemActionsProps = ComponentPropsWithRef<"div">;
export type ItemHeaderProps = ComponentPropsWithRef<"div">;
export type ItemFooterProps = ComponentPropsWithRef<"div">;
