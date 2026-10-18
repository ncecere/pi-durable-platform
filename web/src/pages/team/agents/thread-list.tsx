/*
 * The test chat's threads: each with its title, a lock for a private one,
 * and for a shared one who started it and, with several people, their
 * avatars. Also who-sees-it for the open thread's header.
 */
import { Lock, Trash2, UsersRound } from "lucide-react";
import type { AgentThread } from "../../../api/agents";
import { plural } from "../../../lib/format";
import { Avatar } from "@/components/ui/avatar/avatar";
import { Badge } from "@/components/ui/badge/badge";
import { IconButton } from "@/components/ui/button/button";
import s from "./agents.module.css";

type Principal = { kind: string; id: string; name: string } | null | undefined;

/** "you" for the viewer, else their name. */
export const whoIs = (p: Principal, viewerId: string | undefined) => (p ? (p.kind === "user" && p.id === viewerId ? "you" : p.name) : undefined);

/** "Started by Alex" ("by you" for the viewer); undefined when unknown. */
export function startedBy(t: Pick<AgentThread, "createdBy">, viewerId?: string) {
  const who = whoIs(t.createdBy, viewerId);
  return who ? `Started by ${who}` : undefined;
}

type ListProps = {
  threads: AgentThread[];
  activeId?: string;
  viewerId?: string;
  /** A personal workspace: only its owner, so no one to share with. */
  personal: boolean;
  onSelect: (id: string) => void;
  onDelete: (id: string) => void;
};

export function ThreadList({ threads, activeId, viewerId, personal, onSelect, onDelete }: ListProps) {
  return (
    <ul className={s.threadList}>
      {threads.map((t) => (
        <li key={t.threadId} className={s.threadRow} data-active={t.threadId === activeId || undefined}>
          <button type="button" className={s.threadButton} onClick={() => onSelect(t.threadId)} aria-current={t.threadId === activeId || undefined}>
            <span className={s.threadTitle}>
              {t.visibility === "private" && !personal ? <Lock aria-hidden className={s.threadIcon} /> : null}
              <span className={s.truncate}>{t.title}</span>
            </span>
            {personal ? null : <ThreadSubline thread={t} viewerId={viewerId} />}
          </button>
          <IconButton icon={<Trash2 aria-hidden />} label={`Delete chat “${t.title}”`} size="sm" onClick={() => onDelete(t.threadId)} />
        </li>
      ))}
    </ul>
  );
}

/** Under a thread's title: "Private", or who started it and how many people are in it. */
function ThreadSubline({ thread, viewerId }: { thread: AgentThread; viewerId?: string }) {
  if (thread.visibility === "private") return <span className={s.threadSub}>Private</span>;
  const people = thread.participants;
  const by = startedBy(thread, viewerId);
  if (!by && people.length < 2) return null;
  return (
    <span className={s.threadSub}>
      {by ? <span className={s.truncate}>{by}</span> : null}
      {people.length > 1 ? (
        <span className={s.people} title={people.map((p) => p.name).join(", ")}>
          <span className={s.avatars} aria-hidden>
            {people.slice(0, 3).map((p) => (
              <Avatar key={p.id} name={p.name} size="xs" decorative />
            ))}
          </span>
          {plural(people.length, "person", "people")}
        </span>
      ) : null}
    </span>
  );
}

/** The open thread's header: a Private badge, or who started a shared one. */
export function ThreadAudience({ thread, viewerId }: { thread: AgentThread; viewerId?: string }) {
  if (thread.visibility === "private")
    return (
      <Badge size="sm">
        <Lock aria-hidden className={s.threadIcon} /> Private
      </Badge>
    );
  const by = startedBy(thread, viewerId);
  const people = thread.participants.length;
  return (
    <span className={s.audience}>
      {/* The icon and its word stay on one line; who started it and the people wrap after them. */}
      <span className={s.audienceLabel}>
        <UsersRound aria-hidden className={s.threadIcon} /> Shared
      </span>
      {by ? ` · ${by}` : ""}
      {people > 1 ? ` · ${plural(people, "person", "people")}` : ""}
    </span>
  );
}
