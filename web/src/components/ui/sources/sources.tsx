"use client";

import { Collapsible } from "@base-ui/react/collapsible";
import { ArrowUpRight, BookOpen, ChevronDown, FileText } from "lucide-react";
import { type ComponentPropsWithRef, type ReactNode, useId } from "react";
import { cx } from "@/lib/bitop-utils";
import styles from "./sources.module.css";

/*
 * Sources: a collapsible "Used 4 sources" list under an answer (Base UI
 * Collapsible: the trigger is a button with aria-expanded / aria-controls).
 *
 *   <Sources>
 *     <SourcesTrigger count={sources.length} />
 *     <SourcesContent>
 *       {sources.map((s, i) => <Source key={s.href} index={i + 1} {...s} />)}
 *     </SourcesContent>
 *   </Sources>
 *
 * Give each Source the same `index` as its [n] citation marker so the two
 * can be matched up. To let a citation chip jump to its source, give the
 * Source an `id` and `tabIndex={-1}` and focus it from InlineCitation's
 * `onActivate` (open the list first); it shows a focus ring while focused,
 * and while it has a `data-highlighted` attribute.
 *
 * With `onSelect`, the card is a button that opens the source somewhere in
 * the app (e.g. a side panel with the passage); its `href` then becomes a
 * separate "open the link" icon link beside it. `selectLabel` names the
 * button ("Show source 2: Leave policy"); its meta and description describe
 * it.
 *
 *   <Source index={2} title="Leave policy" href={url} onSelect={() => openViewer(2)} selectLabel="Show source 2: Leave policy" />
 */

export type SourcesProps = Omit<Collapsible.Root.Props, "className" | "onOpenChange"> & {
  className?: string;
  onOpenChange?: (open: boolean) => void;
};

export function Sources({ className, onOpenChange, ...props }: SourcesProps) {
  return <Collapsible.Root {...props} onOpenChange={onOpenChange ? (o) => onOpenChange(o) : undefined} className={cx(styles.root, className)} />;
}

export type SourcesTriggerProps = Omit<Collapsible.Trigger.Props, "className"> & {
  /** Number of sources; the default label is "Used N sources". */
  count: number;
  className?: string;
  children?: ReactNode;
};

export function SourcesTrigger({ count, className, children, ...props }: SourcesTriggerProps) {
  return (
    <Collapsible.Trigger {...props} className={cx(styles.trigger, className)}>
      <BookOpen aria-hidden className={styles.triggerIcon} />
      <span>{children ?? `Used ${count} ${count === 1 ? "source" : "sources"}`}</span>
      <ChevronDown aria-hidden className={styles.chevron} />
    </Collapsible.Trigger>
  );
}

export type SourcesContentProps = Omit<Collapsible.Panel.Props, "className"> & {
  className?: string;
  /** Accessible name of the list. */
  label?: string;
};

export function SourcesContent({ className, children, label = "Sources", ...props }: SourcesContentProps) {
  return (
    <Collapsible.Panel {...props} className={cx(styles.panel, className)}>
      <ol aria-label={label} className={styles.list}>
        {children}
      </ol>
    </Collapsible.Panel>
  );
}

export type SourceProps = Omit<ComponentPropsWithRef<"li">, "title"> & {
  title: ReactNode;
  /** Link to the source; opens in a new tab. Without it the source is plain text (e.g. an uploaded file). */
  href?: string;
  /** Snippet or summary. */
  description?: ReactNode;
  /** Muted meta text, e.g. "example.com" or "p. 12". Defaults to the href's hostname. */
  meta?: ReactNode;
  /** Decorative favicon / file icon (defaults to a document icon). */
  icon?: ReactNode;
  /** Citation number, matching the [n] markers in the answer. */
  index?: number;
  /** Makes the card a button that opens the source in the app; the href becomes a separate link beside it. */
  onSelect?: () => void;
  /** The button's accessible name with onSelect (default: its title and number). */
  selectLabel?: string;
  /** The separate link's accessible name with onSelect and href (default "Open the link"); "(opens in a new tab)" is added. */
  linkLabel?: string;
};

function hostname(href: string | undefined) {
  if (!href) return undefined;
  try {
    return new URL(href).hostname.replace(/^www\./, "");
  } catch {
    return undefined;
  }
}

export function Source({ title, href, description, meta, icon, index, onSelect, selectLabel, linkLabel, className, ...props }: SourceProps) {
  const id = useId();
  const metaText = meta ?? hostname(href);
  const body = (
    <>
      <span aria-hidden className={styles.icon}>
        {icon ?? <FileText />}
      </span>
      <span className={styles.text}>
        <span className={styles.title}>
          {index !== undefined && (
            <span className={styles.index}>
              <span className="sr-only">Source</span> {index}
              <span className="sr-only">:</span>
            </span>
          )}{" "}
          {title}
          {href && !onSelect && <ArrowUpRight aria-hidden className={styles.external} />}
          {href && !onSelect && " "}
          {href && !onSelect && <span className="sr-only">(opens in a new tab)</span>}
        </span>
        {metaText && (
          <span id={`${id}-meta`} className={styles.meta}>
            {metaText}
          </span>
        )}
        {description && (
          <span id={`${id}-description`} className={styles.description}>
            {description}
          </span>
        )}
      </span>
    </>
  );
  if (onSelect) {
    const describedBy = [metaText && `${id}-meta`, description && `${id}-description`].filter(Boolean).join(" ") || undefined;
    return (
      <li {...props} className={cx(styles.source, className)} data-selectable="">
        <button type="button" className={styles.row} onClick={onSelect} aria-label={selectLabel} aria-describedby={selectLabel ? describedBy : undefined}>
          {body}
        </button>
        {href && (
          <a href={href} target="_blank" rel="noreferrer noopener" className={styles.link}>
            <ArrowUpRight aria-hidden />
            <span className="sr-only">{linkLabel ?? "Open the link"} (opens in a new tab)</span>
          </a>
        )}
      </li>
    );
  }
  return (
    <li {...props} className={cx(styles.source, className)}>
      {href ? (
        <a href={href} target="_blank" rel="noreferrer noopener" className={styles.row}>
          {body}
        </a>
      ) : (
        <div className={styles.row}>{body}</div>
      )}
    </li>
  );
}
