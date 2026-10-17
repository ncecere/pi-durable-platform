"use client";

import { Fragment, type ReactNode, useMemo, useState } from "react";
import { ScrollArea } from "@/components/ui/scroll-area/scroll-area";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group/toggle-group";
import {
  diffJson,
  diffText,
  formatJsonValue,
  parseJsonValue,
  type DiffLine,
  type JsonChange,
} from "@/components/ui/diff-viewer/diff";
import { cx } from "@/lib/bitop-utils";
import styles from "./diff-viewer.module.css";

/*
 * DiffViewer: before/after for text and JSON, e.g. an audit entry or two
 * versions of an agent's settings.
 *
 *   <DiffViewer label="Changes to the agent" format="json" before={entry.before} after={entry.after} />
 *   <DiffViewer label="System prompt" before={v1.prompt} after={v2.prompt} defaultMode="split" />
 *
 * - format="text": a line diff (Myers). Runs of unchanged lines longer than
 *   2 × `contextLines` are folded into a "Show N unchanged lines" button.
 * - format="json": a structural diff of the two values (strings are parsed):
 *   one row per added, removed or changed key, with its path
 *   (`limits.maxPages`, `tags[2]`) and the old and new values.
 * - mode="unified" (one column, old then new) or "split" (before | after),
 *   switchable with the built-in toggle (`showModeToggle`).
 *
 * Changes are never shown by colour alone: every changed line has a visible
 * + / − (or ~ for a changed key) and a visually hidden "Added" / "Removed" /
 * "Changed", and the summary above says how much changed. The diff is a
 * table named by `label`, inside a ScrollArea (a focusable region when it
 * overflows) so long values scroll instead of stretching the page.
 */

export type DiffMode = "unified" | "split";

export type DiffViewerLabels = {
  before: string;
  after: string;
  added: string;
  removed: string;
  changed: string;
  /** "Unified" / "Split" in the mode toggle, and its group name. */
  unified: string;
  split: string;
  mode: string;
  field: string;
  noChanges: string;
  notSet: string;
  /** "Show 12 unchanged lines" */
  showUnchanged: (count: number) => string;
  /** Text summary, e.g. "4 lines added, 1 removed". */
  textSummary: (added: number, removed: number) => string;
  /** JSON summary, e.g. "3 changes: 1 added, 1 removed, 1 changed". */
  jsonSummary: (added: number, removed: number, changed: number) => string;
  approximate: string;
};

const plural = (n: number, one: string, many: string) => `${n.toLocaleString()} ${n === 1 ? one : many}`;

const defaultLabels: DiffViewerLabels = {
  before: "Before",
  after: "After",
  added: "Added",
  removed: "Removed",
  changed: "Changed",
  unified: "Unified",
  split: "Split",
  mode: "Diff layout",
  field: "Field",
  noChanges: "No changes",
  notSet: "Not set",
  showUnchanged: (n) => `Show ${plural(n, "unchanged line", "unchanged lines")}`,
  textSummary: (a, r) => `${plural(a, "line", "lines")} added, ${r.toLocaleString()} removed`,
  jsonSummary: (a, r, c) => `${plural(a + r + c, "change", "changes")}: ${a} added, ${r} removed, ${c} changed`,
  approximate: "The versions are too different to align line by line; all old lines are shown removed and all new lines added.",
};

export type DiffViewerProps = {
  /** Names the diff table and scroll region, e.g. "Changes to Support agent". */
  label: string;
  /** Old value: text, or (format="json") any JSON value or JSON string. */
  before: unknown;
  after: unknown;
  format?: "text" | "json";
  mode?: DiffMode;
  defaultMode?: DiffMode;
  onModeChange?: (mode: DiffMode) => void;
  /** Show the Unified / Split toggle (default true). */
  showModeToggle?: boolean;
  /** Unchanged lines kept around each change (text). Default 3. */
  contextLines?: number;
  /** Max height of the scroll area (default "24rem"). */
  maxHeight?: string;
  /** Wrap long lines (default true); otherwise they scroll sideways. */
  wrap?: boolean;
  /** Extra controls in the header, e.g. a Copy button. */
  actions?: ReactNode;
  labels?: Partial<DiffViewerLabels>;
  className?: string;
};

