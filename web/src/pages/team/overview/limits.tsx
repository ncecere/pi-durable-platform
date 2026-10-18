/* A personal workspace's limits on its overview: agents, agents running at once, and the monthly budget (recorded). */
import { useQuery } from "@tanstack/react-query";
import { limitLines, workspaceLimitsQuery } from "../../../api/personal";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Card } from "@/components/ui/card/card";
import { Meter } from "@/components/ui/meter/meter";
import { SkeletonText } from "@/components/ui/skeleton/skeleton";
import s from "../../shared.module.css";
import { useTeam } from "../common";
import o from "./overview.module.css";

export function LimitsCard() {
  const { slug } = useTeam();
  const limits = useQuery(workspaceLimitsQuery(slug));
  const l = limits.data;
  const lines = l ? limitLines(l) : undefined;
  return (
    <Card title="Limits" description="Set by platform admins for personal workspaces.">
      {limits.isLoading ? (
        <SkeletonText lines={2} />
      ) : !l || !lines ? (
        <ErrorAlert error={limits.error} title="Couldn't load the limits" onRetry={() => void limits.refetch()} />
      ) : (
        <div className={o.limits}>
          <Meter label="Agents" value={l.agentCount} max={l.maxAgents ?? null} valueText={lines.agents} />
          <Meter
            label="Running at once"
            value={l.runningCount}
            max={l.maxRunning ?? null}
            valueText={lines.running}
            description="Idle agents hibernate, which frees a place."
          />
          {lines.budget && <p className={s.note}>{lines.budget}</p>}
        </div>
      )}
    </Card>
  );
}
