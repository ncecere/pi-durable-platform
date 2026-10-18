/* Admin › MCP servers: labels and helpers shared by the list, the add dialog and a server's page. */
import type { McpServer } from "@/api/mcp";
import { plural } from "@/lib/format";

export type McpAccessMode = McpServer["accessMode"];
export type McpVisibility = McpServer["visibility"];

export const mcpAccessOptions: { value: McpAccessMode; label: string; description: string }[] = [
  {
    value: "proxy",
    label: "Through the platform (proxy) — recommended",
    description: "Agents never see the headers. The platform adds them, allows only the tools a template picked, and logs each call.",
  },
  {
    value: "direct",
    label: "Direct",
    description: "Agents get the URL and the headers and call the server themselves. Use it only with scoped tokens.",
  },
];

export const whatIsAnMcpServer =
  "An MCP server gives agents tools: a remote server the platform reads the tool list of, published to every team or to chosen teams. Templates pick which servers and tools their agents get.";

/** Who may attach the server: "All teams", "Platform Tools", "3 teams". */
export function mcpPublishedTo(m: Pick<McpServer, "visibility" | "teams">): string {
  if (m.visibility === "all") return "All teams";
  const teams = m.teams ?? [];
  if (teams.length === 0) return "No teams";
  return teams.length === 1 ? teams[0]!.name : `${teams.length} teams`;
}

/** "3 tools", or "—" when they couldn't be read and none are known. */
export const toolCount = (m: McpServer) => (m.toolsError && m.tools.length === 0 ? "—" : plural(m.tools.length, "tool"));

/** The hint under the slug: what its tools are called. */
export const slugHint = (slug: string) => `Tools appear as mcp__${slug || "<slug>"}__… It can't be changed later.`;

export const teamsProblem = (visibility: McpVisibility, teams: string[]) =>
  visibility === "teams" && teams.length === 0 ? "Choose at least one team, or publish it to all teams." : undefined;
