/*
 * Pure diff helpers for DiffViewer (no React): a line diff (Myers) and a
 * structural JSON diff. Exported so apps can summarise changes ("3 fields
 * changed") without rendering the viewer.
 */

export type DiffLine = {
  kind: "equal" | "add" | "remove";
  text: string;
  /** 1-based line number in `before` (equal and remove lines). */
  oldNumber?: number;
  /** 1-based line number in `after` (equal and add lines). */
  newNumber?: number;
};

export type TextDiff = {
  lines: DiffLine[];
  added: number;
  removed: number;
  /** The inputs were too different to align within the budget: every old line is shown removed and every new line added. */
  approximate: boolean;
};

export function splitLines(text: string): string[] {
  if (text === "") return [];
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  // A trailing newline doesn't add an empty last line.
  if (lines[lines.length - 1] === "") lines.pop();
  return lines;
}

/**
 * Line diff with Myers' O(ND) algorithm after trimming the common prefix and
 * suffix. `maxEdits` bounds the work for very different inputs; past it the
 * result is a plain "remove all, add all" marked `approximate`.
 */
export function diffText(before: string, after: string, maxEdits = 4000): TextDiff {
  const a = splitLines(before);
  const b = splitLines(after);
  let start = 0;
  while (start < a.length && start < b.length && a[start] === b[start]) start++;
  let endA = a.length;
  let endB = b.length;
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--;
    endB--;
  }
  const A = a.slice(start, endA);
  const B = b.slice(start, endB);
  const middle = myers(A, B, maxEdits);
  const approximate = middle === null;
  const ops: DiffLine["kind"][] = middle ?? [...A.map(() => "remove" as const), ...B.map(() => "add" as const)];

  const lines: DiffLine[] = [];
  let oldNo = 0;
  let newNo = 0;
  const equal = (text: string) => lines.push({ kind: "equal", text, oldNumber: ++oldNo, newNumber: ++newNo });
  for (let i = 0; i < start; i++) equal(a[i]!);
  let ia = 0;
  let ib = 0;
  for (const op of ops) {
    if (op === "equal") {
      equal(A[ia++]!);
      ib++;
    } else if (op === "remove") lines.push({ kind: "remove", text: A[ia++]!, oldNumber: ++oldNo });
    else lines.push({ kind: "add", text: B[ib++]!, newNumber: ++newNo });
  }
  for (let i = endA; i < a.length; i++) equal(a[i]!);
  return {
    lines,
    added: lines.filter((l) => l.kind === "add").length,
    removed: lines.filter((l) => l.kind === "remove").length,
    approximate,
  };
}

/** Edit script from A to B, or null when it needs more than `maxEdits` edits. */
function myers(A: string[], B: string[], maxEdits: number): DiffLine["kind"][] | null {
  const N = A.length;
  const M = B.length;
  if (N === 0) return B.map(() => "add");
  if (M === 0) return A.map(() => "remove");
  const max = N + M;
  const o = max + 1;
  const v = new Int32Array(2 * max + 3);
  // trace[d] holds v for k in [-d, d] after step d (index k + d).
  const trace: Int32Array[] = [];
  let last = -1;
  outer: for (let d = 0; d <= max; d++) {
    if (d > maxEdits) return null;
    for (let k = -d; k <= d; k += 2) {
      let x = k === -d || (k !== d && v[o + k - 1]! < v[o + k + 1]!) ? v[o + k + 1]! : v[o + k - 1]! + 1;
      let y = x - k;
      while (x < N && y < M && A[x] === B[y]) {
        x++;
        y++;
      }
      v[o + k] = x;
      if (x >= N && y >= M) {
        trace.push(v.slice(o - d, o + d + 1));
        last = d;
        break outer;
      }
    }
    trace.push(v.slice(o - d, o + d + 1));
  }

  const ops: DiffLine["kind"][] = [];
  let x = N;
  let y = M;
  for (let d = last; d > 0; d--) {
    const prev = trace[d - 1]!;
    const at = (k: number) => prev[k + d - 1]!;
    const k = x - y;
    const down = k === -d || (k !== d && at(k - 1) < at(k + 1));
    const prevK = down ? k + 1 : k - 1;
    const prevX = at(prevK);
    const prevY = prevX - prevK;
    while (x > prevX && y > prevY) {
      ops.push("equal");
      x--;
      y--;
    }
    ops.push(down ? "add" : "remove");
    x = prevX;
    y = prevY;
  }
  while (x > 0 && y > 0) {
    ops.push("equal");
    x--;
    y--;
  }
  return ops.reverse();
}

