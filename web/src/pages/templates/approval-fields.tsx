/*
 * A template version's tool approvals (docs/approvals.md): which tools ask a
 * person before they run (names, or patterns with *), the lowest team role
 * that may decide calls in shared threads besides the person the run is for
 * (owners and admins always may), and how long a request waits before it is
 * denied.
 */
import type { Schemas } from "../../api/client";
import { Field } from "@/components/ui/field/field";
import { NumberInput } from "@/components/ui/number-input/number-input";
import { Select } from "@/components/ui/select/select";
import { TagInput } from "@/components/ui/tag-input/tag-input";

export type ApprovalPolicy = Schemas["ToolApprovalPolicy"];

export const defaultApprovals: ApprovalPolicy = { tools: [], role: "admin", timeoutSeconds: 86400 };

const roleItems: { value: ApprovalPolicy["role"]; label: string }[] = [
  { value: "admin", label: "Owners and admins" },
  { value: "editor", label: "Editors and above" },
  { value: "member", label: "Every member" },
];

const TOOL_PATTERN = /^[A-Za-z0-9_.*-]{1,200}$/;

/** A problem with the fields, or undefined. */
export function approvalsProblem(p: ApprovalPolicy): string | undefined {
  if (p.tools.some((t) => !TOOL_PATTERN.test(t))) return "Tool names are letters, digits, _ . - and * (any characters).";
  if (p.tools.length > 100) return "At most 100 tools.";
  if (!(p.timeoutSeconds >= 60 && p.timeoutSeconds <= 2592000)) return "Wait between 1 minute and 30 days.";
  return undefined;
}

export const sameApprovals = (a: ApprovalPolicy, b: ApprovalPolicy) =>
  a.role === b.role && a.timeoutSeconds === b.timeoutSeconds && a.tools.join("\n") === b.tools.join("\n");

type Props = { value: ApprovalPolicy; onChange: (p: ApprovalPolicy) => void; showErrors?: boolean };

export function ApprovalFields({ value, onChange, showErrors }: Props) {
  const problem = showErrors ? approvalsProblem(value) : undefined;
  const hours = String(Math.round((value.timeoutSeconds / 3600) * 100) / 100);
  return (
    <>
      <Field
        label="Ask a person before these tools run"
        labelHint="Optional"
        description="Tool names, or patterns: mcp__github__* is every tool of the github MCP server. The call waits until someone allowed approves it; a denied call is refused and the agent is told why. A private conversation's calls are its creator's to decide, an unattended run's the owners' and admins'."
        error={problem}
      >
        <TagInput value={value.tools} maxTags={100} placeholder="get_time, mcp__crm__delete_*" onValueChange={(tools) => onChange({ ...value, tools })} />
      </Field>
      {value.tools.length > 0 ? (
        <>
          <Select<ApprovalPolicy["role"]>
            label="Who else may decide in shared conversations (besides the person who asked)"
            items={roleItems}
            value={value.role}
            onValueChange={(role) => role && onChange({ ...value, role })}
          />
          <Field label="Deny when nobody decides within" description="1 minute to 30 days.">
            <NumberInput
              value={hours}
              maximumFractionDigits={2}
              unit="hours"
              onValueChange={(v) => onChange({ ...value, timeoutSeconds: Math.round(Number(v || "0") * 3600) })}
            />
          </Field>
        </>
      ) : null}
    </>
  );
}
