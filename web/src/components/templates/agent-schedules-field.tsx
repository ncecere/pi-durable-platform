/* A template version's setting: its agents may set their own reminders and recurring work (the schedule tool). */
import { Checkbox } from "@/components/ui/checkbox/checkbox";
import { Fieldset } from "@/components/ui/field/field";

export const agentSchedulesLabel = "Let its agents schedule their own reminders and recurring work";

export function AgentSchedulesField({ checked, onCheckedChange }: { checked: boolean; onCheckedChange: (on: boolean) => void }) {
  return (
    <Fieldset legend="Schedules">
      <Checkbox
        label={agentSchedulesLabel}
        description="Gives agents a schedule tool. What they set is listed under the agent's Triggers, made by the agent; it runs within the agent's trigger limits, and its team's owners and admins can pause or delete it."
        checked={checked}
        onCheckedChange={onCheckedChange}
      />
    </Fieldset>
  );
}
