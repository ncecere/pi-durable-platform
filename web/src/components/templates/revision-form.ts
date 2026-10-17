/*
 * A settings form over a revisioned object (If-Match), that never throws away
 * what the person typed (AD-01). When the object changes elsewhere while they
 * edit (another tab, another admin), or a save comes back 412, the form keeps
 * their edits, takes the other changes into the fields they didn't touch, and
 * SettingsPage shows what changed with two choices: "Overwrite with mine" or
 * "Discard mine and load theirs".
 *
 *   const [form, setForm, revision] = useRevisionForm(formOf(team), team.revision, { labels, choices });
 *
 * `choices` gives selects' display labels for the list of changes; a record
 * shared by two pages (the cost settings) carries every field in both forms
 * and names the other page's in `elsewhere`, so a change there is listed too.
 *   <SettingsPage revision={revision} dirty=… onSave=… onDiscard=…>
 *
 * Don't key the editor by the revision (that remounts it and loses the
 * edits). A 412 refetches every query (main.tsx), so the latest version
 * arrives here.
 */
import { type SetStateAction, useCallback, useState } from "react";
import { ApiError } from "../../api/client";

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

/** Equal once the server's tidying is allowed for: text trimmed. */
const tidy = (v: unknown): unknown =>
  typeof v === "string" ? v.trim() : Array.isArray(v) ? v.map(tidy) : v && typeof v === "object" ? Object.fromEntries(Object.entries(v).map(([k, x]) => [k, tidy(x)])) : v;
const sameTidied = (a: unknown, b: unknown) => same(tidy(a), tidy(b));

/** The new version is the person's own save: it has every change they submitted (allowing for tidying). */
export function isOwnSave<T extends object>(base: T, submitted: T, saved: T): boolean {
  return (Object.keys(submitted) as (keyof T)[]).every((k) => same(submitted[k], base[k]) || sameTidied(submitted[k], saved[k]));
}

/** A save refused because the object changed since it was loaded (412 revision_conflict). */
export function isRevisionConflict(err: unknown): boolean {
  return err instanceof ApiError && err.status === 412;
}

/**
 * One field another person changed: their value, the person's own, and whether the person changed it too
 * (differently). `where` names the page that edits a field this form carries but doesn't show (AD2-08).
 */
export type ServerChange = { key: string; label: string; theirs: string; mine: string; clash: boolean; where?: string };

export type FieldLabels<T> = Partial<Record<keyof T & string, string>>;
export type FieldFormat<T> = (key: keyof T & string, value: unknown) => string;
/** A select's or radio group's display labels by stored value ("high" → "High"), so the list of changes reads like the form (AD2-17). */
export type FieldChoices<T> = Partial<Record<keyof T & string, Record<string, string>>>;
/** Fields of the record that this form carries but another page edits, with that page ("Costs → Settings"). */
export type FieldsElsewhere<T> = Partial<Record<keyof T & string, string>>;

export type RevisionOptions<T> = { labels?: FieldLabels<T>; format?: FieldFormat<T>; choices?: FieldChoices<T>; elsewhere?: FieldsElsewhere<T> };

/** The format that shows a choice's label, else the value as short text. */
export const choiceFormat =
  <T>(choices: FieldChoices<T> = {}): FieldFormat<T> =>
  (key, value) => {
    const label = choices[key]?.[String(value ?? "")];
    return label ?? formatValue(value);
  };

/** "maxClassification" → "Max classification". */
export const humanize = (key: string) => {
  const words = key.replace(/([a-z0-9])([A-Z])/g, "$1 $2").replace(/[_-]+/g, " ").toLowerCase();
  return words.charAt(0).toUpperCase() + words.slice(1);
};

/** A value as short text for the list of changes. */
export function formatValue(value: unknown): string {
  if (value === null || value === undefined || value === "") return "empty";
  if (typeof value === "boolean") return value ? "On" : "Off";
  if (typeof value === "number") return value.toLocaleString();
  if (typeof value === "string") return value.length > 80 ? `${value.slice(0, 79)}…` : value;
  if (Array.isArray(value) && value.every((v) => typeof v !== "object")) return value.length ? value.join(", ") : "none";
  return "changed";
}

/** The fields of `base` that `theirs` changed, with the person's values beside them. */
export function serverChanges<T extends object>(
  base: T,
  mine: T,
  theirs: T,
  labels: FieldLabels<T> = {},
  format: FieldFormat<T> = (_k, v) => formatValue(v),
  elsewhere: FieldsElsewhere<T> = {},
) {
  const keys = new Set([...Object.keys(base), ...Object.keys(theirs)]) as Set<keyof T & string>;
  const out: ServerChange[] = [];
  for (const key of keys) {
    if (same(base[key], theirs[key])) continue;
    const change: ServerChange = { key, label: labels[key] ?? humanize(key), theirs: format(key, theirs[key]), mine: format(key, mine[key]), clash: !same(mine[key], theirs[key]) };
    if (elsewhere[key]) change.where = elsewhere[key];
    out.push(change);
  }
  return out;
}

/** The latest version with the person's own changes (since `base`) on top. */
export function rebase<T extends object>(base: T, mine: T, theirs: T): T {
  const out = { ...theirs };
  for (const key of Object.keys(mine) as (keyof T)[]) if (!same(base[key], mine[key])) out[key] = mine[key];
  return out;
}

type Theirs<T> = { values: T; revision: number };

