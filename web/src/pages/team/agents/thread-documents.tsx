/*
 * A conversation's exposed documents (docs/custom-agents.md, Custom state for
 * apps), beside the chat: each one read from the documents endpoint at the
 * revision the thread's view names, so a document.updated event (a new
 * revision in the live view) reloads it. The first-party kinds' documents
 * (docs/first-party-agents.md) have their own renderings: the triage queue
 * as a table, the research plan as a checklist and its sources as a list.
 * Any other document is shown as JSON.
 */
import { useQuery } from "@tanstack/react-query";
import { type ChatView, threadDocumentQuery } from "../../../api/agents";
import { errorMessage } from "../../../api/client";
import { Badge } from "@/components/ui/badge/badge";
import { Checklist } from "@/components/ui/checklist/checklist";
import { CodeBlock } from "@/components/ui/code-block/code-block";
import { Disclosure } from "@/components/ui/disclosure/disclosure";
import { Source, Sources, SourcesContent, SourcesTrigger } from "@/components/ui/sources/sources";
import { Table, Td, Tr } from "@/components/ui/table/table";
import s from "./agents.module.css";

type Props = { agentId: string; threadId: string; documents: ChatView["documents"] };

/** The documents panel: nothing when the thread exposes none. */
export function ThreadDocuments({ agentId, threadId, documents }: Props) {
  const kinds = Object.entries(documents ?? {}).sort(([a], [b]) => order(a) - order(b) || a.localeCompare(b));
  if (kinds.length === 0) return null;
  return (
    <aside className={s.documents} aria-label="Documents">
      {kinds.map(([kind, ref]) => (
        <ThreadDocumentView key={kind} agentId={agentId} threadId={threadId} kind={kind} revision={ref.revision} />
      ))}
    </aside>
  );
}

const KNOWN = ["research.plan", "research.sources", "triage.queue"];
const order = (kind: string) => (KNOWN.includes(kind) ? KNOWN.indexOf(kind) : KNOWN.length);

const titles: Record<string, string> = { "triage.queue": "Triage queue", "research.plan": "Research plan", "research.sources": "Sources" };

function ThreadDocumentView({ agentId, threadId, kind, revision }: { agentId: string; threadId: string; kind: string; revision: string }) {
  const doc = useQuery({ ...threadDocumentQuery(agentId, threadId, kind, revision), placeholderData: (previous) => previous });
  const value = doc.data?.value;
  return (
    <Disclosure defaultOpen title={titles[kind] ?? kind}>
      {doc.isError ? (
        <p className={s.hint}>{errorMessage(doc.error)}</p>
      ) : value === undefined ? (
        <p className={s.hint}>Loading…</p>
      ) : (
        <DocumentBody kind={kind} value={value} />
      )}
    </Disclosure>
  );
}

/** A document's value, rendered by its kind. Exported for tests. */
export function DocumentBody({ kind, value }: { kind: string; value: unknown }) {
  if (kind === "triage.queue" && isObject(value) && Array.isArray(value.items)) return <TriageQueue items={value.items.filter(isObject)} />;
  if (kind === "research.plan" && isObject(value) && Array.isArray(value.steps)) return <ResearchPlan question={str(value.question)} steps={value.steps.filter(isObject)} />;
  if (kind === "research.sources" && isObject(value) && Array.isArray(value.sources)) return <ResearchSources sources={value.sources.filter(isObject)} />;
  return <CodeBlock code={JSON.stringify(value, null, 2)} language="json" hideHeader />;
}

type Obj = Record<string, unknown>;
const isObject = (v: unknown): v is Obj => typeof v === "object" && v !== null && !Array.isArray(v);
const str = (v: unknown) => (typeof v === "string" ? v : "");
const strings = (v: unknown) => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []);
/** Only http(s) links are links: a document is the agent's, and its URLs come from what it read. */
const safeHref = (v: unknown) => (typeof v === "string" && /^https?:\/\//i.test(v) ? v : undefined);

const statusTone = (status: string) =>
  status === "done" ? "success" : status === "waiting_approval" ? "warning" : status === "skipped" ? "neutral" : "info";
const statusLabel = (status: string) => status.replace(/_/g, " ");

function TriageQueue({ items }: { items: Obj[] }) {
  return (
    <Table
      caption="Triage queue"
      columns={["Item", "Status", "Decision", "Draft"]}
      density="compact"
      maxHeight="20rem"
      stickyHeader
      empty={items.length === 0 ? <span className={s.hint}>Nothing seen yet.</span> : undefined}
    >
      {items.map((item) => {
        const href = safeHref(item.url);
        const title = `${str(item.id)} ${str(item.title)}`.trim();
        const labels = strings(item.labels);
        return (
          <Tr key={str(item.id)}>
            <Td>{href ? <a href={href} target="_blank" rel="noreferrer noopener">{title}</a> : title}</Td>
            <Td>
              <Badge size="sm" tone={statusTone(str(item.status))}>
                {statusLabel(str(item.status))}
              </Badge>
            </Td>
            <Td>{str(item.decision) || "—"}</Td>
            <Td>
              {labels.length ? <span>Labels: {labels.join(", ")}. </span> : null}
              {str(item.reply) || str(item.summary) || (labels.length ? null : "—")}
            </Td>
          </Tr>
        );
      })}
    </Table>
  );
}

function ResearchPlan({ question, steps }: { question: string; steps: Obj[] }) {
  if (steps.length === 0) return <p className={s.hint}>No plan yet.</p>;
  return (
    <Checklist
      headingLevel={3}
      title={question || "Plan"}
      steps={steps.map((step) => {
        const status = str(step.status);
        const via = str(step.subagent);
        const notes = [status === "done" ? "" : statusLabel(status), via ? `${via} subagent` : "", str(step.note)].filter(Boolean).join(" · ");
        return { id: str(step.id), title: str(step.text), description: notes || undefined, done: status === "done" || status === "skipped" };
      })}
    />
  );
}

function ResearchSources({ sources }: { sources: Obj[] }) {
  if (sources.length === 0) return <p className={s.hint}>No sources yet.</p>;
  return (
    <Sources defaultOpen>
      <SourcesTrigger count={sources.length} />
      <SourcesContent label="Research sources">
        {sources.map((source, i) => (
          <Source
            key={str(source.url) || i}
            index={i + 1}
            title={str(source.title) || str(source.url)}
            href={safeHref(source.url)}
            description={strings(source.supports).join("; ") || undefined}
          />
        ))}
      </SourcesContent>
    </Sources>
  );
}
