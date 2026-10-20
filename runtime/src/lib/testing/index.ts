/**
 * @pdp/runtime/testing: the test kit for agents built with the library (docs/custom-agents.md#testing). Stable, like
 * the library's main entry. Runner-agnostic: the suites take `describe`, `it`, `expect` (and `beforeAll`/`afterAll`)
 * from Vitest or Jest; nothing here imports a test runner.
 */
export { afterGate, answer, type FakeModel, type FakeStep, fakeModel, type Gate, gate, toolCall } from "./model.ts";
export { memoryStorage, StorageCrashed, TestStorage } from "./storage.ts";
export { type ExposedDocumentRead, startTestAgent, type TestAgent, type TestAgentOptions } from "./agent.ts";
export { expectReplayed, ReplayAssertionError, type ReplaySummary } from "./replay.ts";
export {
	type ConformanceRunner,
	type ExpectLike,
	registerRuntimeConformance,
	registerStandbyConformance,
	type RuntimeUnderTest,
	type StandbyUnderTest,
} from "./conformance.ts";
/** pi-durable's storage conformance suite, for an alternative storage (the platform's PostgreSQL storage passes it). */
export { registerStorageConformance, type StorageConformanceRunner } from "@earendil-works/pi-durable/testing";
