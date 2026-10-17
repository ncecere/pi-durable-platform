"use client";

import { Accordion as BaseAccordion } from "@base-ui/react/accordion";
import { ChevronDown } from "lucide-react";
import { createContext, useContext, type ReactNode } from "react";
import { cx } from "@/lib/bitop-utils";
import styles from "./accordion.module.css";

/*
 * Accordion (Base UI): a stack of headings that each show and hide a panel.
 * Every trigger is a real <button> inside a heading, with aria-expanded and
 * aria-controls; panels are regions labelled by their trigger. Tab moves
 * between triggers; Enter/Space toggles.
 *
 *   <Accordion defaultValue={["billing"]}>
 *     <AccordionItem value="billing">
 *       <AccordionTrigger>Billing</AccordionTrigger>
 *       <AccordionPanel>…</AccordionPanel>
 *     </AccordionItem>
 *   </Accordion>
 *
 * One panel opens at a time by default; set `multiple` to allow several.
 * `headingLevel` (default 3) should fit the page outline.
 */

export type AccordionHeadingLevel = 2 | 3 | 4 | 5 | 6;

const HeadingLevel = createContext<AccordionHeadingLevel>(3);

export type AccordionProps<Value = unknown> = Omit<BaseAccordion.Root.Props<Value>, "className"> & {
  className?: string;
  /** `plain` separates items with hairlines; `outline` wraps them in a card. */
  variant?: "plain" | "outline";
  /** Heading level for every trigger (default 3). Override per trigger if needed. */
  headingLevel?: AccordionHeadingLevel;
};

export function Accordion<Value = unknown>({ className, variant = "plain", headingLevel = 3, ...props }: AccordionProps<Value>) {
  return (
    <HeadingLevel.Provider value={headingLevel}>
      <BaseAccordion.Root<Value> {...props} data-variant={variant} className={cx(styles.root, className)} />
    </HeadingLevel.Provider>
  );
}

export type AccordionItemProps = Omit<BaseAccordion.Item.Props, "className"> & { className?: string };

export function AccordionItem({ className, ...props }: AccordionItemProps) {
  return <BaseAccordion.Item {...props} className={cx(styles.item, className)} />;
}

export type AccordionTriggerProps = Omit<BaseAccordion.Trigger.Props, "className"> & {
  className?: string;
  /** Heading level of the wrapping heading (defaults to the Accordion's). */
  headingLevel?: AccordionHeadingLevel;
  /** Muted text after the title, e.g. a count or the current value. */
  meta?: ReactNode;
};

export function AccordionTrigger({ className, headingLevel, meta, children, ...props }: AccordionTriggerProps) {
  const inherited = useContext(HeadingLevel);
  const level = headingLevel ?? inherited;
  const Heading = `h${level}` as const;
  return (
    <BaseAccordion.Header className={styles.header} render={<Heading />}>
      <BaseAccordion.Trigger {...props} className={cx(styles.trigger, className)}>
        <span className={styles.title}>{children}</span>
        {meta !== undefined && <span className={styles.meta}>{meta}</span>}
        <ChevronDown aria-hidden className={styles.icon} />
      </BaseAccordion.Trigger>
    </BaseAccordion.Header>
  );
}

export type AccordionPanelProps = Omit<BaseAccordion.Panel.Props, "className"> & {
  className?: string;
  /** Class for the padded inner wrapper. */
  contentClassName?: string;
};

export function AccordionPanel({ className, contentClassName, children, ...props }: AccordionPanelProps) {
  return (
    <BaseAccordion.Panel {...props} className={cx(styles.panel, className)}>
      <div className={cx(styles.content, contentClassName)}>{children}</div>
    </BaseAccordion.Panel>
  );
}

/** Alias of AccordionPanel (shadcn naming). */
export const AccordionContent = AccordionPanel;
export type AccordionContentProps = AccordionPanelProps;
