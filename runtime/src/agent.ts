import type { RequestListener } from "node:http";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { createRegistry, Harness } from "@earendil-works/pi-durable";
import { AgentMcp } from "./mcp.ts";
import { createAgentModels } from "./models.ts";
import { createRuntimeServer } from "./server.ts";
import { AgentOwnedElsewhereError, openPostgresAgentStorage } from "./storage/postgres.ts";
import { gatewayPost, ThreadStatusReporter } from "./thread-status.ts";
import { ThreadStore } from "./threads.ts";
import { assistantExtension } from "./tools.ts";

/** Everything one agent needs: from environment variables, or from the platform's activation request. */
export type AgentConfig = {
	agentId: string;
	name: string;
	instructions: string;
	/** The agent's own schema and database role. */
	schema: string;
	databaseUrl: string;
	/** Bearer token the platform presents on every request. */
	token: string;
	/** Model settings (MODEL_PROVIDER, MODEL_*), see models.ts. */
	modelEnv: Record<string, string | undefined>;
	createSchema?: boolean;
};

export const DEFAULT_INSTRUCTIONS = "You are a helpful, concise assistant. Use get_time for anything about the current date or time.";

export type RunningAgent = {
	handler: RequestListener;
	/** Graceful stop: work in flight stays pending in storage and resumes on the next start. */
	close(): Promise<void>;
};

const ctx = BACKGROUND_CONTEXT;

export { AgentOwnedElsewhereError };

/**
 * Open the agent's storage (taking its ownership lock), resume unfinished work and return its HTTP handler. Throws
 * AgentOwnedElsewhereError when another process owns the agent. Once running, it exits the process when storage fails,
 * so the supervisor starts a fresh one that resumes from storage.
 */
export async function startAgent(config: AgentConfig): Promise<RunningAgent> {
	const log = (...args: unknown[]) => console.log(`[agent ${config.agentId}]`, ...args);
	const { models, model, source, usage } = createAgentModels(config.modelEnv);
	log(`model ${model.provider}/${model.modelId} via ${source}`);

	const storage = await openPostgresAgentStorage({
		connection: { connectionString: config.databaseUrl },
		schema: config.schema,
		createSchema: config.createSchema === true,
		onConnectionLost: (error) => {
			console.error(`[agent ${config.agentId}] database connection lost; exiting so a new owner can take over`, error.message);
			process.exit(1);
		},
	});

	const registry = createRegistry();
	const extension = assistantExtension(config.instructions);
	registry.install(extension);
	// MCP servers: the template's, and those apps gave threads (installed before resuming, so resumed runs find their tools).
	const mcp = new AgentMcp(config.modelEnv, (ext) => registry.install(ext));
	for (const ext of mcp.extensions) registry.install(ext);
	await mcp.restore().catch((error: unknown) => console.error(`[agent ${config.agentId}] thread MCP servers:`, (error as Error).message));
	const harness = await Harness.open(
		storage.storage,
		{ models, registry, onReport: (error) => console.error(`[agent ${config.agentId}]`, error) },
		ctx,
	);
	// Continue runs, tool calls and queued input left by a previous process.
	harness.resume();

	let closing = false;
	harness.closed.then((end) => {
		if (end.reason === "failed" && !closing) {
			console.error(`[agent ${config.agentId}] storage failed; exiting so the agent restarts from storage`, end.error);
			process.exit(1);
		}
	});

	const threads = new ThreadStore(harness, () => ({ model, extensions: [extension, ...mcp.extensions] }));
	const mcpActive = mcp.extensions.length > 0 || mcp.clientServers;
	// The platform keeps a record of every thread and its status (so listing doesn't wake the agent).
	const env = config.modelEnv;
	const status =
		env.PLATFORM_GATEWAY_URL && env.PLATFORM_KEY
			? new ThreadStatusReporter(harness, threads, gatewayPost(env.PLATFORM_GATEWAY_URL, env.PLATFORM_KEY))
			: undefined;
	void status?.sync().catch((error: unknown) => console.error(`[agent ${config.agentId}] thread sync:`, (error as Error).message));
	const { handler, touch } = createRuntimeServer({
		harness,
		threads,
		token: config.token,
		info: { agentId: config.agentId, name: config.name, model: `${model.provider}/${model.modelId}` },
		// pi keeps the model a conversation was created with (pi.agent), so a thread begun before the agent's model
		// changed would keep asking for the old one, which this runtime no longer has: its turns would end unanswered
		// (no_model) without a reply. Every message moves the thread to the agent's model now (an equal value writes
		// nothing), with the thread's MCP servers.
		beforeMessage: async (threadId, conversation) => {
			const current = { provider: model.provider, modelId: model.modelId };
			if (!mcpActive) return conversation.configure({ model: current }, ctx);
			const extensions = await mcp.forThread(threadId, [extension]).catch((error: unknown) => {
				console.error(`[agent ${config.agentId}] MCP servers of thread ${threadId}:`, (error as Error).message);
				return [extension, ...mcp.extensions];
			});
			await conversation.configure({ model: current, extensions }, ctx);
		},
		afterMessage: status ? (threadId, submissionId) => status.watch(threadId, submissionId) : undefined,
	});
	// A reply that just ended is recent work: the reaper waits a full idle period after it.
	if (status) status.onReplyEnd = touch;
	return {
		handler,
		close: async () => {
			closing = true;
			await harness.close(ctx).catch(() => {});
			await usage?.flush();
			await mcp.close();
			await status?.stop();
		},
	};
}
