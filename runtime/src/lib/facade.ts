/**
 * The part of pi-durable an agent's author uses, re-exported so custom agents import this library and never
 * `@earendil-works/pi-durable` directly. pi-durable is experimental and pinned (its API changed between 1.1 and 1.2);
 * when an upgrade changes one of these names or shapes, this module adapts it, or the library's contract version goes
 * up and docs/custom-agents.md says what changed. Anything not exported here is not part of the library's promise.
 */
export {
	configure,
	CompactionTask,
	defineDoc,
	defineEntry,
	defineExtension,
	defineTask,
	defineTool,
	GenerationTask,
	hook,
	section,
	ToolTask,
	wrapSection,
	wrapTool,
} from "@earendil-works/pi-durable";
export type {
	Agent,
	AgentChange,
	ConversationId,
	ConversationRecord,
	ConversationDocToken,
	DocDefinition,
	EntryRecord,
	Extension,
	HarnessSettings,
	HookApi,
	HookRegistration,
	JsonObject,
	ModelRef,
	PromptSection,
	SessionDocToken,
	ToolDiagnostic,
	ToolExecutionApi,
	ToolExecutionResult,
	ToolHookCall,
	ToolRegistration,
	Tx,
} from "@earendil-works/pi-durable";
export { Type } from "@earendil-works/pi-ai";
export type { Static, TSchema } from "@earendil-works/pi-ai";
export type { Context } from "@earendil-works/chord/context";
