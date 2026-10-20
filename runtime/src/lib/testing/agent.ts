import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import type { Harness } from "@earendil-works/pi-durable";
import { type AgentConfig, type RunningAgent, startAgent } from "../agent.ts";
import { assistantAgent, DEFAULT_INSTRUCTIONS } from "../assistant.ts";
import type { AgentDefinition } from "../define.ts";
import type { ChatView } from "../view.ts";
import { type FakeModel, fakeModel } from "./model.ts";
import { memoryStorage, type TestStorage } from "./storage.ts";

export type TestAgentOptions = {
	/** Default: a fresh `fakeModel()`. */
	model?: FakeModel;
	/** Default: a fresh `memoryStorage()`. */
	storage?: TestStorage;
	/** The platform's variables (MCP_SERVERS, MCP_THREADS_URL, ...); the model always comes from `model`. */
	env?: Record<string, string>;
	instructions?: string;
	agentId?: string;
	name?: string;
};

export type TestAgent = {
	readonly baseUrl: string;
	/** The bearer token the agent's HTTP API expects. */
	readonly token: string;
	readonly agent: RunningAgent;
	readonly harness: Harness;
	readonly model: FakeModel;
	readonly storage: TestStorage;
	/** Errors the agent reported as fatal (it would have exited). */
	readonly fatal: unknown[];
	/** Sends a message over HTTP; returns its submission ID. */
	send(threadId: string, text: string, body?: Record<string, unknown>): Promise<number>;
	/** The thread's chat view, or undefined when it doesn't exist. */
	view(threadId: string): Promise<ChatView | undefined>;
	/**
	 * An exposed document of the thread, as apps read it (`GET /threads/{thread}/documents/{kind}`), or undefined when
	 * the thread or the document doesn't exist (or the kind isn't exposed). Other errors throw.
	 */
	document(threadId: string, kind: string): Promise<ExposedDocumentRead | undefined>;
	/** Waits until the thread is idle and its last message is an answer (at most `timeoutMs`, default 10 s). */
	settled(threadId: string, options?: { timeoutMs?: number; messages?: number }): Promise<ChatView>;
	/** The agent's `/status`. */
	status(): Promise<Record<string, unknown>>;
	/**
	 * The process dies now, wherever it is (in a model request, a tool call, a subagent): nothing reaches storage any
	 * more, its HTTP server stops and its running code is aborted.
	 */
	crash(): void;
	/** Crashes (unless already crashed or closed) and starts the agent again on the same storage, as its supervisor would. */
	restart(changes?: { definition?: AgentDefinition; model?: FakeModel; env?: Record<string, string> }): Promise<TestAgent>;
	/** A graceful stop (hibernation). */
	close(): Promise<void>;
};

/** An exposed document as the runtime serves it: its kind, its definition's version, its revision and its value. */
export type ExposedDocumentRead = { kind: string; version: number; revision: string; value: unknown };

const TOKEN = "test-kit-runtime-token-0123456789";

/**
 * Starts an agent from a definition the way the platform would, but on in-memory storage and a fake model, with its
 * HTTP API on a loopback port. The platform's own runtime tests use it; so can a custom agent's.
 */
export async function startTestAgent(definition: AgentDefinition = assistantAgent, options: TestAgentOptions = {}): Promise<TestAgent> {
	const model = options.model ?? fakeModel();
	const storage = options.storage ?? memoryStorage();
	const config: AgentConfig = {
		agentId: options.agentId ?? "agent-test",
		name: options.name ?? "Test agent",
		instructions: options.instructions ?? DEFAULT_INSTRUCTIONS,
		schema: "agent_test",
		databaseUrl: "postgres://unused",
		token: TOKEN,
		modelEnv: { ...(options.env ?? {}) },
	};
	const fatal: unknown[] = [];
	const agent = await startAgent(config, definition, { storage: storage.open(), models: model, onFatal: (error) => fatal.push(error) });
	const server: Server = createServer(agent.handler);
	await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
	const baseUrl = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
	let ended = false;
	const stopServer = () => {
		server.closeAllConnections();
		server.close();
	};
	const call = async (path: string, init: RequestInit = {}) => {
		const res = await fetch(baseUrl + path, { ...init, headers: { authorization: `Bearer ${TOKEN}`, "content-type": "application/json", ...(init.headers ?? {}) } });
		const body = (await res.json()) as { data?: unknown; error?: { code: string; message: string } };
		return { status: res.status, body };
	};
	const view = async (threadId: string) => {
		const { status, body } = await call(`/threads/${threadId}`);
		if (status === 404) return undefined;
		if (status !== 200) throw new Error(`GET /threads/${threadId}: ${status} ${JSON.stringify(body.error)}`);
		return body.data as ChatView;
	};
	const self: TestAgent = {
		baseUrl,
		token: TOKEN,
		agent,
		harness: agent.harness,
		model,
		storage,
		fatal,
		send: async (threadId, text, body = {}) => {
			const { status, body: reply } = await call(`/threads/${threadId}/messages`, { method: "POST", body: JSON.stringify({ text, ...body }) });
			if (status !== 202) throw new Error(`POST /threads/${threadId}/messages: ${status} ${JSON.stringify(reply.error)}`);
			return (reply.data as { submissionId: number }).submissionId;
		},
		view,
		document: async (threadId, kind) => {
			const { status, body } = await call(`/threads/${threadId}/documents/${encodeURIComponent(kind)}`);
			if (status === 404) return undefined;
			if (status !== 200) throw new Error(`GET /threads/${threadId}/documents/${kind}: ${status} ${JSON.stringify(body.error)}`);
			return body.data as ExposedDocumentRead;
		},
		settled: async (threadId, { timeoutMs = 10_000, messages = 0 } = {}) => {
			const deadline = Date.now() + timeoutMs;
			let last: ChatView | undefined;
			while (Date.now() < deadline) {
				last = await view(threadId);
				const tail = last?.messages.at(-1);
				if (last && !last.busy && last.tools.length === 0 && last.messages.length >= messages && tail?.role === "assistant" && !tail.toolCalls?.length) return last;
				await new Promise((r) => setTimeout(r, 20));
			}
			throw new Error(`thread ${threadId} did not settle within ${timeoutMs} ms: ${JSON.stringify(last?.messages.at(-1) ?? null)}`);
		},
		status: async () => (await call("/status")).body.data as Record<string, unknown>,
		crash: () => {
			if (ended) return;
			ended = true;
			storage.crash();
			stopServer();
			// Aborts the running code (its context's signal); it can no longer write. Not awaited: code that ignores
			// its signal never returns, as it would never return in a dead process.
			void agent.harness.close(BACKGROUND_CONTEXT).catch(() => {});
		},
		restart: async (changes = {}) => {
			self.crash();
			return startTestAgent(changes.definition ?? definition, {
				...options,
				model: changes.model ?? model,
				storage,
				env: changes.env ?? options.env,
			});
		},
		close: async () => {
			if (ended) return;
			ended = true;
			stopServer();
			await agent.close();
		},
	};
	return self;
}