export function DiffViewer({
  label,
  before,
  after,
  format = "text",
  mode: modeProp,
  defaultMode = "unified",
  onModeChange,
  showModeToggle = true,
  contextLines = 3,
  maxHeight = "24rem",
  wrap = true,
  actions,
  labels: labelsProp,
  className,
}: DiffViewerProps) {
  const labels = { ...defaultLabels, ...labelsProp };
  const [innerMode, setInnerMode] = useState<DiffMode>(defaultMode);
  const mode = modeProp ?? innerMode;
  const setMode = (m: DiffMode) => {
    if (modeProp === undefined) setInnerMode(m);
    onModeChange?.(m);
  };

  const text = useMemo(() => (format === "text" ? diffText(toText(before), toText(after)) : null), [format, before, after]);
  const changes = useMemo(() => (format === "json" ? diffJson(parseJsonValue(before), parseJsonValue(after)) : null), [format, before, after]);

  let summary: string;
  let empty = false;
  if (text) {
    empty = text.added === 0 && text.removed === 0;
    summary = empty ? labels.noChanges : labels.textSummary(text.added, text.removed);
  } else {
    const count = (k: JsonChange["kind"]) => changes!.filter((c) => c.kind === k).length;
    empty = changes!.length === 0;
    summary = empty ? labels.noChanges : labels.jsonSummary(count("added"), count("removed"), count("changed"));
  }

  return (
    <div className={cx(styles.root, className)} data-mode={mode} data-wrap={wrap ? "" : undefined}>
      <div className={styles.header}>
        <p className={styles.summary}>{summary}</p>
        <div className={styles.actions}>
          {actions}
          {showModeToggle && !empty && (
            <ToggleGroup
              aria-label={labels.mode}
              size="sm"
              variant="outline"
              joined
              value={[mode]}
              onValueChange={(v) => {
                const next = v[0] as DiffMode | undefined;
                if (next) setMode(next);
              }}
            >
              <ToggleGroupItem value="unified">{labels.unified}</ToggleGroupItem>
              <ToggleGroupItem value="split">{labels.split}</ToggleGroupItem>
            </ToggleGroup>
          )}
        </div>
      </div>
      {text?.approximate && <p className={styles.note}>{labels.approximate}</p>}
      {!empty && (
        <ScrollArea label={label} maxHeight={maxHeight} orientation={wrap ? "vertical" : "both"} className={styles.scroll}>
          {text ? (
            <TextDiffTable label={label} lines={text.lines} mode={mode} contextLines={contextLines} labels={labels} />
          ) : (
            <JsonDiffTable label={label} changes={changes!} mode={mode} labels={labels} />
          )}
        </ScrollArea>
      )}
    </div>
  );
}

function toText(value: unknown): string {
  if (value === undefined || value === null) return "";
  return typeof value === "string" ? value : formatJsonValue(value);
}

/* ---------------- Text ---------------- */

type Block = { type: "lines"; lines: DiffLine[] } | { type: "fold"; id: number; lines: DiffLine[] };

