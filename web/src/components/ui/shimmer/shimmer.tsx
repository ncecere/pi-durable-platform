"use client";

import { useRender } from "@base-ui/react/use-render";
import type { CSSProperties, ComponentPropsWithRef } from "react";
import { cx, dataFlag } from "@/lib/bitop-utils";
import styles from "./shimmer.module.css";

/*
 * Shimmer: text with a light sweep moving across it, for "Thinking…" and
 * other in-progress labels. The resting colour is --color-text-muted (4.5:1+
 * on every surface) and the sweep brightens towards --color-text, so the text
 * is always readable. Under prefers-reduced-motion the sweep is removed and
 * the text is simply muted.
 */

export type ShimmerProps = ComponentPropsWithRef<"span"> & {
  /** Seconds per sweep. */
  duration?: number;
  /** Turn the animation off (e.g. once streaming ends) without changing the element. */
  active?: boolean;
  /** Render another element, e.g. `render={<p />}`. */
  render?: useRender.RenderProp;
};

export function Shimmer({ duration = 2, active = true, className, style, render, ref, ...props }: ShimmerProps) {
  return useRender({
    render,
    defaultTagName: "span",
    ref,
    props: {
      ...props,
      className: cx(styles.shimmer, className),
      "data-active": dataFlag(active),
      style: { "--shimmer-duration": `${duration}s`, ...style } as CSSProperties,
    },
  });
}
