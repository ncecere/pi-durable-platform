/*
 * A settings form's save bar that knows about save conflicts (AD-01): when
 * the object changed elsewhere while the person edited, a notice lists what
 * changed (their edits are still in the form) and the bar offers "Overwrite
 * with mine" and "Discard mine and load theirs" instead of Save and Discard.
 * SettingsPage uses it; a form with its own layout can too.
 */
import { type ReactNode, useEffect, useRef } from "react";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { SaveBar } from "@/components/ui/save-bar/save-bar";
import { isRevisionConflict, type RevisionControl, type ServerChange } from "./revision-form";
import styles from "./templates.module.css";

export const conflictLabels = { overwrite: "Overwrite with mine", discard: "Discard mine and load theirs" } as const;

export function ConflictNotice({ changes }: { changes: ServerChange[] | null }) {
  const ref = useRef<HTMLDivElement>(null);
  const loaded = changes !== null;
  // Brought into view above the sticky save bar when it appears and when the list arrives.
  // In braces: scrollIntoView may return a promise, which React would take for a cleanup function.
  useEffect(() => {
    ref.current?.scrollIntoView?.({ block: "nearest" });
  }, [loaded]);
  return (
    <Alert ref={ref} tone="warning" title="Someone else changed these settings while you were editing" className={styles.conflict}>
      {changes === null ? (
        <p>Your changes are still in the form. Loading the latest version…</p>
      ) : (
        <>
          <p>
            Your changes are still in the form. <strong>{conflictLabels.overwrite}</strong> saves your version over theirs;{" "}
            <strong>{conflictLabels.discard}</strong> shows theirs instead.
          </p>
          <ul className={styles.conflictList} aria-label="Changed elsewhere">
            {changes.map((c) => (
              <li key={c.key}>
                <span className={styles.conflictField}>{c.label}:</span> now “{c.theirs}”
                {c.where ? <> (changed on {c.where}; your save keeps it)</> : c.clash ? <> (yours: “{c.mine}”)</> : " (you didn't change it, so the form shows theirs)"}
              </li>
            ))}
          </ul>
        </>
      )}
    </Alert>
  );
}

type RevisionSaveBarProps = {
  /** The bar is shown (the form is edited, or a conflict waits for a choice). */
  open: boolean;
  revision?: RevisionControl;
  saving: boolean;
  error: unknown;
  message?: ReactNode;
  saveLabel: string;
  saveDisabled?: boolean;
  /** Saves (the Save button submits the form; Overwrite calls this). */
  onSave: () => void;
  onDiscard: () => void;
  /** The error alert's title. */
  errorTitle?: string;
  /** The page shows the save error itself (it still counts for the conflict). */
  hideError?: boolean;
};

/** Whether a conflict waits for the person's choice. */
export const conflictOpen = (revision?: RevisionControl) => Boolean(revision && (revision.changes || revision.waiting));

/** The conflict notice, the save error and the sticky save bar (its Save button submits the surrounding form). */
export function RevisionSaveBar({ open, revision, saving, error, message, saveLabel, saveDisabled = false, onSave, onDiscard, errorTitle = "Couldn't save the changes", hideError = false }: RevisionSaveBarProps) {
  const conflict = conflictOpen(revision);
  const status = revision?.status;
  useEffect(() => {
    status?.(saving, error);
  }, [status, saving, error]);
  return (
    <>
      {open && conflict && <ConflictNotice changes={revision?.changes ?? null} />}
      {open && !hideError && Boolean(error) && !(conflict && isRevisionConflict(error)) && <ErrorAlert error={error} title={errorTitle} />}
      {conflict && revision ? (
        <SaveBar open={open} message="Changed elsewhere while you were editing">
          <Button variant="ghost" disabled={saving || !revision.changes} onClick={revision.discardMine}>
            {conflictLabels.discard}
          </Button>
          <Button
            loading={saving}
            disabled={saveDisabled || !revision.changes}
            onClick={() => {
              revision.overwrite();
              revision.submitting();
              onSave();
            }}
          >
            {conflictLabels.overwrite}
          </Button>
        </SaveBar>
      ) : (
        <SaveBar open={open} message={message}>
          <Button variant="ghost" disabled={saving} onClick={onDiscard}>
            Discard
          </Button>
          <Button type="submit" loading={saving} disabled={saveDisabled}>
            {saveLabel}
          </Button>
        </SaveBar>
      )}
    </>
  );
}
