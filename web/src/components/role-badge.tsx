/*
 * A team role as a badge that explains itself (tasks H9): pressing or hovering it opens what the role can and can't do
 * (roleAbilities, from DESIGN.md §3.5). On the Members list, the team Overview's "Your role" and Team settings.
 */
import { Badge } from "@/components/ui/badge/badge";
import { Popover } from "@/components/ui/popover/popover";
import { roleAbilities, roleLabels, type TeamRole } from "./roles";
import r from "./role-badge.module.css";

export function RoleBadge({ role, prefix = "", tone }: { role: TeamRole; prefix?: string; tone?: "info" | "neutral" }) {
  const label = roleLabels[role];
  const { can, cannot } = roleAbilities[role];
  return (
    <Popover
      openOnHover
      side="bottom"
      align="start"
      title={`What ${/^[AEIOU]/.test(label) ? "an" : "a"} ${label.toLowerCase()} can do`}
      trigger={
        <button type="button" className={r.trigger} aria-label={`${prefix}${label}: what this role can do`}>
          <Badge tone={tone ?? (role === "owner" ? "info" : "neutral")}>
            {prefix}
            {label}
          </Badge>
        </button>
      }
    >
      <div className={r.body}>
        <p className={r.heading}>Can</p>
        <ul className={r.list}>
          {can.map((c) => (
            <li key={c}>{c}</li>
          ))}
        </ul>
        <p className={r.heading}>Can't</p>
        <ul className={r.list}>
          {cannot.map((c) => (
            <li key={c}>{c}</li>
          ))}
        </ul>
      </div>
    </Popover>
  );
}