/* ---------------- JSON ---------------- */

export type JsonChange = {
  kind: "added" | "removed" | "changed";
  /** Where the change is, e.g. `limits.maxPages` or `tags[2]`. "" is the root value. */
  path: string;
  before?: unknown;
  after?: unknown;
};

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);

function keyPath(parent: string, key: string): string {
  const safe = /^[A-Za-z_$][\w$]*$/.test(key) ? key : JSON.stringify(key);
  if (!parent) return safe.startsWith('"') ? `[${safe}]` : safe;
  return safe.startsWith('"') ? `${parent}[${safe}]` : `${parent}.${safe}`;
}

function sameValue(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (typeof a !== typeof b || a === null || b === null || typeof a !== "object") return Number.isNaN(a) && Number.isNaN(b);
  if (Array.isArray(a) !== Array.isArray(b)) return false;
  if (Array.isArray(a)) {
    const bb = b as unknown[];
    return a.length === bb.length && a.every((x, i) => sameValue(x, bb[i]));
  }
  const ka = Object.keys(a as object);
  const kb = Object.keys(b as object);
  return ka.length === kb.length && ka.every((k) => Object.prototype.hasOwnProperty.call(b, k) && sameValue((a as Record<string, unknown>)[k], (b as Record<string, unknown>)[k]));
}

/**
 * Structural diff of two JSON values: added, removed and changed keys with
 * their paths, in document order (keys of `before` first, then new keys).
 * Arrays are compared by index; a value that changes type is "changed".
 */
export function diffJson(before: unknown, after: unknown, path = ""): JsonChange[] {
  if (sameValue(before, after)) return [];
  if (isObject(before) && isObject(after)) {
    const out: JsonChange[] = [];
    for (const key of Object.keys(before)) {
      const p = keyPath(path, key);
      if (!Object.prototype.hasOwnProperty.call(after, key)) out.push({ kind: "removed", path: p, before: before[key] });
      else out.push(...diffJson(before[key], after[key], p));
    }
    for (const key of Object.keys(after)) {
      if (!Object.prototype.hasOwnProperty.call(before, key)) out.push({ kind: "added", path: keyPath(path, key), after: after[key] });
    }
    return out;
  }
  if (Array.isArray(before) && Array.isArray(after)) {
    const out: JsonChange[] = [];
    const n = Math.max(before.length, after.length);
    for (let i = 0; i < n; i++) {
      const p = `${path}[${i}]`;
      if (i >= after.length) out.push({ kind: "removed", path: p, before: before[i] });
      else if (i >= before.length) out.push({ kind: "added", path: p, after: after[i] });
      else out.push(...diffJson(before[i], after[i], p));
    }
    return out;
  }
  if (before === undefined) return [{ kind: "added", path, after }];
  if (after === undefined) return [{ kind: "removed", path, before }];
  return [{ kind: "changed", path, before, after }];
}

/** Parses a JSON string; other values (and invalid JSON) are returned as they are. */
export function parseJsonValue(value: unknown): unknown {
  if (typeof value !== "string") return value;
  try {
    return JSON.parse(value);
  } catch {
    return value;
  }
}

/** A JSON value as display text: strings quoted, objects and arrays indented. */
export function formatJsonValue(value: unknown): string {
  if (value === undefined) return "";
  const text = JSON.stringify(value, null, 2);
  return text === undefined ? String(value) : text;
}
