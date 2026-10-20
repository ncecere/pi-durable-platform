// The runtime contract (docs/runtime-contract.md) as HTTP tests, against this agent: what the platform needs from any
// runtime it runs. The test kit's agent runs it on in-memory storage and a fake model (any answer will do).
import { registerRuntimeConformance, startTestAgent } from "@pdp/runtime/testing";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { taskBoardAgent } from "../src/agent.ts";

registerRuntimeConformance({ describe, it, expect, beforeAll, afterAll }, "the task board agent", async () => {
	const agent = await startTestAgent(taskBoardAgent);
	return { baseUrl: agent.baseUrl, token: agent.token, close: () => agent.close() };
});
