/* Admin › Models: labels, formatting and helpers shared by the providers and models pages. */
import type { Model, ModelProvider, ProviderKind, ProviderType } from "@/api/models";

export const kindLabels: Record<ProviderKind, string> = {
  openai: "OpenAI",
  anthropic: "Anthropic",
  "openai-compatible": "OpenAI-compatible",
  "openai-responses": "OpenAI Responses-compatible",
  "anthropic-compatible": "Anthropic-compatible",
};

/** A provider type's name, from the kinds endpoint when loaded. */
export const kindLabel = (kind: string, kinds?: ProviderType[]) => kinds?.find((k) => k.id === kind)?.name ?? kindLabels[kind as ProviderKind] ?? kind;

export type AccessMode = ModelProvider["accessMode"];

export const accessLabels: Record<AccessMode, string> = { proxy: "Through the platform", direct: "Direct" };

export const accessOptions: { value: AccessMode; label: string; description: string }[] = [
  {
    value: "proxy",
    label: "Through the platform (proxy) — recommended",
    description: "Agents never see the key, and the platform counts their usage.",
  },
  {
    value: "direct",
    label: "Direct",
    description: "Agents get the URL and the key and call the provider themselves. Use it only with scoped keys.",
  },
];

export const whatIsAProvider =
  "A provider is an API the platform's agents reach models through: OpenAI, Anthropic, or a compatible server such as LiteLLM, vLLM or Ollama. Its key is stored encrypted and never shown again.";

/** "$3.00" per million tokens, or "—". */
export const price = (p: number | null | undefined) => (p == null ? "—" : `$${p.toFixed(2)}`);

/** Who may use the model: "All teams", "Platform Tools", "3 teams", with "Personal workspaces" when they may. */
export function publishedTo(m: Pick<Model, "visibility" | "teams" | "personal">): string {
  const teams = m.teams ?? [];
  const shared = m.visibility === "all" ? "All teams" : teams.length === 0 ? "" : teams.length === 1 ? teams[0]!.name : `${teams.length} teams`;
  if (!m.personal) return shared || "No teams";
  return shared ? `${shared} and personal workspaces` : "Personal workspaces";
}

/** The "Published to" checkbox for personal workspaces (models and MCP servers). */
export const personalLabel = "Also available in personal workspaces";
export const personalHint = "People's own workspaces can use it too. Shared teams follow the choice above.";

/** Extra headers as edited: rows of name and value. */
export type HeaderRow = { name: string; value: string };

/** The rows with a name, as the API's map. */
export function headersOf(rows: HeaderRow[]): Record<string, string> {
  return Object.fromEntries(rows.filter((r) => r.name.trim()).map((r) => [r.name.trim(), r.value]));
}

/** "Enter a name." for a row with a value but no name. */
export const headersProblem = (rows: HeaderRow[]) => (rows.some((r) => !r.name.trim() && r.value) ? "Give every header a name." : undefined);

export const urlProblem = (url: string, required: boolean) => {
  const u = url.trim();
  if (!u) return required ? "Enter the server's address." : undefined;
  return /^https?:\/\/\S+$/.test(u) ? undefined : "Enter an address starting with https:// or http://.";
};
