/*
 * Agents: a placeholder until the agents domain lands. Each agent will run in
 * its own isolated Pi Durable runtime, owned by a team or a service.
 */
import { Sparkles } from "lucide-react";
import { terms } from "../../lib/terms";
import { Card } from "@/components/ui/card/card";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import s from "../shared.module.css";

export function AgentsPage() {
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader title={terms.agents} description="Deploy and manage Pi Durable agents for your teams and services." />
      <Card>
        <EmptyState
          icon={<Sparkles />}
          title="Agents are coming soon."
          description="This is where your teams will deploy and manage Pi Durable agents, each in its own isolated runtime."
        />
      </Card>
    </Stack>
  );
}
