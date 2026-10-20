/*
 * A platform admin reads one conversation: first the reason (10–500
 * characters), which the platform records in the agent's team's audit log
 * before answering; then the conversation as it was at that moment, read-only,
 * with the documents the agent exposes in it (each read with the same reason,
 * and recorded too).
 */
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { type AdminAgentThread, adminReadThread, adminReadThreadDocument, type ChatView } from "../../../api/agents";
import { errorMessage } from "../../../api/client";
import { FormDialog } from "../../../components/form-dialog";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Conversation, ConversationContent, ConversationEmptyState } from "@/components/ui/conversation/conversation";
import { Dialog, DialogClose } from "@/components/ui/dialog/dialog";
import { Disclosure } from "@/components/ui/disclosure/disclosure";
import { Field } from "@/components/ui/field/field";
import { Textarea } from "@/components/ui/input/input";
import { Time } from "@/components/ui/time/time";
import { toast } from "@/components/ui/toast/toast";
import s from "./agents.module.css";
import type { Agent } from "./labels";
import { ChatMessages } from "./messages";
import { DocumentBody } from "./thread-documents";

export const REASON_MIN = 10;
export const REASON_MAX = 500;

/**
 * A thread's title as staff may see it: withheld for private ones, every one in a personal workspace or of a
 * service-managed agent, and for auditors. A withheld title says only that ("Title hidden"), not why: a shared
 * thread's title can be hidden too, and the Visibility column says who sees it.
 */
export function conversationTitle(t: AdminAgentThread, canRead: boolean): { text: string; hidden: boolean } {
  if (t.titleHidden || t.title === null || !canRead) return { text: hiddenTitle, hidden: true };
  return { text: t.title, hidden: false };
}

export const hiddenTitle = "Title hidden";

/** How a sentence names the thread: its title in quotes, or "this conversation" when the title is withheld. */
export function conversationName(t: AdminAgentThread, canRead: boolean): string {
  const title = conversationTitle(t, canRead);
  return title.hidden ? "this conversation" : `“${title.text}”`;
}

/** Why the reason can't be sent yet, or undefined. */
export function reasonProblem(reason: string): string | undefined {
  const n = [...reason.trim()].length;
  if (n < REASON_MIN) return `Give a reason of at least ${REASON_MIN} characters.`;
  if (n > REASON_MAX) return `At most ${REASON_MAX} characters.`;
  return undefined;
}

type Props = { agent: Agent; thread: AdminAgentThread; onClose: () => void };

export function ReadConversationDialog({ agent, thread, onClose }: Props) {
  const qc = useQueryClient();
  const [reason, setReason] = useState("");
  const [submitted, setSubmitted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [snapshot, setSnapshot] = useState<{ view: ChatView; at: number; documents: { kind: string; value?: unknown; error?: string }[] } | null>(
    null,
  );
  const problem = reasonProblem(reason);
  const shown = conversationTitle(thread, true);
  const title = shown.hidden ? `Conversation (${hiddenTitle.toLowerCase()})` : shown.text;

  const submit = async () => {
    setSubmitted(true);
    if (problem) return;
    setBusy(true);
    setError(null);
    try {
      const view = await adminReadThread(agent.id, thread.threadId, reason.trim());
      const documents = await Promise.all(
        Object.keys(view.documents ?? {}).map((kind) =>
          adminReadThreadDocument(agent.id, thread.threadId, kind, reason.trim()).then(
            (doc) => ({ kind, value: doc.value as unknown }),
            (err: unknown) => ({ kind, error: errorMessage(err) }),
          ),
        ),
      );
      setSnapshot({ view, at: Date.now(), documents });
      // The read is in the team's audit log now.
      void qc.invalidateQueries({ queryKey: ["team", agent.teamSlug, "audit"] });
    } catch (err) {
      setError(err);
      toast.error("Couldn't open the conversation", errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  if (snapshot)
    return (
      <Dialog
        open
        onOpenChange={(o) => !o && onClose()}
        size="xl"
        title={title}
        description={
          <>
            Read-only, as of <Time value={snapshot.at} format="datetime" />. This read is recorded in {agent.teamName}&apos;s audit log.
          </>
        }
        footer={<DialogClose>Close</DialogClose>}
      >
        {snapshot.documents.map((d) => (
          <Disclosure key={d.kind} title={d.kind}>
            {d.error ? <p className={s.hint}>{d.error}</p> : <DocumentBody kind={d.kind} value={d.value} />}
          </Disclosure>
        ))}
        <Conversation className={s.snapshot} aria-label={`Conversation: ${title}`}>
          <ConversationContent>
            {snapshot.view.messages.length > 0 ? (
              <ChatMessages view={snapshot.view} participants={thread.participants.length} lastReplyAt={thread.lastReplyAt} />
            ) : (
              <ConversationEmptyState title="No messages" description="This conversation has no messages." />
            )}
          </ConversationContent>
        </Conversation>
      </Dialog>
    );

  return (
    <FormDialog
      title={`Read ${conversationName(thread, true)}?`}
      description="Conversations belong to the people in them. Say why you need to read this one. Recorded in the team's audit log, which the owner can see."
      onClose={onClose}
      onSubmit={() => void submit()}
      submitLabel="Read conversation"
      busy={busy}
      formProps={{ noValidate: true }}
    >
      {error ? <ErrorAlert error={error} title="Couldn't open the conversation" /> : null}
      <Field
        label="Reason"
        description={`${REASON_MIN}–${REASON_MAX} characters, for example the support ticket you're working on. ${[...reason.trim()].length}/${REASON_MAX}`}
        error={submitted ? problem : undefined}
      >
        <Textarea value={reason} rows={3} maxLength={REASON_MAX} aria-required onChange={(e) => setReason(e.target.value)} />
      </Field>
    </FormDialog>
  );
}
