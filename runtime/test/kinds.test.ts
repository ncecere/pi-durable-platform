// The first-party kinds (docs/first-party-agents.md): picked by AGENT_KIND, their settings checked, and built with the
// library's public exports only, so a custom agent could build the same.
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { AgentDefinitionError, assistantAgent } from "@pdp/runtime";
import * as kinds from "@pdp/runtime/kinds";
import { describe, expect, it } from "vitest";

const KINDS_DIR = fileURLToPath(new URL("../src/kinds/", import.meta.url));

describe("agent kinds", () => {
	it("picks the composition by kind; absent or empty is the assistant", () => {
		expect(kinds.agentForKind(undefined)).toBe(assistantAgent);
		expect(kinds.agentForKind("")).toBe(assistantAgent);
		expect(kinds.agentForKind("assistant", "{}")).toBe(assistantAgent);
		expect(kinds.agentForKind("triage").name).toBe("triage");
		expect(kinds.agentForKind("researcher").name).toBe("researcher");
		expect(kinds.agentFromEnv({ AGENT_KIND: "researcher", AGENT_KIND_SETTINGS: '{"backgroundSubagents":false}' }).name).toBe("researcher");
		expect([...kinds.AGENT_KINDS]).toEqual(["assistant", "triage", "researcher"]);
	});

	it("refuses an unknown kind and settings the kind doesn't take", () => {
		expect(() => kinds.agentForKind("poet")).toThrow(AgentDefinitionError);
		expect(() => kinds.agentForKind("Triage")).toThrow(/unknown AGENT_KIND/);
		expect(() => kinds.agentForKind("assistant", '{"listTools":[]}')).toThrow(AgentDefinitionError);
		expect(() => kinds.agentForKind("triage", "not json")).toThrow(AgentDefinitionError);
		expect(() => kinds.agentForKind("triage", "[]")).toThrow(AgentDefinitionError);
		expect(() => kinds.agentForKind("triage", '{"writeTool":["x"]}')).toThrow(/unknown field/);
		expect(() => kinds.agentForKind("triage", '{"writeTools":["bad name!"]}')).toThrow(AgentDefinitionError);
		expect(() => kinds.agentForKind("triage", '{"approveWrites":"yes"}')).toThrow(AgentDefinitionError);
		expect(() => kinds.agentForKind("researcher", '{"maxPlanSteps":0}')).toThrow(AgentDefinitionError);
		expect(() => kinds.agentForKind("researcher", '{"maxPlanSteps":21}')).toThrow(AgentDefinitionError);
	});

	it("parses settings with their defaults", () => {
		expect(kinds.parseTriageSettings(undefined)).toEqual({ listTools: [], readTools: [], writeTools: [], approveWrites: true });
		expect(kinds.parseTriageSettings('{"writeTools":["mcp__gh__add_comment","mcp__gh__add_comment"],"approveWrites":false}')).toMatchObject({
			writeTools: ["mcp__gh__add_comment"],
			approveWrites: false,
		});
		expect(kinds.parseResearcherSettings("")).toEqual({ backgroundSubagents: true, maxPlanSteps: 8 });
		expect(kinds.toolMatchesAny(["mcp__gh__*"], "mcp__gh__add_comment")).toBe(true);
		expect(kinds.toolMatchesAny(["mcp__gh__add_*"], "mcp__gh__list_issues")).toBe(false);
		expect(kinds.toolMatchesAny(["mcp__g.h__x"], "mcp__gxh__x")).toBe(false);
	});

	it("are built with the library's public exports only", () => {
		const files = readdirSync(KINDS_DIR).filter((f) => f.endsWith(".ts"));
		expect(files.length).toBeGreaterThanOrEqual(4);
		for (const file of files) {
			const source = readFileSync(KINDS_DIR + file, "utf8");
			const specifiers = [...source.matchAll(/(?:^|\n)\s*(?:import|export)[^;]*?from\s+"([^"]+)"/g), ...source.matchAll(/import\(\s*"([^"]+)"\s*\)/g)].map((m) => m[1]!);
			expect(specifiers.length, file).toBeGreaterThan(0);
			for (const specifier of specifiers) {
				// The library's main entry, or another module of the kinds: never pi-durable, the library's internals or node.
				const allowed = specifier === "@pdp/runtime" || /^\.\/[a-z-]+\.ts$/.test(specifier);
				expect(allowed, `${file} imports ${specifier}`).toBe(true);
			}
		}
	});
});
