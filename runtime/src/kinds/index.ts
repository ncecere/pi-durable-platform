/**
 * @pdp/runtime/kinds: the platform's first-party agents (docs/first-party-agents.md), built with the library's public
 * exports only. The runtime image picks one by the template version's kind (AGENT_KIND, with AGENT_KIND_SETTINGS);
 * a team builds a variant by importing a kind's parts from here, or by copying its module.
 */
import { type AgentDefinition, AgentDefinitionError, assistantAgent } from "@pdp/runtime";
import { parseObject } from "./settings.ts";
import { parseResearcherSettings, researcherAgent } from "./researcher.ts";
import { parseTriageSettings, triageAgent } from "./triage.ts";

export * from "./researcher.ts";
export * from "./triage.ts";
export { matches as toolMatchesAny } from "./settings.ts";

/** The kinds a template version may have; `assistant` is the default (the platform's own agent). */
export const AGENT_KINDS = ["assistant", "triage", "researcher"] as const;
export type AgentKind = (typeof AGENT_KINDS)[number];

/**
 * The definition for a kind and its settings (JSON, as AGENT_KIND_SETTINGS carries them). An absent or empty kind is
 * the assistant. Throws AgentDefinitionError for an unknown kind or settings the kind refuses.
 */
export function agentForKind(kind: string | undefined, settings?: string): AgentDefinition {
	switch (kind === undefined || kind === "" ? "assistant" : kind) {
		case "assistant": {
			const fields = Object.keys(parseObject(settings, "assistant"));
			if (fields.length > 0) throw new AgentDefinitionError("the assistant kind takes no settings");
			return assistantAgent;
		}
		case "triage":
			return triageAgent(parseTriageSettings(settings));
		case "researcher":
			return researcherAgent(parseResearcherSettings(settings));
		default:
			throw new AgentDefinitionError(`unknown AGENT_KIND ${JSON.stringify(String(kind).slice(0, 64))} (one of ${AGENT_KINDS.join(", ")})`);
	}
}

/** The definition the platform's variables ask for (AGENT_KIND, AGENT_KIND_SETTINGS): what `src/main.ts` runs. */
export function agentFromEnv(env: Readonly<Record<string, string | undefined>>): AgentDefinition {
	return agentForKind(env.AGENT_KIND, env.AGENT_KIND_SETTINGS);
}
