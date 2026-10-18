/* An agent's Overview: its model requests, tokens and estimated cost over the last 30 days, and a link to the team's Usage for it. */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowRight } from "lucide-react";
import { teamUsageQuery } from "@/api/usage";
import { costEstimate, unpricedText } from "@/components/usage/labels";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { DescriptionList } from "@/components/ui/description-list/description-list";
import { SkeletonText } from "@/components/ui/skeleton/skeleton";
import { tokens, usd } from "@/lib/format";
import s from "../../shared.module.css";
import type { Agent } from "./labels";

export function AgentUsageCard({ agent }: { agent: Agent }) {
  // No period: the server's default, the last 30 days through today (UTC).
  const usage = useQuery(teamUsageQuery(agent.teamSlug, { groupBy: "day", agentId: agent.id }));
  const total = usage.data?.total;
  return (
    <Card
      title="Usage (last 30 days)"
      description={costEstimate}
      actions={
        <Button
          size="sm"
          variant="ghost"
          render={<Link to="/teams/$team/usage" params={{ team: agent.teamSlug }} search={{ agentId: agent.id }} aria-label={`View usage of ${agent.name}`} />}
        >
          View usage <ArrowRight aria-hidden />
        </Button>
      }
    >
      {usage.isLoading ? (
        <div role="status" aria-label="Loading usage…">
          <SkeletonText lines={3} />
        </div>
      ) : usage.error ? (
        <ErrorAlert error={usage.error} title="Couldn't load the usage" />
      ) : total ? (
        <>
          <DescriptionList
            dividers
            items={[
              { label: "Requests", value: total.requests.toLocaleString() },
              {
                label: "Tokens",
                value: `${tokens(total.inputTokens)} input · ${tokens(total.outputTokens)} output`,
              },
              { label: "Estimated cost", value: usd(total.costUsd) },
            ]}
          />
          {total.unpricedRequests > 0 && <p className={s.note}>{unpricedText(total.unpricedRequests)}</p>}
        </>
      ) : null}
    </Card>
  );
}
