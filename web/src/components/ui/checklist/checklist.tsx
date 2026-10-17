"use client";

import type { useRender } from "@base-ui/react/use-render";
import { Check, X } from "lucide-react";
import { type ComponentPropsWithRef, isValidElement, type ReactNode, useId } from "react";
import { Button, IconButton } from "@/components/ui/button/button";
import { Progress } from "@/components/ui/progress/progress";
import { cx, dataFlag } from "@/lib/bitop-utils";
import styles from "./checklist.module.css";

/*
 * Checklist: onboarding steps with done states ("Getting started", an admin
 * setup list). A titled section with a progress summary ("2 of 4 done") and
 * an ordered list of steps; each step has a title, an optional description
 * and an action (a link or a button). The first open step is the current
 * one: its action is the primary button.
 *
 *   <Checklist
 *     title="Get started"
 *     steps={[
 *       { id: "source", title: "Add a data source", done: true },
 *       { id: "kb", title: "Create a knowledge base", description: "…",
 *         done: false, action: { label: "Create", render: <Link to="/kbs/new" /> } },
 *     ]}
 *     onDismiss={() => hide()}
 *   />
 *
 * Done state is shown by an icon and spelled out for screen readers ("Done:"
 * / "To do:" before the title), not by colour. `onDismiss` adds a Dismiss
 * button; persisting the choice is up to the app.
 */

export type ChecklistAction = {
  label: string;
  /** Plain link. */
  href?: string;
  /** Router link or another element: `render={<Link to="/sources/new" />}`. */
  render?: useRender.RenderProp;
  onClick?: () => void;
};

export type ChecklistStep = {
  id: string;
  title: ReactNode;
  description?: ReactNode;
  done: boolean;
  /** A link or button to do the step (hidden once done). Pass an element for full control. */
  action?: ChecklistAction | ReactNode;
};

export type ChecklistLabels = {
  /** "2 of 4 done" */
  summary: (done: number, total: number) => string;
  done: string;
  todo: string;
  dismiss: string;
};

export type ChecklistProps = Omit<ComponentPropsWithRef<"section">, "title" | "children"> & {
  title: ReactNode;
  description?: ReactNode;
  steps: ChecklistStep[];
  /** Heading level of the title (default 2). */
  headingLevel?: 2 | 3 | 4;
  /** Adds a Dismiss button (×). */
  onDismiss?: () => void;
  /** Shown instead of the progress summary when every step is done. */
  complete?: ReactNode;
  /** Hide the progress bar (the "2 of 4 done" text stays). */
  hideProgress?: boolean;
  labels?: Partial<ChecklistLabels>;
};

const defaultLabels: ChecklistLabels = {
  summary: (done, total) => `${done} of ${total} done`,
  done: "Done",
  todo: "To do",
  dismiss: "Dismiss",
};

function isAction(a: unknown): a is ChecklistAction {
  return typeof a === "object" && a !== null && !isValidElement(a) && "label" in a;
}

export function Checklist({
  title,
  description,
  steps,
  headingLevel = 2,
  onDismiss,
  complete,
  hideProgress = false,
  labels: labelsProp,
  className,
  ...props
}: ChecklistProps) {
  const labels = { ...defaultLabels, ...labelsProp };
  const titleId = useId();
  const Heading = `h${headingLevel}` as const;
  const done = steps.filter((s) => s.done).length;
  const total = steps.length;
  const currentId = steps.find((s) => !s.done)?.id;
  const allDone = total > 0 && done === total;
  const summary = labels.summary(done, total);

  return (
    <section {...props} aria-labelledby={titleId} className={cx(styles.root, className)} data-complete={dataFlag(allDone)}>
      <div className={styles.header}>
        <div className={styles.heading}>
          <Heading id={titleId} className={styles.title}>
            {title}
          </Heading>
          {description && <p className={styles.description}>{description}</p>}
        </div>
        {onDismiss && (
          <IconButton
            size="sm"
            icon={<X aria-hidden />}
            label={typeof title === "string" ? `${labels.dismiss} ${title}` : labels.dismiss}
            onClick={onDismiss}
            className={styles.dismiss}
          />
        )}
      </div>
      {allDone && complete ? (
        <div className={styles.complete}>{complete}</div>
      ) : hideProgress ? (
        <p className={styles.summary}>{summary}</p>
      ) : (
        <Progress label={summary} value={total ? (done / total) * 100 : 0} showValue={false} size="sm" tone={allDone ? "success" : "primary"} />
      )}
      <ol className={styles.steps}>
        {steps.map((step, index) => {
          const current = step.id === currentId;
          return (
            <li key={step.id} className={styles.step} data-done={dataFlag(step.done)} data-current={dataFlag(current)}>
              <span aria-hidden className={styles.marker}>
                {step.done ? <Check /> : index + 1}
              </span>
              <div className={styles.body}>
                <p className={styles.stepTitle}>
                  <span className="sr-only">{step.done ? labels.done : labels.todo}: </span>
                  {step.title}
                </p>
                {step.description && <p className={styles.stepDescription}>{step.description}</p>}
              </div>
              {!step.done && step.action && (
                <div className={styles.action}>
                  {isAction(step.action) ? (
                    <Button
                      size="sm"
                      variant={current ? "primary" : "secondary"}
                      onClick={step.action.onClick}
                      render={step.action.render ?? (step.action.href !== undefined ? <a href={step.action.href} /> : undefined)}
                    >
                      {step.action.label}
                    </Button>
                  ) : (
                    step.action
                  )}
                </div>
              )}
            </li>
          );
        })}
      </ol>
    </section>
  );
}
