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
	DEFAULT_EXPOSED_DOCUMENT_BYTES,
	defineAgent,
	type ExposedDocument,
	MAX_EXPOSED_DOCUMENT_BYTES,
} from "./define.ts";
export {
	type AgentConfig,
	AgentOwnedElsewhereError,
	type AgentProcess,
	type RunningAgent,
	RuntimeRefusedError,
	type StartOptions,
	startAgent,
} from "./agent.ts";
export { type AgentChoice, runAgentProcess } from "./process.ts";
export { type Author, runAuthor } from "./author.ts";
export { assistantAgent, assistantExtension, DEFAULT_INSTRUCTIONS } from "./assistant.ts";
export { defineSubagent, SUBAGENT_REPORT, type SubagentDetails, type SubagentOptions, type SubagentReport } from "./subagent.ts";
export {
	type ApprovalDecision,
	approvalHook,
	type ApprovalOptions,
	type ApprovalRecord,
	type ApprovalRequester,
	type ApprovalStatus,
	type Decider,
} from "./approvals.ts";
export { CONTRACT_VERSION, PI_DURABLE_VERSION } from "./version.ts";
export * from "./facade.ts";

// ── Platform parts: not covered by the library's promise ─────────────────────────────
export { openPostgresAgentStorage, type OpenPostgresAgentStorageOptions, type PostgresAgentStorage, OWNERSHIP_TABLE } from "./storage/postgres.ts";
export { createRuntimeServer, type ExposedDocuments, revisionOf, type RuntimeOptions } from "./server.ts";
export { checkUpgrade, refusedHandler } from "./upgrade.ts";
export { createStandbyServer, parseConfig } from "./standby.ts";
export { type ThreadRecord, ThreadsDoc, ThreadStore } from "./threads.ts";
export { gatewayPost, type ThreadStatus, ThreadStatusReporter, unansweredError } from "./thread-status.ts";
export { AgentMcp, type McpServerConfig, modelVisible, toolName } from "./mcp.ts";
export { type AgentSchedule, SCHEDULE_EXTENSION, scheduleExtension, type ScheduleOptions } from "./schedule.ts";
export { type AgentModels, createAgentModels } from "./models.ts";
export { countingUsage, UsageReporter } from "./usage.ts";
export {
	boundedDetails,
	buildView,
	type ChatApproval,
	type ChatDocumentRef,
	type ChatMessage,
	type ChatTool,
	type ChatView,
	MAX_DETAILS_BYTES,
	USER_ENTRY,
} from "./view.ts";
export { Approvals, ApprovalsDoc, approvalId, parseApprovalPolicy, runStarter, toolMatches } from "./approvals.ts";
export { isSecretKey, isSecretValue, redactArguments } from "./redact.ts";
export { applyDelta, type DeltaEvent, DELTA_VERSION } from "./delta.ts";
