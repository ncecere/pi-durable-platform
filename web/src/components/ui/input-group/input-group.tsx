"use client";

import type { ComponentPropsWithRef, MouseEvent } from "react";
import { Button, type ButtonProps } from "@/components/ui/button/button";
import { Input, type InputProps, Textarea, type TextareaProps } from "@/components/ui/input/input";
import { cx } from "@/lib/bitop-utils";
import styles from "./input-group.module.css";

/*
 * InputGroup: one control boundary around an Input (or Textarea) and its
 * addons: icons, text such as "https://" or "USD", keyboard hints and
 * buttons. The control is still a Base UI Field control, so a surrounding
 * <Field> labels it, adds the description and marks it invalid; the group
 * shows the focus ring and the error border for it.
 *
 *   <Field label="Website">
 *     <InputGroup>
 *       <InputGroupAddon><InputGroupText>https://</InputGroupText></InputGroupAddon>
 *       <InputGroupInput placeholder="example.com" />
 *       <InputGroupAddon align="end">
 *         <InputGroupButton iconOnly aria-label="Copy URL"><Copy aria-hidden /></InputGroupButton>
 *       </InputGroupAddon>
 *     </InputGroup>
 *   </Field>
 *
 * Addons are decorative unless they contain a button. Clicking a non-button
 * part of an addon focuses the control. With a Textarea, use
 * align="block-start" / "block-end" for toolbars above or below it.
 */

export type InputGroupProps = ComponentPropsWithRef<"div"> & {
  size?: "sm" | "md";
};

export function InputGroup({ size = "md", className, ...props }: InputGroupProps) {
  return <div {...props} data-size={size} className={cx(styles.group, className)} />;
}

export type InputGroupAddonAlign = "start" | "end" | "block-start" | "block-end";

export type InputGroupAddonProps = ComponentPropsWithRef<"div"> & {
  /** Where the addon sits: before/after the control, or above/below a textarea. */
  align?: InputGroupAddonAlign;
};

export function InputGroupAddon({ align = "start", className, onClick, ...props }: InputGroupAddonProps) {
  function focusControl(e: MouseEvent<HTMLDivElement>) {
    onClick?.(e);
    if (e.defaultPrevented) return;
    if ((e.target as HTMLElement).closest("button, a, input, textarea, select, [role='button']")) return;
    e.currentTarget.parentElement?.querySelector<HTMLElement>("[data-group-control]")?.focus();
  }
  return <div {...props} data-align={align} onClick={focusControl} className={cx(styles.addon, className)} />;
}

export type InputGroupTextProps = ComponentPropsWithRef<"span">;

/** Static text inside an addon, e.g. "https://", "USD" or "/ 280". */
export function InputGroupText({ className, ...props }: InputGroupTextProps) {
  return <span {...props} className={cx(styles.text, className)} />;
}

/** A compact Button sized for an addon. Defaults to the ghost variant and size sm. */
export type InputGroupButtonProps = ButtonProps;

export function InputGroupButton({ className, variant = "ghost", size = "sm", ...props }: InputGroupButtonProps) {
  return <Button {...(props as ButtonProps)} variant={variant} size={size} className={cx(styles.button, className)} />;
}

export type InputGroupInputProps = Omit<InputProps, "startIcon" | "size">;

/** The group's text input (bitop Input, a Base UI Field control). */
export function InputGroupInput({ className, ...props }: InputGroupInputProps) {
  return <Input {...props} data-group-control="" className={cx(styles.control, className)} />;
}

export type InputGroupTextareaProps = TextareaProps;

/** The group's textarea (bitop Textarea, a Base UI Field control). */
export function InputGroupTextarea({ className, ...props }: InputGroupTextareaProps) {
  return <Textarea {...props} data-group-control="" className={cx(styles.control, styles.textarea, className)} />;
}
