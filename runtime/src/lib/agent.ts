import type { RequestListener } from "node:http";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import type { MutableModels } from "@earendil-works/pi-ai/models";
import { AgentDoc, type Conversation, createRegistry, type Extension, Harness, type HarnessSettings, type ModelRef, type Storage } from "@earendil-works/pi-durable";
import { assistantAgent } from "./assistant.ts";
import {
	type AgentContext,
	type AgentDefinition,
	createConversationDocuments,
	createSessionDocuments,
	defineAgent,
	exposedDocuments,
	resolveExtensions,
	resolveSettings,
} from "./define.ts";
import { AgentMcp } from "./mcp.ts";
import { createAgentModels } from "./models.ts";
import { createRuntimeServer } from "./server.ts";
import { AgentOwnedElsewhereError, openPostgresAgentStorage } from "./storage/postgres.ts";
import { gatewayPost, ThreadStatusReporter } from "./thread-status.ts";
import { ThreadStore } from "./threads.ts";
import { checkUpgrade, RuntimeRefusedError } from "./upgrade.ts";

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
	/** Model, MCP and platform settings (MODEL_*, MCP_*, PLATFORM_*), see models.ts and mcp.ts. */
	modelEnv: Record<string, string | undefined>;
	createSchema?: boolean;
};

/** Replacements for what the platform normally provides, for tests (see the test kit, `@pdp/runtime/testing`). */
export type StartOptions = {
	/** An open storage to use instead of the agent's PostgreSQL schema (`schema` and `databaseUrl` are then unused). */
	storage?: Storage;
	/** Models to use instead of those MODEL_* describes. */
	models?: { models: MutableModels; model: ModelRef };
	/**
	 * The agent can't go on: its storage failed or its database connection was lost. The default logs and exits the
	 * process with status 1, so the supervisor starts a fresh one that resumes from storage (the runtime contract).
	 */
	onFatal?: (error: unknown) => void;
};

/** What a process serves for an agent: its HTTP handler, and a graceful stop. */
export type AgentProcess = {
	handler: RequestListener;
	close(): Promise<void>;
};

export type RunningAgent = AgentProcess & {
	handler: RequestListener;
	/** The open harness, for tests and tools that need to look at storage. */
	harness: Harness;
	threads: ThreadStore;
	/** Graceful stop: work in flight stays pending in storage and resumes on the next start. */
	close(): Promise<void>;
};

const ctx = BACKGROUND_CONTEXT;

export { AgentOwnedElsewhereError, RuntimeRefusedError };

/**
 * Open the agent's storage (taking its ownership, see storage/postgres.ts), install the definition's extensions and
 * the MCP servers' tools, resume unfinished work and return its HTTP handler. Throws AgentOwnedElsewhereError when
 * another process owns the agent, and RuntimeRefusedError, before the storage is opened, when the platform refuses
 * this runtime (the upgrade check, upgrade.ts: a newer pi-durable has written the agent's storage). Once running, it calls `onFatal` (by default: exits the process) when storage
 * fails, so the supervisor starts a fresh one that resumes from storage.
 *
 * The library owns the extension selection: every thread selects the definition's extensions, then the MCP
 * servers', on creation, again before every message, and at start for runs a previous process left unfinished.
 */
