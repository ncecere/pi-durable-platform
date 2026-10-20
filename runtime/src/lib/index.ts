/**
 * @pdp/runtime: the Pi Durable Platform agent runtime as a library (docs/custom-agents.md).
 *
 * Stable (the library's promise, versioned with the runtime contract): defining and running an agent, the subagent
 * helper, the pi-durable subset in facade.ts, the version constants, and the test kit (`@pdp/runtime/testing`).
 * The parts below "Platform parts" are exported for the platform's own runtime and for tests; they may change in any
 * release.
 */

// ── Stable: defining and running an agent ─────────────────────────────────────────────
export {
	type AgentContext,
	type AgentDefinition,
	AgentDefinitionError,
	type AgentDocument,
	type AgentSettings,
	defineAgent,
} from "./define.ts";
export { type AgentConfig, AgentOwnedElsewhereError, type RunningAgent, type StartOptions, startAgent } from "./agent.ts";
export { runAgentProcess } from "./process.ts";
export { assistantAgent, assistantExtension, DEFAULT_INSTRUCTIONS } from "./assistant.ts";
export { defineSubagent, type SubagentDetails, type SubagentOptions } from "./subagent.ts";
export { CONTRACT_VERSION, PI_DURABLE_VERSION } from "./version.ts";
export * from "./facade.ts";

// ── Platform parts: not covered by the library's promise ─────────────────────────────
export { openPostgresAgentStorage, type OpenPostgresAgentStorageOptions, type PostgresAgentStorage, OWNERSHIP_TABLE } from "./storage/postgres.ts";
export { createRuntimeServer, type RuntimeOptions } from "./server.ts";
export { createStandbyServer, parseConfig } from "./standby.ts";
export { type ThreadRecord, ThreadsDoc, ThreadStore } from "./threads.ts";
export { gatewayPost, type ThreadStatus, ThreadStatusReporter, unansweredError } from "./thread-status.ts";
export { AgentMcp, type McpServerConfig, modelVisible, toolName } from "./mcp.ts";
export { type AgentModels, createAgentModels } from "./models.ts";
export { countingUsage, UsageReporter } from "./usage.ts";
export { buildView, type ChatMessage, type ChatView, USER_ENTRY } from "./view.ts";
