"use client";

import type { ComponentPropsWithRef, ReactNode } from "react";
import { Button, type ButtonProps } from "@/components/ui/button/button";
import { cx } from "@/lib/bitop-utils";
import styles from "./suggestion.module.css";

/*
 * Suggestions: prompt starters as pill buttons.
 *
 *   <Suggestions>
 *     {ideas.map((s) => <Suggestion key={s} suggestion={s} onSelect={send} />)}
 *   </Suggestions>
 *
 * `layout="scroll"` keeps one row that scrolls sideways (mobile composer);
 * `wrap` (default) flows onto several lines, centred, or from the start
 * with `align="start"` (under left-aligned text); a long suggestion wraps
 * inside its chip, never wider than the group. The group is labelled
 * "Suggestions"; each chip is a real button whose name is its text.
 */

export type SuggestionsProps = ComponentPropsWithRef<"div"> & {
  layout?: "wrap" | "scroll";
  /** Where wrapped rows line up (the wrap layout): centred (default), or from the start. */
  align?: "center" | "start";
  /** Accessible name of the group. */
  label?: string;
};

export function Suggestions({ layout = "wrap", align = "center", label = "Suggestions", className, children, ...props }: SuggestionsProps) {
  return (
    <div {...props} role="group" aria-label={label} data-layout={layout} data-align={align} className={cx(styles.suggestions, className)}>
      {children}
    </div>
  );
}

export type SuggestionProps = Omit<ButtonProps, "onSelect" | "children" | "iconOnly"> & {
  /** The prompt text, passed to onSelect. */
  suggestion: string;
  onSelect?: (suggestion: string) => void;
  /** Leading decorative icon. */
  icon?: ReactNode;
  /** Visible content if different from the suggestion text. */
  children?: ReactNode;
};

export function Suggestion({ suggestion, onSelect, icon, children, className, onClick, ...props }: SuggestionProps) {
  return (
    <Button
      {...props}
      variant="secondary"
      size="sm"
      className={cx(styles.suggestion, className)}
      onClick={(e) => {
        onClick?.(e);
        if (!e.defaultPrevented) onSelect?.(suggestion);
      }}
    >
      {icon}
      <span className={styles.text}>{children ?? suggestion}</span>
    </Button>
  );
}
