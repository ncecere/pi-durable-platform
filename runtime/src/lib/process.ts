import { createServer, type Server } from "node:http";
import { type AgentConfig, AgentOwnedElsewhereError, type RunningAgent, startAgent } from "./agent.ts";
import { assistantAgent, DEFAULT_INSTRUCTIONS } from "./assistant.ts";
import type { AgentDefinition } from "./define.ts";
import { createStandbyServer } from "./standby.ts";

/**
 * Runs one agent process with a definition, started one of two ways (docs/runtime-contract.md lists every variable):
 *
 * 1. As a given agent, from environment variables (local processes, Kubernetes pods):
 *      AGENT_ID, AGENT_NAME, AGENT_INSTRUCTIONS   identity and prompt from the agent's template
 *      AGENT_SCHEMA, AGENT_DATABASE_URL           the agent's own schema and database role
 *      RUNTIME_TOKEN                              bearer token the platform presents
 *      MODEL_PROVIDER, MODEL_*                    see models.ts
 * 2. In standby (Agent Sandbox warm pools): without AGENT_ID, with RUNTIME_ACTIVATION_TOKEN. The platform activates
 *    the runtime as one agent through POST /activate (standby.ts).
 *
 * PORT and HOST say where to listen (HOST defaults to loopback). The process exits 1 when storage fails, so its
 * supervisor starts a fresh one that resumes from storage; 2 when it is misconfigured; 3 when another process owns
 * the agent. SIGINT and SIGTERM stop it gracefully (work in flight stays pending and resumes on the next start).
 */
export async function runAgentProcess(definition: AgentDefinition = assistantAgent, env: NodeJS.ProcessEnv = process.env): Promise<Server> {
	const get = (name: string, fallback?: string): string => {
		const value = env[name] || fallback;
		if (value === undefined || value === "") {
			console.error(`[runtime] ${name} is required`);
			process.exit(2);
		}
		return value;
	};

	const port = Number(get("PORT", "4100"));
	const host = get("HOST", "127.0.0.1");
	let agent: RunningAgent | undefined;

	let server: Server;
	if (env.AGENT_ID) {
		const config: AgentConfig = {
			agentId: get("AGENT_ID"),
			name: get("AGENT_NAME", "Assistant"),
			instructions: get("AGENT_INSTRUCTIONS", DEFAULT_INSTRUCTIONS),
			schema: get("AGENT_SCHEMA"),
			databaseUrl: get("AGENT_DATABASE_URL"),
			token: get("RUNTIME_TOKEN"),
			modelEnv: env,
			createSchema: env.AGENT_CREATE_SCHEMA === "true",
		};
		try {
			agent = await startAgent(config, definition);
		} catch (error) {
			if (error instanceof AgentOwnedElsewhereError) {
				// The supervisor (the platform, or Kubernetes) sees exit code 3 and retries later.
				console.error(`[agent ${config.agentId}] another process owns this agent; exiting`);
				process.exit(3);
			}
			throw error;
		}
		server = createServer(agent.handler);
	} else if (env.RUNTIME_ACTIVATION_TOKEN) {
		server = createStandbyServer({
			activationToken: env.RUNTIME_ACTIVATION_TOKEN,
			start: (config) => startAgent(config, definition),
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
	return server;
}
