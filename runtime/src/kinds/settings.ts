// Shared parsing for the kinds' settings (AGENT_KIND_SETTINGS, docs/first-party-agents.md). Built only with the
// library's public exports, like the kinds themselves.
import { AgentDefinitionError } from "@pdp/runtime";

/** A tool name, or a pattern where `*` stands for any characters (as tool approvals take them). */
export const TOOL_PATTERN = /^[A-Za-z0-9_.*-]{1,200}$/;
/** The most tool names one list of a kind's settings may hold. */
export const MAX_TOOLS = 50;

/** The settings object (absent or empty: `{}`); its problems never echo its values, which a log may show. */
export function parseObject(raw: string | undefined, kind: string): Record<string, unknown> {
	if (raw === undefined || raw.trim() === "") return {};
	let value: unknown;
	try {
		value = JSON.parse(raw);
	} catch {
		throw new AgentDefinitionError(`AGENT_KIND_SETTINGS for ${kind} is not JSON`);
	}
	if (typeof value !== "object" || value === null || Array.isArray(value)) throw new AgentDefinitionError(`AGENT_KIND_SETTINGS for ${kind} is not a JSON object`);
	return value as Record<string, unknown>;
}

/** Refuses fields the kind doesn't have (a typo would silently do nothing). */
export function onlyFields(settings: Record<string, unknown>, kind: string, fields: readonly string[]): void {
	for (const key of Object.keys(settings)) {
		if (!fields.includes(key)) throw new AgentDefinitionError(`AGENT_KIND_SETTINGS for ${kind}: unknown field ${JSON.stringify(key.slice(0, 64))}`);
	}
}

export function toolList(settings: Record<string, unknown>, field: string, kind: string): string[] {
	const value = settings[field];
	if (value === undefined) return [];
	if (!Array.isArray(value) || value.length > MAX_TOOLS || !value.every((t) => typeof t === "string" && TOOL_PATTERN.test(t))) {
		throw new AgentDefinitionError(`${kind} settings: ${field} is at most ${MAX_TOOLS} tool names or patterns of 1-200 letters, digits, _ . - or *`);
	}
	return [...new Set(value as string[])];
}

export function flag(settings: Record<string, unknown>, field: string, kind: string, fallback: boolean): boolean {
	const value = settings[field];
	if (value === undefined) return fallback;
	if (typeof value !== "boolean") throw new AgentDefinitionError(`${kind} settings: ${field} is true or false`);
	return value;
}

export function wholeNumber(settings: Record<string, unknown>, field: string, kind: string, min: number, max: number, fallback: number): number {
	const value = settings[field];
	if (value === undefined) return fallback;
	if (typeof value !== "number" || !Number.isInteger(value) || value < min || value > max) {
		throw new AgentDefinitionError(`${kind} settings: ${field} is a whole number from ${min} to ${max}`);
	}
	return value;
}

/** Whether a tool name matches a name or a `*` pattern. */
export function matches(patterns: readonly string[], name: string): boolean {
	return patterns.some((pattern) => {
		if (!pattern.includes("*")) return pattern === name;
		const re = new RegExp(`^${pattern.split("*").map((part) => part.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join(".*")}$`);
		return re.test(name);
	});
}

/** A string argument, trimmed and bounded (the model's input is untrusted). */
export function clip(value: unknown, max: number): string {
	return typeof value === "string" ? value.trim().slice(0, max) : "";
}