/** Splits lines into visible runs and folds of unchanged lines far from any change. */
function foldUnchanged(lines: DiffLine[], context: number): Block[] {
  const blocks: Block[] = [];
  let run: DiffLine[] = [];
  let foldId = 0;
  const flush = (atStart: boolean, atEnd: boolean) => {
    const keepBefore = atStart ? 0 : context;
    const keepAfter = atEnd ? 0 : context;
    if (run.length > keepBefore + keepAfter + 1) {
      if (keepBefore) blocks.push({ type: "lines", lines: run.slice(0, keepBefore) });
      blocks.push({ type: "fold", id: foldId++, lines: run.slice(keepBefore, run.length - keepAfter) });
      if (keepAfter) blocks.push({ type: "lines", lines: run.slice(run.length - keepAfter) });
    } else if (run.length) blocks.push({ type: "lines", lines: run });
    run = [];
  };
  let seenChange = false;
  // Consecutive changed lines stay in one block, so split mode can pair them.
  let changes: DiffLine[] = [];
  for (const line of lines) {
    if (line.kind === "equal") {
      if (changes.length) blocks.push({ type: "lines", lines: changes });
      changes = [];
      run.push(line);
      continue;
    }
    if (!changes.length) flush(!seenChange, false);
    seenChange = true;
    changes.push(line);
  }
  if (changes.length) blocks.push({ type: "lines", lines: changes });
  flush(!seenChange, true);
  return blocks;
}

type Labels = DiffViewerLabels;

function Sign({ kind, labels, word: wordProp }: { kind: DiffLine["kind"] | JsonChange["kind"]; labels: Labels; word?: string }) {
  const sign = kind === "add" || kind === "added" ? "+" : kind === "remove" || kind === "removed" ? "−" : kind === "changed" ? "~" : " ";
  const word =
    wordProp ?? (kind === "add" || kind === "added" ? labels.added : kind === "remove" || kind === "removed" ? labels.removed : kind === "changed" ? labels.changed : null);
  return (
    <>
      <span aria-hidden className={styles.sign}>
        {sign}
      </span>
      {word && <span className="sr-only">{word}: </span>}
    </>
  );
}

type Row = { left?: DiffLine; right?: DiffLine };

/** Pairs removed and added lines side by side within each change. */
function toSplitRows(lines: DiffLine[]): Row[] {
  const rows: Row[] = [];
  let removed: DiffLine[] = [];
  let added: DiffLine[] = [];
  const flush = () => {
    for (let i = 0; i < Math.max(removed.length, added.length); i++) rows.push({ left: removed[i], right: added[i] });
    removed = [];
    added = [];
  };
  for (const line of lines) {
    if (line.kind === "remove") removed.push(line);
    else if (line.kind === "add") added.push(line);
    else {
      flush();
      rows.push({ left: line, right: line });
    }
  }
  flush();
  return rows;
}

function TextDiffTable({ label, lines, mode, contextLines, labels }: { label: string; lines: DiffLine[]; mode: DiffMode; contextLines: number; labels: Labels }) {
  const [expanded, setExpanded] = useState<Set<number>>(() => new Set());
  const blocks = useMemo(() => foldUnchanged(lines, Math.max(0, contextLines)), [lines, contextLines]);
  const cols = mode === "split" ? 4 : 3;

  return (
    <table className={styles.table}>
      <caption className="sr-only">{label}</caption>
      <thead className={mode === "unified" ? styles.hiddenHead : undefined}>
        {mode === "split" ? (
          <tr>
            <th scope="col" className={styles.numHead}>
              <span className="sr-only">{labels.before} line</span>
            </th>
            <th scope="col">{labels.before}</th>
            <th scope="col" className={styles.numHead}>
              <span className="sr-only">{labels.after} line</span>
            </th>
            <th scope="col">{labels.after}</th>
          </tr>
        ) : (
          <tr>
            <th scope="col">{labels.before} line</th>
            <th scope="col">{labels.after} line</th>
            <th scope="col">Content</th>
          </tr>
        )}
      </thead>
      <tbody>
        {blocks.map((block, bi) => {
          if (block.type === "fold" && !expanded.has(block.id)) {
            return (
              <tr key={`fold-${block.id}`} className={styles.foldRow}>
                <td colSpan={cols}>
                  <button type="button" className={styles.fold} onClick={() => setExpanded((s) => new Set(s).add(block.id))}>
                    {labels.showUnchanged(block.lines.length)}
                  </button>
                </td>
              </tr>
            );
          }
          if (mode === "split") {
            return (
              <Fragment key={bi}>
                {toSplitRows(block.lines).map((row, ri) => (
                  <tr key={ri}>
                    <SplitCells line={row.left} side="left" labels={labels} />
                    <SplitCells line={row.right} side="right" labels={labels} />
                  </tr>
                ))}
              </Fragment>
            );
          }
          return (
            <Fragment key={bi}>
              {block.lines.map((line, li) => (
                <tr key={li} data-kind={line.kind}>
                  <td className={styles.num}>{line.oldNumber}</td>
                  <td className={styles.num}>{line.newNumber}</td>
                  <td className={styles.code}>
                    <Sign kind={line.kind} labels={labels} />
                    <span className={styles.text}>{line.text}</span>
                  </td>
                </tr>
              ))}
            </Fragment>
          );
        })}
      </tbody>
    </table>
  );
}

