/*
 * An agent's Conversations, for platform staff: every thread, private ones
 * too, with who sees it, who started it, who's in it and when it last moved.
 * Titles of private conversations (and of every one in a personal workspace
 * or of a service-managed agent) are withheld and read "Title hidden": the
 * Visibility column says whether a thread is private or shared. A platform admin may read one with a reason, recorded in the
 * team's audit log for its owners to see (ReadConversationDialog); auditors
 * only see the list, without titles.
 */
import { useQuery } from "@tanstack/react-query";
import { BookOpen, Lock, MessagesSquare, UsersRound } from "lucide-react";
import { useState } from "react";
import { type AdminAgentThread, adminThreadsQuery } from "../../../api/agents";
import { ApiError } from "../../../api/client";
import { RelativeTime } from "../../../components/templates/list-page";
import { plural } from "../../../lib/format";
import { Alert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { CellText, type DataTableColumn, DataTable } from "@/components/ui/data-table/data-table";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import shared from "../../shared.module.css";
import s from "./agents.module.css";
import type { Agent } from "./labels";
import { conversationName, conversationTitle, ReadConversationDialog } from "./read-conversation";

type Props = { agent: Agent; personal: boolean; canRead: boolean };

export function ConversationsTab({ agent, personal, canRead }: Props) {
  const threads = useQuery(adminThreadsQuery(agent.id));
  const [reading, setReading] = useState<AdminAgentThread | null>(null);
  const forbidden = threads.error instanceof ApiError && threads.error.status === 403;
  const hiddenTitles = personal
    ? "Titles in a personal workspace are hidden, shared or not."
    : agent.externalKey != null
      ? "Titles of a service-managed agent's conversations are hidden, shared or not."
      : "Titles of private conversations are hidden.";
  const readLabel = (t: AdminAgentThread) => {
    const by = t.createdBy?.name ?? "someone";
    return conversationTitle(t, canRead).hidden ? `Read the conversation started by ${by}…` : `Read ${conversationName(t, canRead)}, started by ${by}…`;
  };

  const columns: DataTableColumn<AdminAgentThread>[] = [
    {
      id: "title",
      header: "Conversation",
      rowHeader: true,
      accessor: (t) => conversationTitle(t, canRead).text,
      cell: (t) => {
        const title = conversationTitle(t, canRead);
        return <span className={title.hidden ? shared.muted : shared.primary}>{title.text}</span>;
      },
    },
    {
      id: "visibility",
      header: "Visibility",
      accessor: "visibility",
      cell: (t) =>
        t.visibility === "private" ? (
          <Badge size="sm">
            <Lock aria-hidden className={s.threadIcon} /> Private
          </Badge>
        ) : (
          <Badge size="sm" tone="info">
            <UsersRound aria-hidden className={s.threadIcon} /> Shared
          </Badge>
        ),
    },
    { id: "creator", header: "Started by", accessor: (t) => t.createdBy?.name ?? "", cell: (t) => (t.createdBy ? t.createdBy.name : <span className={shared.muted}>Unknown</span>) },
    {
      id: "participants",
      header: "People",
      accessor: (t) => t.participants.length,
      cell: (t) =>
        t.participants.length ? (
          <CellText primary={plural(t.participants.length, "person", "people")} secondary={t.participants.map((p) => p.name).join(", ")} />
        ) : (
          <span className={shared.muted}>—</span>
        ),
    },
    { id: "updated", header: "Last activity", accessor: (t) => t.updatedAt, cell: (t) => <RelativeTime value={new Date(t.updatedAt).toISOString()} /> },
  ];

  if (forbidden)
    return (
      <Card>
        <EmptyState
          icon={<Lock />}
          title="Only platform admins can see this agent's conversations."
          description="Opening one takes a reason, recorded in the team's audit log."
        />
      </Card>
    );

  return (
    <>
      {!agent.enabled && canRead ? (
        <Alert tone="info">This agent is turned off: its conversations can be read once it&apos;s turned on again.</Alert>
      ) : null}
      <Card
        title="Conversations"
        description={`Every conversation with ${agent.name}, private ones too. ${hiddenTitles} ${canRead ? "Reading one takes a reason, recorded in the team's audit log, which its owners can see." : "Only platform admins can read them."}`}
        flush
      >
        <DataTable<AdminAgentThread>
          caption={`Conversations with ${agent.name}`}
          stack
          columns={columns}
          data={threads.data ?? []}
          getRowId={(t) => t.threadId}
          rowLabel={(t) => conversationTitle(t, canRead).text}
          defaultSort={{ columnId: "updated", direction: "descending" }}
          loading={threads.isLoading}
          error={threads.error}
          onRetry={() => void threads.refetch()}
          empty={<EmptyState size="compact" icon={<MessagesSquare />} title="No conversations yet." />}
          rowActions={
            canRead
              ? (t) => (
                  <Button size="sm" variant="secondary" disabled={!agent.enabled} onClick={() => setReading(t)} aria-label={readLabel(t)}>
                    <BookOpen aria-hidden /> Read…
                  </Button>
                )
              : undefined
          }
        />
      </Card>
      {reading ? <ReadConversationDialog agent={agent} thread={reading} onClose={() => setReading(null)} /> : null}
    </>
  );
}
