/*
 * SettingsPage (D3): a settings tab or page as sections in one form, with
 * ONE sticky save bar and the unsaved-changes guard built in (F-17).
 *
 *   <SettingsPage dirty={changed} saving={save.isPending} error={save.error}
 *     onSave={() => save.mutate(form)} onDiscard={reset}>
 *     <SettingsSection title="General">…fields…</SettingsSection>
 *     <SettingsSection title="Crawling">…</SettingsSection>
 *     <DangerZone>
 *       <DangerAction title="Delete this source" description="…"
 *         action={<Button variant="danger" onClick={…}>Delete source</Button>} />
 *     </DangerZone>
 *   </SettingsPage>
 *
 * Danger-zone buttons act on their own (with a confirmation); they're not
 * part of the save. Give them type="button" (bitop's Button's default).
 *
 * A form over a revisioned object passes `revision` from useRevisionForm:
 * when the object changes elsewhere while the person edits, or a save comes
 * back 412, their edits stay, a notice lists what changed, and the save bar
 * offers "Overwrite with mine" and "Discard mine and load theirs" (AD-01).
 */
import { type FormEvent, type ReactNode, useEffect } from "react";
import { Alert } from "@/components/ui/alert/alert";
import { Card } from "@/components/ui/card/card";
import { Form } from "@/components/ui/field/field";
import { terms } from "@/lib/terms";
import styles from "./templates.module.css";
import { type UnsavedGuardOptions, useUnsavedChangesGuard } from "./unsaved-guard";
import { conflictOpen, RevisionSaveBar } from "./conflict-notice";
import type { RevisionControl } from "./revision-form";

export type SettingsPageProps = {
  /** The form differs from what's saved: shows the save bar and arms the guard. */
  dirty: boolean;
  onSave: () => void;
  onDiscard: () => void;
  saving?: boolean;
  /** The save failed. Shown above the save bar. */
  error?: unknown;
  /** Save button text (default "Save settings": one rule for settings pages; dialogs say "Save <thing>", AD2-16). */
  saveLabel?: string;
  /** Save bar text (default "Unsaved changes"); e.g. "Not saved: fix the highlighted field". */
  message?: ReactNode;
  /** Viewers who can't edit get no save bar (and no guard), and a note saying so. */
  canEdit?: boolean;
  /** The read-only note (default: the viewer's role can't change these settings); null when the page already says so above. */
  readOnlyNote?: ReactNode;
  /** Disable Save, e.g. while a field is invalid. */
  saveDisabled?: boolean;
  guard?: UnsavedGuardOptions;
  /** From useRevisionForm: keeps the edits through a change made elsewhere and asks whose to keep. */
  revision?: RevisionControl;
  children: ReactNode;
  className?: string;
};

export function SettingsPage({
  dirty,
  onSave,
  onDiscard,
  saving = false,
  error,
  saveLabel = "Save settings",
  message,
  canEdit = true,
  readOnlyNote = "You can view these settings. Your role can't change them.",
  saveDisabled = false,
  guard,
  revision,
  children,
  className,
}: SettingsPageProps) {
  const conflict = conflictOpen(revision);
  const open = canEdit && (dirty || conflict);
  // A settings page is one form: switching its page's tabs (?tab=) leaves it too.
  const dialog = useUnsavedChangesGuard(open, { samePath: true, ...guard });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!open || saving || saveDisabled || conflict) return;
    revision?.submitting();
    onSave();
  };
  return (
    <Form noValidate onSubmit={submit} className={className ?? styles.settings}>
      {/* One untitled info notice, worded like the team pages' (VI-20). */}
      {!canEdit && readOnlyNote !== null && <Alert tone="info">{readOnlyNote}</Alert>}
      {children}
      <RevisionSaveBar
        open={open}
        revision={revision}
        saving={saving}
        error={error}
        message={message}
        saveLabel={saveLabel}
        saveDisabled={saveDisabled}
        onSave={onSave}
        onDiscard={onDiscard}
      />
      {dialog}
    </Form>
  );
}

export type SettingsSectionProps = {
  title: ReactNode;
  description?: ReactNode;
  /** Controls in the section header, e.g. a "Test" button. */
  actions?: ReactNode;
  /** Anchor: a link with this hash (#ocr) scrolls the section into view once it renders (⌘K deep links). */
  id?: string;
  children: ReactNode;
};

/** One titled group of settings (General, Crawling, …). */
export function SettingsSection({ title, description, actions, id, children }: SettingsSectionProps) {
  useEffect(() => {
    if (id && globalThis.location?.hash === `#${id}`) document.getElementById(id)?.scrollIntoView?.({ block: "start" });
  }, [id]);
  return (
    <Card id={id} title={title} description={description} actions={actions}>
      <div className={styles.sectionBody}>{children}</div>
    </Card>
  );
}

/** The last section: irreversible or disruptive actions, each with its own confirmation. */
export function DangerZone({ children, description }: { children: ReactNode; description?: ReactNode }) {
  return (
    <Card title={terms.dangerZone} description={description} className={styles.dangerZone}>
      <div className={styles.dangerList}>{children}</div>
    </Card>
  );
}

export type DangerActionProps = {
  title: ReactNode;
  description: ReactNode;
  /** The button, e.g. <Button variant="danger">Delete source</Button>. Disable it with a reason when it can't run. */
  action: ReactNode;
  /** Why the action is unavailable (P-04), shown under the description. */
  disabledReason?: ReactNode;
};

export function DangerAction({ title, description, action, disabledReason }: DangerActionProps) {
  return (
    <div className={styles.dangerRow}>
      <div className={styles.dangerText}>
        <h3 className={styles.dangerTitle}>{title}</h3>
        <p className={styles.dangerDescription}>{description}</p>
        {disabledReason && <p className={styles.dangerReason}>{disabledReason}</p>}
      </div>
      <div className={styles.dangerButton}>{action}</div>
    </div>
  );
}