export async function startAgent(config: AgentConfig, definition: AgentDefinition = assistantAgent, options: StartOptions = {}): Promise<RunningAgent> {
	const log = (...args: unknown[]) => console.log(`[agent ${config.agentId}]`, ...args);
	const logError = (...args: unknown[]) => console.error(`[agent ${config.agentId}]`, ...args);
	let closing = false;
	let fatal = false;
	const onFatal = (error: unknown) => {
		if (closing || fatal) return;
		fatal = true;
		if (options.onFatal) return options.onFatal(error);
		logError("storage failed; exiting so the agent restarts from storage", error instanceof Error ? error.message : error);
		process.exit(1);
	};

	defineAgent(definition);
	const context: AgentContext = { agentId: config.agentId, name: config.name, instructions: config.instructions, env: config.modelEnv };
	const extensions = resolveExtensions(definition, context);
	const settings = resolveSettings(definition, context);
	const documents = definition.documents ?? [];
	const exposed = exposedDocuments(definition);

	// The upgrade check, before anything reads the storage (upgrade.ts).
	await checkUpgrade(config.modelEnv, log);

	const created = options.models ? { ...options.models, source: "given", usage: undefined } : createAgentModels(config.modelEnv);
	const { models, model, usage } = created;
	log(`model ${model.provider}/${model.modelId} via ${created.source}${definition.name ? `; agent ${definition.name}` : ""}`);

	const storage =
		options.storage ??
		(
			await openPostgresAgentStorage({
				connection: { connectionString: config.databaseUrl },
				schema: config.schema,
				createSchema: config.createSchema === true,
				onConnectionLost: (error) => onFatal(new Error(`database connection lost: ${error.message}`)),
			})
		).storage;

	const registry = createRegistry();
	for (const extension of extensions) registry.install(extension);
	// MCP servers: the template's, and those apps gave threads (installed before resuming, so resumed runs find their tools).
	const mcp = new AgentMcp(config.modelEnv, (ext) => registry.install(ext));
	for (const ext of mcp.extensions) registry.install(ext);
	await mcp.restore().catch((error: unknown) => logError("thread MCP servers:", (error as Error).message));

	// The default selection is what a conversation gets when nothing chose for it (one a tool creates, for instance):
	// the agent's extensions and the template's MCP servers, never another thread's servers.
	const base = (): Extension[] => [...extensions, ...mcp.extensions];
	const harnessSettings: HarnessSettings = Object.defineProperties({}, {
		...Object.getOwnPropertyDescriptors(settings),
		extensions: { get: base, enumerable: true },
	});
	const harness = await Harness.open(
		storage,
		{
			models,
			registry,
			settings: harnessSettings,
			conversationCreated: async (tx, conversation) => {
				await createConversationDocuments(tx, conversation, documents);
				await definition.conversationCreated?.(tx, conversation);
			},
			onReport: (error) => logError(error),
		},
		ctx,
	);
	if (documents.some((doc) => doc.definition.scope === "session")) await harness.commit((tx) => createSessionDocuments(tx, documents), ctx);

	const current: ModelRef = { provider: model.provider, modelId: model.modelId };
	const threads = new ThreadStore(harness, () => ({ model: current, extensions: base() }));
	// Calls to the template's MCP servers through the platform name their thread, so the platform can say who they are for.
	mcp.threadOf = (conversationId) => threads.threadOf(conversationId);
	// pi keeps the model and extensions a conversation was created with (pi.agent). A thread begun before the agent's
	// model changed would keep asking for the old one, which this runtime no longer has (its turns would end unanswered,
	// no_model), and one begun before an extension was added would run without it (a guard, say). So the runtime moves
	// every thread to the agent's model and selection: here, before resuming, for runs a previous process left
	// unfinished, and before every message (an equal value writes nothing).
	const align = (conversation: Conversation, selection: readonly Extension[]) => conversation.configure({ model: current, extensions: [...selection] }, ctx);
	for (const record of await threads.list()) {
		const selection = [...extensions, ...mcp.installed(record.threadId)];
		const stored = await harness.snapshot(AgentDoc, record.conversationId as never, ctx);
		if (sameSelection(stored, current, selection)) continue;
		const conversation = await threads.get(record.threadId);
		if (conversation !== undefined) await align(conversation, selection);
	}
	// Continue runs, tool calls and queued input left by a previous process.
	harness.resume();
	harness.closed.then((end) => {
		if (end.reason === "failed") onFatal(end.error);
	});

	// The platform keeps a record of every thread and its status (so listing doesn't wake the agent).
	const env = config.modelEnv;
	const status =
		env.PLATFORM_GATEWAY_URL && env.PLATFORM_KEY
			? new ThreadStatusReporter(harness, threads, gatewayPost(env.PLATFORM_GATEWAY_URL, env.PLATFORM_KEY))
			: undefined;
	void status?.sync().catch((error: unknown) => logError("thread sync:", (error as Error).message));
	const { handler, touch } = createRuntimeServer({
		harness,
		threads,
		token: config.token,
		info: { agentId: config.agentId, name: config.name, model: `${model.provider}/${model.modelId}` },
		beforeMessage: async (threadId, conversation) => {
			const servers = await mcp.forThread(threadId).catch((error: unknown) => {
				logError(`MCP servers of thread ${threadId}:`, (error as Error).message);
				return mcp.extensions;
			});
			await align(conversation, [...extensions, ...servers]);
		},
		afterMessage: status ? (threadId, submissionId) => status.watch(threadId, submissionId) : undefined,
		documents: exposed,
	});
	// A reply that just ended is recent work: the reaper waits a full idle period after it.
	if (status) status.onReplyEnd = touch;
	return {
		handler,
		harness,
		threads,
		close: async () => {
			closing = true;
			await harness.close(ctx).catch(() => {});
			await usage?.flush();
			await mcp.close();
			await status?.stop();
		},
	};
}

/** Whether a conversation's stored agent already has this model and exactly this extension selection. */
function sameSelection(stored: { model?: ModelRef; extensions?: unknown } | undefined, model: ModelRef, selection: readonly Extension[]): boolean {
	if (stored === undefined) return false;
	if (stored.model?.provider !== model.provider || stored.model?.modelId !== model.modelId) return false;
	const names = stored.extensions;
	return Array.isArray(names) && names.length === selection.length && names.every((name, i) => name === selection[i]!.name);
}
