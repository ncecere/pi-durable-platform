/*
 * The Overview's Tools row: the MCP servers the agent's template version gives it, whether apps may add their own, and
 * whether it may schedule its own reminders.
 */
import { useQuery } from "@tanstack/react-query";
import { mcpSummary } from "../../../api/mcp";
import { templateVersionsQuery } from "../../../api/templates";
import { Badge } from "@/components/ui/badge/badge";
import s from "../../shared.module.css";
import type { Agent } from "./labels";

export function AgentToolsValue({ agent }: { agent: Agent }) {
  const versions = useQuery(templateVersionsQuery(agent.templateId));
  if (versions.isLoading) return <span className={s.secondary}>Loading…</span>;
  const version = versions.data?.find((v) => v.version === agent.templateVersion);
  if (!version) return <span className={s.secondary}>Couldn&apos;t load its template version.</span>;
  const servers = version.mcp ?? [];
  return (
    <span>
      {servers.length === 0 ? (
        "No MCP servers"
      ) : (
        <span className={s.badges}>
          {servers.map((a) => (
            <Badge key={a.serverId} size="sm" title="An MCP server the agent can call">
              {mcpSummary(a)}
            </Badge>
          ))}
        </span>
      )}
      {version.clientMcp ? <span className={s.secondary}>Apps may add MCP servers for a thread.</span> : null}
      {version.agentSchedules ? <span className={s.secondary}>It may schedule its own reminders and recurring work (listed under Triggers).</span> : null}
    </span>
  );
}
