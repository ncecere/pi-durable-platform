"use client";

import { useRender } from "@base-ui/react/use-render";
import type { ComponentPropsWithRef } from "react";
import { cx } from "@/lib/bitop-utils";

export type VisuallyHiddenProps = ComponentPropsWithRef<"span"> & {
  /** Render another element, e.g. `render={<h2 />}`. */
  render?: useRender.RenderProp;
};

/** Content available to assistive technology but not shown on screen. */
export function VisuallyHidden({ className, render, ref, ...props }: VisuallyHiddenProps) {
  return useRender({ render, defaultTagName: "span", ref, props: { ...props, className: cx("sr-only", className) } });
}
