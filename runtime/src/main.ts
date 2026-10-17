import { createServer } from "node:http";
import { type AgentConfig, AgentOwnedElsewhereError, DEFAULT_INSTRUCTIONS, type RunningAgent, startAgent } from "./agent.ts";
import { createStandbyServer } from "./standby.ts";

/**
 * One agent process, started one of two ways:
 *
 * 1. As a given agent, from environment variables (local processes, Kubernetes pods):
 *      AGENT_ID, AGENT_NAME, AGENT_INSTRUCTIONS   identity and prompt from the agent's template
 *      AGENT_SCHEMA, AGENT_DATABASE_URL           the agent's own schema and database role
 *      RUNTIME_TOKEN                              bearer token the platform presents
 *      MODEL_PROVIDER, MODEL_*                    see models.ts
 * 2. In standby (Agent Sandbox warm pools): without AGENT_ID, with RUNTIME_ACTIVATION_TOKEN. The platform activates
 *    the runtime as one agent through POST /activate (standby.ts).
 *
 * PORT and HOST say where to listen (HOST defaults to loopback). The process exits non-zero when storage fails, so
 * its supervisor starts a fresh one that resumes from storage.
 */
const env = (name: string, fallback?: string): string => {
	const value = process.env[name] || fallback;
	if (value === undefined || value === "") {
		console.error(`[runtime] ${name} is required`);
		process.exit(2);
	}
	return value;
};

const port = Number(env("PORT", "4100"));
const host = env("HOST", "127.0.0.1");
let agent: RunningAgent | undefined;

let server;
if (process.env.AGENT_ID) {
	const config: AgentConfig = {
		agentId: env("AGENT_ID"),
		name: env("AGENT_NAME", "Assistant"),
		instructions: env("AGENT_INSTRUCTIONS", DEFAULT_INSTRUCTIONS),
		schema: env("AGENT_SCHEMA"),
		databaseUrl: env("AGENT_DATABASE_URL"),
		token: env("RUNTIME_TOKEN"),
		modelEnv: process.env,
		createSchema: process.env.AGENT_CREATE_SCHEMA === "true",
	};
	try {
		agent = await startAgent(config);
	} catch (error) {
		if (error instanceof AgentOwnedElsewhereError) {
			// The supervisor (the platform, or Kubernetes) sees exit code 3 and retries later.
			console.error(`[agent ${config.agentId}] another process owns this agent; exiting`);
			process.exit(3);
		}
		throw error;
	}
	server = createServer(agent.handler);
} else if (process.env.RUNTIME_ACTIVATION_TOKEN) {
	server = createStandbyServer({
		activationToken: process.env.RUNTIME_ACTIVATION_TOKEN,
		start: startAgent,
		onActivated: (running) => {
			agent = running;
		},
	});
	console.log("[runtime] standby: waiting to be activated as an agent");
} else {
	console.error("[runtime] AGENT_ID (an agent) or RUNTIME_ACTIVATION_TOKEN (standby) is required");
	process.exit(2);
}
server.listen(port, host, () => console.log(`[runtime] listening on http://${host}:${port}`));

let closing = false;
const close = async (code: number) => {
	if (closing) return;
	closing = true;
	server.close();
	await agent?.close();
	process.exit(code);
};
// Graceful hibernation: work in flight stays pending in storage and resumes on the next start.
for (const signal of ["SIGINT", "SIGTERM"] as const) process.once(signal, () => void close(0));
