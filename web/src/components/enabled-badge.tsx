import { switchLabel } from "../lib/terms";
import { StatusBadge } from "@/components/ui/badge/badge";

/** A switchable object's state (a service account, a key): Enabled or Disabled. */
export function EnabledBadge({ enabled }: { enabled: boolean }) {
  return <StatusBadge tone={enabled ? "success" : "neutral"}>{switchLabel(enabled)}</StatusBadge>;
}
