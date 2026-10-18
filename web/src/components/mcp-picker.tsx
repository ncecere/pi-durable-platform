/*
 * The "MCP servers" section of the template dialogs: the servers the team may
 * attach, each with all its tools or chosen ones, and whether apps may give
 * agents their own servers for a thread. A server the current version uses
 * that isn't offered any more (turned off, unpublished) stays listed, marked
 * unavailable, so it can be removed. A team template is offered its team's
 * own servers too, badged "This team" ("This workspace"); a platform template
 * (or one in the catalog) platform servers only (template-models.ts).
 */
import type { McpAttach, McpAttached, McpServer } from "@/api/mcp";
import { Badge } from "@/components/ui/badge/badge";
import { Checkbox } from "@/components/ui/checkbox/checkbox";
import { Fieldset } from "@/components/ui/field/field";
import { RadioGroup } from "@/components/ui/radio-group/radio-group";
import { plural } from "@/lib/format";
import s from "../pages/shared.module.css";
import m from "./mcp-picker.module.css";

/** A version's servers as a create body's `mcp`. */
export const attachOf = (attached: McpAttached[] | null | undefined): McpAttach[] =>
  (attached ?? []).map((a) => (a.tools == null ? { serverId: a.serverId } : { serverId: a.serverId, tools: a.tools }));

const key = (list: McpAttach[]) =>
  JSON.stringify(
    [...list].sort((a, b) => a.serverId.localeCompare(b.serverId)).map((a) => [a.serverId, a.tools == null ? null : [...a.tools].sort()]),
  );

/** Whether two choices attach the same servers and tools, in any order. */
export const sameMcp = (a: McpAttach[], b: McpAttach[]) => key(a) === key(b);

/** "Pick at least one tool…" when a server is set to chosen tools but none is picked. */
export function mcpProblem(value: McpAttach[]): string | undefined {
  return value.some((a) => a.tools != null && a.tools.length === 0) ? "Pick at least one tool, or choose All tools." : undefined;
}

type Choice = {
  id: string;
  name: string;
  description?: string;
  tools: { name: string; description?: string | null }[];
  unavailable?: boolean;
  /** The team's own server: "This team" or "This workspace". */
  own?: string;
};

const ownLabel = (x: McpServer) => (!x.team ? undefined : x.team.kind === "personal" ? "This workspace" : "This team");

type Props = {
  /** The servers to offer. */
  servers: McpServer[] | undefined;
  loading?: boolean;
  value: McpAttach[];
  onChange: (next: McpAttach[]) => void;
  /** The servers the current version uses, kept as choices when they aren't offered. */
  current?: McpAttached[] | null;
  clientMcp: boolean;
  onClientMcpChange: (on: boolean) => void;
  /** Shown when no server is offered. */
  emptyText: string;
  /** Under the section's description, e.g. why the team's own servers aren't offered. */
  note?: string;
  /** Said of a current server that isn't offered any more. */
  unavailableText?: string;
  /** Show the "pick at least one tool" errors (after a submit). */
  showErrors?: boolean;
};

const short = (t: string | null | undefined) => (t && t.length > 140 ? `${t.slice(0, 139)}…` : (t ?? undefined));

const unavailableDefault = "Not available any more: turned off or no longer published to the team.";

export function McpPicker({ servers, loading, value, onChange, current, clientMcp, onClientMcpChange, emptyText, note, unavailableText = unavailableDefault, showErrors }: Props) {
  const offered: Choice[] = (servers ?? []).map((x) => ({ id: x.id, name: x.name, description: x.description, tools: x.tools, own: ownLabel(x) }));
  for (const c of current ?? [])
    if (!offered.some((o) => o.id === c.serverId)) offered.push({ id: c.serverId, name: c.name, tools: (c.tools ?? []).map((name) => ({ name })), unavailable: true });
  const picked = (id: string) => value.find((a) => a.serverId === id);
  const update = (id: string, next: McpAttach | null) => onChange(next ? (picked(id) ? value.map((a) => (a.serverId === id ? next : a)) : [...value, next]) : value.filter((a) => a.serverId !== id));
  const toggleTool = (a: McpAttach, tool: string, on: boolean) => {
    const tools = (a.tools ?? []).filter((t) => t !== tool);
    update(a.serverId, { serverId: a.serverId, tools: on ? [...tools, tool] : tools });
  };
  return (
    <Fieldset legend="MCP servers" description="Tools its agents can call. Pick a server for all its tools, or only some of them.">
      <div className={m.servers}>
        {note ? <p className={s.note}>{note}</p> : null}
        {loading ? <p className={s.note}>Loading MCP servers…</p> : offered.length === 0 ? <p className={s.note}>{emptyText}</p> : null}
        {offered.map((o) => {
          const a = picked(o.id);
          const about = o.unavailable ? unavailableText : [o.description, plural(o.tools.length, "tool")].filter(Boolean).join(" · ");
          return (
            <div key={o.id} className={m.servers}>
              <Checkbox
                label={
                  o.own ? (
                    <span className={m.label}>
                      {o.name} <Badge tone="info">{o.own}</Badge>
                    </span>
                  ) : (
                    o.name
                  )
                }
                description={about}
                checked={Boolean(a)}
                onCheckedChange={(on) => update(o.id, on ? { serverId: o.id } : null)}
              />
              {a ? (
                <div className={m.choice}>
                  <RadioGroup
                    legend={`Tools of ${o.name}`}
                    orientation="horizontal"
                    value={a.tools == null ? "all" : "some"}
                    onValueChange={(v) => update(o.id, v === "all" ? { serverId: o.id } : { serverId: o.id, tools: [] })}
                    options={[
                      { value: "all", label: "All tools" },
                      { value: "some", label: "Only these tools", disabled: o.tools.length === 0 },
                    ]}
                  />
                  {a.tools != null ? (
                    <div className={m.tools}>
                      {o.tools.map((t) => (
                        <Checkbox key={t.name} label={t.name} description={short(t.description)} checked={a.tools!.includes(t.name)} onCheckedChange={(on) => toggleTool(a, t.name, on)} />
                      ))}
                      {showErrors && a.tools.length === 0 ? <p className={s.dangerText}>Pick at least one tool, or choose All tools.</p> : null}
                    </div>
                  ) : null}
                </div>
              ) : null}
            </div>
          );
        })}
        <Checkbox
          label="Let apps give agents their own MCP servers for a thread"
          description="An app can connect the servers its user turned on, such as one with the user's own token. Only that thread's conversation gets their tools."
          checked={clientMcp}
          onCheckedChange={onClientMcpChange}
        />
      </div>
    </Fieldset>
  );
}
