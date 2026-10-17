"use client";

import { Collapsible } from "@base-ui/react/collapsible";
import { Brain, ChevronDown } from "lucide-react";
import { type ReactNode, type RefObject, createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import { Shimmer } from "@/components/ui/shimmer/shimmer";
import { cx } from "@/lib/bitop-utils";
import styles from "./reasoning.module.css";

/*
 * Reasoning: the model's "thinking" in a collapsible panel (Base UI
 * Collapsible).
 *
 *   <Reasoning streaming={isReasoning}>
 *     <ReasoningTrigger />
 *     <ReasoningContent><Response streaming={isReasoning}>{text}</Response></ReasoningContent>
 *   </Reasoning>
 *
 * - Opens automatically when `streaming` becomes true, and closes once,
 *   `autoCloseDelay` ms after it becomes false.
 * - Once the user toggles it, it never moves on its own again.
 * - If focus is inside the panel when it closes automatically, focus moves
 *   to the trigger instead of being lost to <body>.
 * - The trigger reads "Thinking…" (shimmering) while streaming, then
 *   "Thought for N seconds" (measured, or the `duration` prop).
 * - Controlled (`open` + `onOpenChange`) or uncontrolled (`defaultOpen`).
 *   When controlled, the automatic opens/closes are requested through
 *   onOpenChange.
 */

type ReasoningContextValue = {
  streaming: boolean;
  duration: number | undefined;
  open: boolean;
  triggerRef: RefObject<HTMLButtonElement | null>;
  panelRef: RefObject<HTMLDivElement | null>;
};
const ReasoningContext = createContext<ReasoningContextValue | null>(null);

export function useReasoning() {
  const ctx = useContext(ReasoningContext);
  if (!ctx) throw new Error("Reasoning parts must be inside <Reasoning>");
  return ctx;
}

export type ReasoningProps = {
  /** True while reasoning tokens are streaming. */
  streaming?: boolean;
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Thinking time in seconds. Measured from `streaming` when omitted. */
  duration?: number;
  /** Close automatically once streaming ends (default true). */
  autoClose?: boolean;
  /** Delay before the automatic close, in ms. */
  autoCloseDelay?: number;
  className?: string;
  children: ReactNode;
};

export function Reasoning({
  streaming = false,
  open,
  defaultOpen,
  onOpenChange,
  duration: durationProp,
  autoClose = true,
  autoCloseDelay = 1000,
  className,
  children,
}: ReasoningProps) {
  const [internalOpen, setInternalOpen] = useState(defaultOpen ?? streaming);
  const isOpen = open ?? internalOpen;
  const [measured, setMeasured] = useState<number | undefined>(undefined);
  const userToggled = useRef(false);
  const closedOnce = useRef(false);
  const startedAt = useRef<number | null>(streaming ? Date.now() : null);
  const wasStreaming = useRef(streaming);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const panelRef = useRef<HTMLDivElement | null>(null);

  const request = useCallback(
    (next: boolean) => {
      if (open === undefined) setInternalOpen(next);
      onOpenChange?.(next);
    },
    [open, onOpenChange],
  );

  useEffect(() => {
    const was = wasStreaming.current;
    wasStreaming.current = streaming;
    if (streaming && !was) {
      startedAt.current = Date.now();
      if (!userToggled.current && !isOpen) request(true);
      return;
    }
    if (!streaming && was) {
      if (startedAt.current !== null) setMeasured(Math.max(1, Math.round((Date.now() - startedAt.current) / 1000)));
      startedAt.current = null;
      if (!autoClose || userToggled.current || closedOnce.current) return;
      closedOnce.current = true;
      const t = setTimeout(() => {
        if (userToggled.current) return;
        // Keyboard users reading the panel would otherwise lose focus to <body>.
        const panel = panelRef.current;
        if (panel && typeof document !== "undefined" && panel.contains(document.activeElement)) triggerRef.current?.focus();
        request(false);
      }, autoCloseDelay);
      return () => clearTimeout(t);
    }
    // Only transitions of `streaming` matter; isOpen / request are read at that moment.
  }, [streaming]);

  return (
    <ReasoningContext.Provider value={{ streaming, duration: durationProp ?? measured, open: isOpen, triggerRef, panelRef }}>
      <Collapsible.Root
        open={isOpen}
        onOpenChange={(next) => {
          userToggled.current = true;
          request(next);
        }}
        className={cx(styles.root, className)}
        data-streaming={streaming ? "" : undefined}
      >
        {children}
      </Collapsible.Root>
    </ReasoningContext.Provider>
  );
}

/** The default trigger label. */
export function reasoningLabel(streaming: boolean, duration: number | undefined): ReactNode {
  if (streaming) return <Shimmer>Thinking…</Shimmer>;
  if (duration === undefined) return "Thought for a few seconds";
  return `Thought for ${duration} ${duration === 1 ? "second" : "seconds"}`;
}

export type ReasoningTriggerProps = {
  /** Custom label: `(streaming, duration) => node`. */
  getLabel?: (streaming: boolean, duration: number | undefined) => ReactNode;
  className?: string;
  children?: ReactNode;
};

export function ReasoningTrigger({ getLabel = reasoningLabel, className, children }: ReasoningTriggerProps) {
  const { streaming, duration, triggerRef } = useReasoning();
  return (
    <Collapsible.Trigger ref={triggerRef} className={cx(styles.trigger, className)}>
      <Brain aria-hidden className={styles.icon} />
      <span className={styles.label}>{children ?? getLabel(streaming, duration)}</span>
      <ChevronDown aria-hidden className={styles.chevron} />
    </Collapsible.Trigger>
  );
}

export type ReasoningContentProps = {
  className?: string;
  children: ReactNode;
};

export function ReasoningContent({ className, children }: ReasoningContentProps) {
  const { panelRef } = useReasoning();
  return (
    <Collapsible.Panel ref={panelRef} className={cx(styles.panel, className)}>
      <div className={styles.content}>{children}</div>
    </Collapsible.Panel>
  );
}