function SplitCells({ line, side, labels }: { line?: DiffLine; side: "left" | "right"; labels: Labels }) {
  if (!line) {
    return (
      <>
        <td className={styles.num} data-kind="none" />
        <td className={styles.code} data-kind="none" />
      </>
    );
  }
  const number = side === "left" ? line.oldNumber : line.newNumber;
  return (
    <>
      <td className={styles.num} data-kind={line.kind}>
        {number}
      </td>
      <td className={styles.code} data-kind={line.kind}>
        <Sign kind={line.kind} labels={labels} />
        <span className={styles.text}>{line.text}</span>
      </td>
    </>
  );
}

/* ---------------- JSON ---------------- */

function Value({ value, labels }: { value: unknown; labels: Labels }) {
  if (value === undefined) {
    return (
      <span className={styles.notSet}>
        <span aria-hidden>—</span>
        <span className="sr-only">{labels.notSet}</span>
      </span>
    );
  }
  return <span className={styles.text}>{formatJsonValue(value)}</span>;
}

function JsonDiffTable({ label, changes, mode, labels }: { label: string; changes: JsonChange[]; mode: DiffMode; labels: Labels }) {
  const path = (c: JsonChange) => <code className={styles.path}>{c.path || "(root)"}</code>;
  if (mode === "split") {
    return (
      <table className={styles.table}>
        <caption className="sr-only">{label}</caption>
        <thead>
          <tr>
            <th scope="col">{labels.field}</th>
            <th scope="col">{labels.before}</th>
            <th scope="col">{labels.after}</th>
          </tr>
        </thead>
        <tbody>
          {changes.map((c) => (
            <tr key={`${c.kind}-${c.path}`} data-kind={c.kind}>
              <th scope="row" className={styles.field}>
                <Sign kind={c.kind} labels={labels} />
                {path(c)}
              </th>
              <td className={styles.code} data-kind={c.kind === "added" ? "none" : "remove"}>
                <Value value={c.before} labels={labels} />
              </td>
              <td className={styles.code} data-kind={c.kind === "removed" ? "none" : "add"}>
                <Value value={c.after} labels={labels} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    );
  }
  // Unified: a changed key is two lines, − old then + new.
  const rows: { key: string; kind: "add" | "remove"; change: JsonChange; value: unknown }[] = [];
  for (const c of changes) {
    if (c.kind !== "added") rows.push({ key: `${c.path}-old`, kind: "remove", change: c, value: c.before });
    if (c.kind !== "removed") rows.push({ key: `${c.path}-new`, kind: "add", change: c, value: c.after });
  }
  return (
    <table className={styles.table}>
      <caption className="sr-only">{label}</caption>
      <thead className={styles.hiddenHead}>
        <tr>
          <th scope="col">{labels.field}</th>
          <th scope="col">Value</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.key} data-kind={r.kind}>
            <th scope="row" className={styles.field}>
              <Sign
                kind={r.kind}
                labels={labels}
                word={r.change.kind === "changed" ? `${labels.changed}, ${(r.kind === "remove" ? labels.before : labels.after).toLocaleLowerCase()}` : undefined}
              />
              {path(r.change)}
            </th>
            <td className={styles.code}>
              <Value value={r.value} labels={labels} />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
