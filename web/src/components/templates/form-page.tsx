/*
 * FormPage (D4, revised 2026-09-28): a long create or edit form (a source, a
 * model connection, a model, a classification level) opens as a page of its
 * own over its list. Short forms stay in dialogs. The open form is ?form=<value>
 * (useFormParam), so reloading keeps it open and Back closes it. After an
 * edit, leaving any way (Cancel, the back link, the breadcrumb, browser Back,
 * the sidebar, a link) asks "Leave without saving?" first (m7).
 *
 *   const form = useFormParam();
 *   <Button onClick={() => form.open("new")}>New connection</Button>
 *   {form.id && <FormPage label="New connection" title="New connection" description=… onClose={form.close}
 *     onSubmit={() => save.mutate()} submitLabel="Create connection" busy={save.isPending}>…fields…</FormPage>}
 *
 * Mount it while the form is open; `onClose` after a successful save.
 *
 * Once a save started by Submit succeeds (a mutation run from `onSubmit`),
 * the form counts as saved until the next edit: closing it afterwards (or a
 * render landing before the page's own navigation) never asks "Leave without
 * saving?" (US-02: a new key's one-time secret was hidden that way).
 */
import { type Mutation, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, type ReactNode, type Ref, useEffect, useId, useRef, useState } from "react";
import { Button } from "@/components/ui/button/button";
import { Card, CardBody } from "@/components/ui/card/card";
import { type DescriptionEntry, DescriptionList } from "@/components/ui/description-list/description-list";
import { Form } from "@/components/ui/field/field";
import { SkeletonText } from "@/components/ui/skeleton/skeleton";
import { useCloseGuard, useEditTracker } from "./close-guard";
import { useRecordParam } from "./record-page";
import { TakeoverPage } from "./takeover";
import { useUnsavedChangesGuard } from "./unsaved-guard";
import styles from "./templates.module.css";

/** The open form's value (?form=): `open(value)` adds a history entry, `close()` goes back. */
export function useFormParam() {
  return useRecordParam("form");
}

export type FormPageProps = {
  /** Plain-text name: the breadcrumb and the page region's name (usually the title). */
  label: string;
  title: ReactNode;
  description?: ReactNode;
  onClose: () => void;
  onSubmit: () => void;
  submitLabel: ReactNode;
  busy?: boolean;
  submitDisabled?: boolean;
  /** Show a placeholder instead of the fields (their options are loading). */
  loading?: boolean;
  /** Key facts about the record being edited, in a card above the form. */
  facts?: DescriptionEntry[];
  /** Buttons at the start of the action row: "Back" to an earlier step, or a destructive action. */
  startActions?: ReactNode;
  formRef?: Ref<HTMLFormElement>;
  /** Unsaved edits, when the caller tracks them (default: anything typed or picked since it opened). */
  dirty?: boolean;
  children: ReactNode;
};

export function FormPage({
  label,
  title,
  description,
  onClose,
  onSubmit,
  submitLabel,
  busy,
  submitDisabled,
  loading,
  facts,
  startActions,
  formRef,
  dirty,
  children,
}: FormPageProps) {
  const formId = useId();
  const edits = useEditTracker();
  const saved = useSavedBySubmit();
  const unsaved = (dirty ?? edits.edited) && !busy && !saved.saved;
  // Closing on purpose (Cancel, the back link, after "Discard changes") turns
  // the navigation guard off first, so it doesn't ask a second time.
  const [leaves, setLeaves] = useState(0);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  useEffect(() => {
    if (leaves > 0) onCloseRef.current();
  }, [leaves]);
  const guard = useCloseGuard(unsaved, () => setLeaves((n) => n + 1));
  // Anything else that leaves (browser Back, the sidebar, a link) asks too.
  const navGuard = useUnsavedChangesGuard(unsaved && leaves === 0, { samePath: true });
  return (
    <TakeoverPage param="form" initialFocus="field" label={label} title={title} description={description} onBack={guard.requestClose}>
      {facts && facts.length > 0 && (
        <Card title="Details" titleAs="h2">
          <DescriptionList items={facts} dividers />
        </Card>
      )}
      <Card>
        <CardBody>
          {loading ? (
            <div role="status" aria-label="Loading…">
              <SkeletonText lines={5} />
            </div>
          ) : (
            <Form
              onInput={(e: FormEvent) => {
                edits.formProps.onInput(e);
                saved.edited();
              }}
              onChange={(e: FormEvent) => {
                edits.formProps.onChange(e);
                saved.edited();
              }}
              id={formId}
              ref={formRef}
              noValidate
              className={styles.formBody}
              onSubmit={(e) => {
                e.preventDefault();
                // Dialogs opened from inside the form bubble their submit here through the portal.
                if (e.target !== e.currentTarget) return;
                saved.track(onSubmit);
              }}
            >
              {children}
            </Form>
          )}
        </CardBody>
      </Card>
      <div className={styles.formActions}>
        {startActions && <div className={styles.formActionsStart}>{startActions}</div>}
        <Button variant="secondary" onClick={guard.requestClose}>
          Cancel
        </Button>
        <Button type="submit" form={formId} loading={busy} disabled={submitDisabled || loading}>
          {submitLabel}
        </Button>
      </div>
      {guard.dialog}
      {navGuard}
    </TakeoverPage>
  );
}

/** A titled group of fields inside a FormPage, laid out in two columns. */
export function FormSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <fieldset className={styles.formSection}>
      <legend className={styles.formSectionTitle}>{title}</legend>
      <div className={styles.formSectionBody}>{children}</div>
    </fieldset>
  );
}

/**
 * Whether a save started by Submit has succeeded since the last edit: the
 * mutations `onSubmit` starts (synchronously, as `save.mutate()` does) are
 * watched in the query client's mutation cache.
 */
function useSavedBySubmit() {
  const qc = useQueryClient();
  const [started, setStarted] = useState<Mutation<unknown, unknown, unknown, unknown>[]>([]);
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    if (started.length === 0) return;
    const cache = qc.getMutationCache();
    const check = () => {
      if (started.some((m) => m.state.status === "success")) setSaved(true);
    };
    check();
    return cache.subscribe(check);
  }, [qc, started]);
  return {
    saved,
    track(submit: () => void) {
      const cache = qc.getMutationCache();
      const before = new Set(cache.getAll());
      submit();
      const now = cache.getAll().filter((m) => !before.has(m));
      if (now.length > 0) setStarted(now as Mutation<unknown, unknown, unknown, unknown>[]);
    },
    edited() {
      if (saved || started.length > 0) {
        setSaved(false);
        setStarted([]);
      }
    },
  };
}
