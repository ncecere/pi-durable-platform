"use client";

import { Collapsible } from "@base-ui/react/collapsible";
import { ChevronDown, CircleAlert, Wrench } from "lucide-react";
import { type ReactNode, isValidElement } from "react";
import { StatusBadge } from "@/components/ui/badge/badge";
import { CodeBlock } from "@/components/ui/code-block/code-block";
import { cx, type Tone } from "@/lib/bitop-utils";
import styles from "./tool.module.css";

/*
 * Tool: a collapsible card for one tool call (Base UI Collapsible).
 *
 *   <Tool>
 *     <ToolHeader name="search_documents" state="completed" />
 *     <ToolContent>
 *       <ToolInput input={{ query: "refund policy" }} />
 *       <ToolOutput output={result} />
 *     </ToolContent>
 *   </Tool>
 *
 * The state is always spelled out in the badge ("Running"), never colour
 * only; the trigger's accessible name includes it.
 */

export type ToolState = "pending" | "running" | "completed" | "error";

const stateBadge: Record<ToolState, { tone: Tone; label: string; pulse?: boolean }> = {
  pending: { tone: "neutral", label: "Pending" },
  running: { tone: "info", label: "Running", pulse: true },
  completed: { tone: "success", label: "Completed" },
  error: { tone: "danger", label: "Error" },
};

export type ToolProps = Omit<Collapsible.Root.Props, "className" | "onOpenChange"> & {
  className?: string;
  onOpenChange?: (open: boolean) => void;
};

export function Tool({ className, onOpenChange, ...props }: ToolProps) {
  return <Collapsible.Root {...props} onOpenChange={onOpenChange ? (o) => onOpenChange(o) : undefined} className={cx(styles.root, className)} />;
}

export type ToolHeaderProps = {
  /** Tool name, e.g. "search_documents". */
  name: string;
  /** Human title shown instead of the name, e.g. "Searched the knowledge base". */
  title?: ReactNode;
  state: ToolState;
  /** Decorative icon (default: a wrench). */
  icon?: ReactNode;
  /** Override the badge text for a state. */
  stateLabel?: string;
  /** Short result text after the badge, e.g. "5 results" (part of the trigger's accessible name). */
  summary?: ReactNode;
  className?: string;
};

export function ToolHeader({ name, title, state, icon, stateLabel, summary, className }: ToolHeaderProps) {
  const badge = stateBadge[state];
  return (
    <Collapsible.Trigger className={cx(styles.trigger, className)} data-state={state}>
      <span aria-hidden className={styles.icon}>
        {icon ?? <Wrench />}
      </span>
      <span className={styles.name}>{title ?? <code>{name}</code>}</span>{" "}
      <StatusBadge tone={badge.tone} pulse={badge.pulse} size="sm" className={styles.badge}>
        {stateLabel ?? badge.label}
      </StatusBadge>
      {summary && (
        <>
          {" "}
          <span className={styles.summary}>{summary}</span>
        </>
      )}
      <ChevronDown aria-hidden className={styles.chevron} />
    </Collapsible.Trigger>
  );
}

export type ToolContentProps = { className?: string; children: ReactNode };

export function ToolContent({ className, children }: ToolContentProps) {
  return (
    <Collapsible.Panel className={cx(styles.panel, className)}>
      <div className={styles.content}>{children}</div>
    </Collapsible.Panel>
  );
}

/** Pretty JSON for tool arguments and results; strings are shown as-is. */
export function formatToolValue(value: unknown): string {
  if (typeof value === "string") return value;
  if (value === undefined) return "";
  try {
    return JSON.stringify(value, null, 2) ?? String(value);
  } catch {
    return String(value);
  }
}

export type ToolInputProps = {
  input: unknown;
  label?: ReactNode;
  className?: string;
};

export function ToolInput({ input, label = "Parameters", className }: ToolInputProps) {
  return (
    <section className={cx(styles.section, className)}>
      <p className={styles.sectionLabel}>{label}</p>
      <CodeBlock code={formatToolValue(input)} language={typeof input === "string" ? "text" : "json"} hideHeader />
    </section>
  );
}

export type ToolOutputProps = {
  /** A React node is rendered as-is; other values are shown as pretty JSON. */
  output?: unknown;
  /** Shown instead of the output when the call failed. */
  errorText?: ReactNode;
  label?: ReactNode;
  className?: string;
};

export function ToolOutput({ output, errorText, label, className }: ToolOutputProps) {
  if (errorText) {
    return (
      <section className={cx(styles.section, className)}>
        <p className={styles.sectionLabel}>{label ?? "Error"}</p>
        <div className={styles.error}>
          <CircleAlert aria-hidden className={styles.errorIcon} />
          <div>{errorText}</div>
        </div>
      </section>
    );
  }
  if (output === undefined || output === null) return null;
  const node = isValidElement(output) ? output : null;
  return (
    <section className={cx(styles.section, className)}>
      <p className={styles.sectionLabel}>{label ?? "Result"}</p>
      {node ?? <CodeBlock code={formatToolValue(output)} language={typeof output === "string" ? "text" : "json"} hideHeader maxHeight="20rem" />}
    </section>
  );
}
