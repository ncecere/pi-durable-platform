import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { createRegistry, Harness } from "@earendil-works/pi-durable";
import { createAgentModels } from "./models.ts";
import { createRuntimeServer } from "./server.ts";
import { AgentOwnedElsewhereError, openPostgresAgentStorage } from "./storage/postgres.ts";
import { ThreadStore } from "./threads.ts";
import { assistantExtension } from "./tools.ts";

/**
 * One agent process. The platform starts it with:
 *
 *   AGENT_ID, AGENT_NAME, AGENT_INSTRUCTIONS   identity and prompt from the agent's template
 *   AGENT_SCHEMA, AGENT_DATABASE_URL           the agent's own schema and database role
 *   RUNTIME_TOKEN                              bearer token the platform presents
 *   PORT, HOST                                 where to listen (HOST defaults to loopback)
 *   MODEL_PROVIDER, MODEL_*                    see models.ts
 *
 * It exits non-zero when storage fails, so its supervisor (the platform, or Kubernetes) restarts it from storage.
 */
const ctx = BACKGROUND_CONTEXT;
const env = (name: string, fallback?: string): string => {
	const value = process.env[name] || fallback;
	if (value === undefined || value === "") {
		console.error(`[runtime] ${name} is required`);
		process.exit(2);
	}
	return value;
};

const agentId = env("AGENT_ID");
const name = env("AGENT_NAME", "Assistant");
const instructions = env(
	"AGENT_INSTRUCTIONS",
	"You are a helpful, concise assistant. Use get_time for anything about the current date or time.",
);
const schema = env("AGENT_SCHEMA");
const token = env("RUNTIME_TOKEN");
const port = Number(env("PORT", "4100"));
const host = env("HOST", "127.0.0.1");
const log = (...args: unknown[]) => console.log(`[agent ${agentId}]`, ...args);

const { models, model, source } = createAgentModels();
log(`model ${model.provider}/${model.modelId} via ${source}`);

let storage;
try {
	storage = await openPostgresAgentStorage({
		connection: { connectionString: env("AGENT_DATABASE_URL") },
		schema,
		createSchema: process.env.AGENT_CREATE_SCHEMA === "true",
		onConnectionLost: (error) => {
			console.error(`[agent ${agentId}] database connection lost; exiting so a new owner can take over`, error.message);
			process.exit(1);
		},
	});
} catch (error) {
	if (error instanceof AgentOwnedElsewhereError) {
		console.error(`[agent ${agentId}] another process owns this agent; exiting`);
		process.exit(3);
	}
	throw error;
}

const registry = createRegistry();
const extension = assistantExtension(instructions);
registry.install(extension);
const harness = await Harness.open(
	storage.storage,
	{ models, registry, onReport: (error) => console.error(`[agent ${agentId}]`, error) },
	ctx,
);
// Continue runs, tool calls and queued input left by a previous process.
harness.resume();

const threads = new ThreadStore(harness, () => ({ model, extensions: [extension] }));
const { server } = createRuntimeServer({ harness, threads, token, info: { agentId, name, model: `${model.provider}/${model.modelId}` } });
server.listen(port, host, () => log(`listening on http://${host}:${port}`));

let closing = false;
const close = async (code: number) => {
	if (closing) return;
	closing = true;
	server.close();
	await harness.close(ctx).catch(() => {});
	process.exit(code);
};
harness.closed.then((end) => {
	if (end.reason === "failed" && !closing) {
		console.error(`[agent ${agentId}] storage failed; exiting so the agent restarts from storage`, end.error);
		process.exit(1);
	}
});
// Graceful hibernation: work in flight stays pending in storage and resumes on the next start.
for (const signal of ["SIGINT", "SIGTERM"] as const) process.once(signal, () => void close(0));
