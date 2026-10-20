/*
 * A template version's agent kind (docs/first-party-agents.md) and its
 * settings: the assistant (the default), triage (the MCP tools that list,
 * read and write, and whether writes ask a person first) or the deep
 * researcher (background subagents, the plan's size).
 */
import type { Schemas } from "../../api/client";
import { Field } from "@/components/ui/field/field";
import { NumberInput } from "@/components/ui/number-input/number-input";
import { RadioGroup } from "@/components/ui/radio-group/radio-group";
import { Switch } from "@/components/ui/switch/switch";
import { TagInput } from "@/components/ui/tag-input/tag-input";

export type AgentKind = Schemas["AgentKind"];
export type KindSettings = Schemas["AgentKindSettings"];

export const kindLabels: Record<AgentKind, string> = {
  assistant: "Assistant",
  triage: "Triage",
  researcher: "Deep researcher",
};

const kindOptions: { value: AgentKind; label: string; description: string }[] = [
  { value: "assistant", label: kindLabels.assistant, description: "Answers with its instructions, model and MCP tools." },
  {
    value: "triage",
    label: kindLabels.triage,
    description: "Reads new issues or discussions through MCP tools, keeps a queue of what it saw and decided, drafts replies and asks before it writes.",
  },
  {
    value: "researcher",
    label: kindLabels.researcher,
    description: "Plans, runs subagents in parallel, keeps its sources and answers with a report.",
  },
];

/** A kind's settings with their defaults (what the platform stores when none are given). */
export function defaultKindSettings(kind: AgentKind): KindSettings {
  if (kind === "triage") return { listTools: [], readTools: [], writeTools: [], approveWrites: true };
  if (kind === "researcher") return { backgroundSubagents: true, maxPlanSteps: 8 };
  return {};
}

/** Settings as a version returns them, completed with the kind's defaults. */
export function kindSettingsOf(kind: AgentKind, settings: KindSettings | undefined): KindSettings {
  const defaults = defaultKindSettings(kind);
  const out: KindSettings = { ...defaults };
  for (const key of Object.keys(defaults) as (keyof KindSettings)[]) {
    if (settings?.[key] !== undefined) (out as Record<string, unknown>)[key] = settings[key];
  }
  return out;
}

const TOOL_PATTERN = /^[A-Za-z0-9_.*-]{1,200}$/;

/** A problem with the settings, or undefined. */
export function kindProblem(kind: AgentKind, s: KindSettings): string | undefined {
  if (kind === "triage") {
    const lists = [s.listTools ?? [], s.readTools ?? [], s.writeTools ?? []];
    if (lists.some((l) => l.some((t) => !TOOL_PATTERN.test(t)))) return "Tool names are letters, digits, _ . - and * (any characters).";
    if (lists.some((l) => l.length > 50)) return "At most 50 tools in each list.";
  }
  if (kind === "researcher") {
    const n = s.maxPlanSteps ?? 8;
    if (!Number.isInteger(n) || n < 1 || n > 20) return "A plan has 1 to 20 steps.";
  }
  return undefined;
}

export const sameKindSettings = (a: KindSettings, b: KindSettings) => JSON.stringify(sorted(a)) === JSON.stringify(sorted(b));
const sorted = (s: KindSettings) => Object.fromEntries(Object.entries(s).sort(([x], [y]) => x.localeCompare(y)));

type Props = {
  kind: AgentKind;
  settings: KindSettings;
  onKindChange: (kind: AgentKind) => void;
  onSettingsChange: (settings: KindSettings) => void;
  showErrors?: boolean;
};

const keepCase = (t: string) => t.trim();

export function KindFields({ kind, settings, onKindChange, onSettingsChange, showErrors }: Props) {
  const problem = showErrors ? kindProblem(kind, settings) : undefined;
  const tools = (field: "listTools" | "readTools" | "writeTools", label: string, description: string, placeholder: string) => (
    <Field label={label} labelHint="Optional" description={description}>
      <TagInput
        value={settings[field] ?? []}
        maxTags={50}
        maxTagLength={200}
        normalize={keepCase}
        noun={{ one: "tool", other: "tools" }}
        placeholder={placeholder}
        onValueChange={(v) => onSettingsChange({ ...settings, [field]: v })}
      />
    </Field>
  );
  return (
    <>
      <RadioGroup<AgentKind>
        legend="Kind"
        description="What its agents do. Triage and the deep researcher are built in; their settings follow."
        options={kindOptions}
        value={kind}
        onValueChange={onKindChange}
        error={problem}
      />
      {kind === "triage" ? (
        <>
          {tools("listTools", "Tools that list new items", "MCP tools by name (mcp__<server>__<tool>), or patterns with *.", "mcp__github__list_issues")}
          {tools("readTools", "Tools that read one item", "Its body, comments and labels.", "mcp__github__get_issue")}
          {tools("writeTools", "Tools that write", "Comment, label, close. Other MCP tools are refused.", "mcp__github__add_comment")}
          <Switch
            label="Ask a person before every write"
            description="Added to the version's approvals: a comment or a label waits until someone allowed approves it."
            checked={settings.approveWrites ?? true}
            onCheckedChange={(v) => onSettingsChange({ ...settings, approveWrites: v })}
          />
        </>
      ) : null}
      {kind === "researcher" ? (
        <>
          <Switch
            label="Long questions run in the background"
            description="Background subagents report back when they finish. A run a trigger started always waits for them, so its run time and budget cover the research."
            checked={settings.backgroundSubagents ?? true}
            onCheckedChange={(v) => onSettingsChange({ ...settings, backgroundSubagents: v })}
          />
          <Field label="Most steps in a plan" description="1 to 20.">
            <NumberInput
              value={String(settings.maxPlanSteps ?? 8)}
              maximumFractionDigits={0}
              onValueChange={(v) => onSettingsChange({ ...settings, maxPlanSteps: Math.round(Number(v || "0")) })}
            />
          </Field>
        </>
      ) : null}
    </>
  );
}
