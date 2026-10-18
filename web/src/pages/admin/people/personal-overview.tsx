/*
 * Admin team › Overview of a personal workspace: its owner, its agents
 * against its limits, and links to what platform staff audit there (its
 * agents, usage and audit log; never its conversations).
 */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChartColumn, FileClock, Sparkles, Zap } from "lucide-react";
import { limitLines, workspaceLimitsQuery } from "@/api/personal";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { StatCard } from "@/components/ui/stat-card/stat-card";
import { terms } from "@/lib/terms";
import s from "../../shared.module.css";

/** The workspace's person, from the team summary. */
export function usePersonalOwner(owner: { id: string; email: string; displayName?: string | null } | null | undefined) {
  return owner ? { name: owner.displayName || owner.email, email: owner.email, id: owner.id } : undefined;
}

/** Agents and agents running, against the limits. */
export function PersonalStats({ slug }: { slug: string }) {
  const limits = useQuery(workspaceLimitsQuery(slug));
  const lines = limits.data ? limitLines(limits.data) : undefined;
  return (
    <>
      <StatCard
        label={terms.agents}
        value={limits.data ? limits.data.agentCount.toLocaleString() : "—"}
        hint={lines?.agents}
        icon={<Sparkles />}
        render={<Link to="/teams/$team/agents" params={{ team: slug }} />}
      />
      <StatCard label="Running" value={limits.data ? limits.data.runningCount.toLocaleString() : "—"} hint={lines?.running} icon={<Zap />} />
    </>
  );
}

/** Where platform staff audit the workspace. */
export function AuditLinks({ slug }: { slug: string }) {
  return (
    <Card title="Audit this workspace" description="Platform staff see its agents' settings and status, its usage and its audit log, not its conversations.">
      <div className={s.badges}>
        <Button size="sm" variant="secondary" render={<Link to="/teams/$team/agents" params={{ team: slug }} />}>
          <Sparkles aria-hidden /> {terms.agents}
        </Button>
        <Button size="sm" variant="secondary" render={<Link to="/teams/$team/usage" params={{ team: slug }} />}>
          <ChartColumn aria-hidden /> Usage
        </Button>
        <Button size="sm" variant="secondary" render={<Link to="/teams/$team/settings" params={{ team: slug }} search={{ tab: "audit" }} />}>
          <FileClock aria-hidden /> {terms.auditLog}
        </Button>
      </div>
    </Card>
  );
}