/** The form, what its edits started from, and an unresolved change made elsewhere. */
export type RevisionState<T> = {
  form: T;
  base: T;
  revision: number | undefined;
  theirs: Theirs<T> | null;
  /** The form when its last save started, until that save lands (a new revision) or fails. */
  submitted: T | null;
  /** A save is in flight: a new revision waits until it ends (it may be this save's, or the one it lost to). */
  saving: boolean;
  /** A save came back 412 and the latest version hasn't loaded yet. */
  waiting: boolean;
};

export function initialRevisionState<T>(saved: T, revision: number | undefined): RevisionState<T> {
  return { form: saved, base: saved, revision, theirs: null, submitted: null, saving: false, waiting: false };
}

/**
 * The state once the saved object (`saved` at `revision`) has loaded: the
 * person's own save landing, a quiet update of an untouched form, or a
 * change made elsewhere that the person decides about.
 */
export function reconcile<T extends object>(s: RevisionState<T>, saved: T, revision: number | undefined): RevisionState<T> {
  if (s.saving) return s;
  if (revision === undefined || revision === s.revision || revision === s.theirs?.revision) {
    // Same revision: an untouched form follows the loaded values (they can differ while a first load settles).
    return revision === s.revision && !s.theirs && same(s.form, s.base) && !same(s.base, saved) ? { ...s, form: saved, base: saved } : s;
  }
  const settled = { revision, theirs: null, submitted: null, waiting: false };
  if (s.submitted && isOwnSave(s.base, s.submitted, saved)) return { ...s, ...settled, form: afterOwnSave(s.form, s.submitted, saved), base: saved };
  if (same(s.form, s.base) || same(s.form, saved)) return { ...s, ...settled, form: saved, base: saved };
  // Nothing the form shows changed (another field of the object did): keep the edits on the new revision.
  if (same(s.base, saved)) return { ...s, ...settled };
  return { ...s, form: rebase(s.base, s.form, saved), theirs: { values: saved, revision }, submitted: null, waiting: false };
}

/**
 * The form once the person's own save landed: the server's values (it may
 * have tidied one, "Name " → "Name"), except in fields they typed in since
 * the save started.
 */
export function afterOwnSave<T extends object>(form: T, submitted: T, saved: T): T {
  const out = { ...saved };
  for (const key of Object.keys(form) as (keyof T)[]) if (!same(form[key], submitted[key])) out[key] = form[key];
  return out;
}

/** What SettingsPage needs to show and resolve a conflict. */
export type RevisionControl = {
  /** Fields changed elsewhere, while the person hasn't chosen yet. */
  changes: ServerChange[] | null;
  /** A save came back 412; the latest version is loading. */
  waiting: boolean;
  /** The form differs from what its edits started from. */
  edited: boolean;
  /** Called by SettingsPage on Save, before onSave. */
  submitting: () => void;
  /** Called by SettingsPage whenever its save starts or ends (with the error of a failed one). */
  status: (saving: boolean, error: unknown) => void;
  /** Keep the form (mine on top of theirs) on the latest revision; SettingsPage then saves. */
  overwrite: () => void;
  /** Drop the edits and show the latest version. */
  discardMine: () => void;
};

export function useRevisionForm<T extends object>(saved: T, revision: number | undefined, opts: RevisionOptions<T> = {}) {
  const [stored, setState] = useState(() => initialRevisionState(saved, revision));
  // Reconciled while rendering (not in an effect), so the page never renders the new data with the old form.
  const savedKey = JSON.stringify(saved);
  const [seen, setSeen] = useState({ key: savedKey, revision, saving: stored.saving });
  let state = stored;
  if (seen.key !== savedKey || seen.revision !== revision || seen.saving !== stored.saving) {
    state = reconcile(stored, saved, revision);
    setSeen({ key: savedKey, revision, saving: stored.saving });
    if (state !== stored) setState(state);
  }

  const setForm = useCallback((next: SetStateAction<T>) => setState((s) => ({ ...s, form: typeof next === "function" ? (next as (p: T) => T)(s.form) : next })), []);
  const { labels, format, choices, elsewhere } = opts;
  // Cheap, and the options may be built each render (a model list's names), so not memoised.
  const changes = state.theirs ? serverChanges(state.base, state.form, state.theirs.values, labels, format ?? choiceFormat(choices), elsewhere) : null;
  const control: RevisionControl = {
    changes,
    waiting: state.waiting,
    edited: !same(state.form, state.base),
    submitting: () => setState((s) => ({ ...s, submitted: s.form })),
    status: useCallback(
      (saving: boolean, error: unknown) =>
        setState((s) => {
          // A save started: what it sends is the form now.
          if (saving) return s.saving ? s : { ...s, saving, submitted: s.submitted ?? s.form };
          if (!error) return s.saving ? { ...s, saving } : s;
          // A failed save: whatever revision comes next isn't its own.
          return { ...s, saving, submitted: null, waiting: isRevisionConflict(error) && !s.theirs };
        }),
      [],
    ),
    overwrite: () => setState((s) => (s.theirs ? { ...s, base: s.theirs.values, revision: s.theirs.revision, theirs: null } : s)),
    discardMine: () =>
      setState((s) => (s.theirs ? { ...s, form: s.theirs.values, base: s.theirs.values, revision: s.theirs.revision, theirs: null } : { ...s, form: s.base, waiting: false })),
  };
  return [state.form, setForm, control] as const;
}
