/*
 * Who sees a new conversation, chosen with its first message on a shared
 * team's agent: Shared (everyone who can use the agent) or Private (only the
 * person). It never changes afterwards. Personal workspaces don't offer it.
 */
import { Lock, UsersRound } from "lucide-react";
import { useId } from "react";
import type { ThreadVisibility } from "../../../api/agents";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group/toggle-group";
import s from "./agents.module.css";

export const visibilityHints: Record<ThreadVisibility, string> = {
  shared: "Everyone who can use this agent sees it",
  private: "Only you",
};

type Props = { value: ThreadVisibility; onChange: (v: ThreadVisibility) => void; disabled?: boolean };

export function VisibilityPicker({ value, onChange, disabled }: Props) {
  const hintId = useId();
  return (
    <div className={s.visibility}>
      <ToggleGroup
        aria-label="Who can see this conversation"
        aria-describedby={hintId}
        size="sm"
        variant="outline"
        joined
        disabled={disabled}
        value={[value]}
        onValueChange={(next: string[]) => {
          // Pressing the current choice again keeps it.
          const picked = next[next.length - 1];
          if (picked === "shared" || picked === "private") onChange(picked);
        }}
      >
        <ToggleGroupItem value="shared">
          <UsersRound aria-hidden /> Shared
        </ToggleGroupItem>
        <ToggleGroupItem value="private">
          <Lock aria-hidden /> Private
        </ToggleGroupItem>
      </ToggleGroup>
      <span id={hintId} className={s.hint}>
        {visibilityHints[value]}
      </span>
    </div>
  );
}
