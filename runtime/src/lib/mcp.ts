import type { Context } from "@earendil-works/chord/context";
import { Type } from "@earendil-works/pi-ai";
import { defineExtension, defineTool, type Extension, type ToolRegistration } from "@earendil-works/pi-durable";
import { createToolSearchExtension } from "@earendil-works/pi-durable/tool-search";
import { McpClient, StreamableHttpTransport, toLlmContent } from "@earendil-works/pi-mcp";

/** How the runtime reaches one MCP server (the platform's RuntimeServer). */
export type McpServerConfig = {
	slug: string;
	name: string;
	description?: string;
	url: string;
	headers?: Record<string, string>;
	/** A catalog server's tools; absent for an app's (client) server, whose tools are listed on connect. */
	tools?: { name: string; title?: string; description?: string; inputSchema?: Record<string, unknown> }[];
	thread?: string;
	client?: boolean;
};

/** Above this many MCP tools, they are deferred: the model finds them with tool_search. */
const DEFER_ABOVE = 20;

/**
 * Whether a tool may be offered to the model. The MCP Apps extension (io.modelcontextprotocol/ui) lets a server keep a
 * tool for its app's interface (`_meta.ui.visibility` without "model"), and a host must not offer that tool to the model.
 * Without the field, or when it isn't what the extension describes, the tool is visible (its default). The platform
 * applies the same rule to the servers it lists (internal/mcp, modelVisible).
 */
export function modelVisible(meta: unknown): boolean {
	if (meta === null || typeof meta !== "object") return true;
	const ui = (meta as { ui?: unknown }).ui;
	if (ui === null || typeof ui !== "object") return true;
	const visibility = (ui as { visibility?: unknown }).visibility;
	if (!Array.isArray(visibility)) return true;
	return visibility.includes("model");
}

/** Tool names providers accept: at most 64 of [A-Za-z0-9_-]. */
export function toolName(prefix: string, slug: string, tool: string): string {
	return `${prefix}__${slug}__${tool}`.replace(/[^A-Za-z0-9_-]/g, "_").slice(0, 64);
}

/** One lazily connected MCP session per server; reconnects after a failure. */
class Connection {
	private client: Promise<McpClient> | undefined;
	constructor(private readonly server: McpServerConfig) {}

	get(): Promise<McpClient> {
		this.client ??= (async () => {
			const client = new McpClient({ name: "pi-durable-platform-agent", version: "1", requestTimeoutMs: 120_000 });
			await client.connect(new StreamableHttpTransport({ url: this.server.url, headers: this.server.headers, openGetStream: false }));
			return client;
		})().catch((error: unknown) => {
			this.client = undefined;
			throw error;
		});
		return this.client;
	}

	async close(): Promise<void> {
		const client = this.client;
		this.client = undefined;
		await client?.then((c) => c.close()).catch(() => {});
	}
}

function toolsFor(prefix: string, server: McpServerConfig, tools: NonNullable<McpServerConfig["tools"]>, conn: Connection, deferred: boolean): ToolRegistration[] {
	return tools.map((tool) =>
		defineTool({
			name: toolName(prefix, server.slug, tool.name),
			description: `${tool.description ?? tool.title ?? tool.name} (MCP server ${server.name})`.slice(0, 4000),
			parameters: Type.Unsafe<Record<string, unknown>>({ ...(tool.inputSchema ?? {}), type: "object", properties: (tool.inputSchema?.properties as object) ?? {} }),
			...(deferred ? { deferred: true as const } : {}),
			execute: async (args, _api, context: Context) => {
				const client = await conn.get();
				try {
					const result = await client.callTool(tool.name, args as Record<string, unknown>, context.abortSignal ? { signal: context.abortSignal } : {});
					return { output: toLlmContent(result), isError: result.isError === true };
				} catch (error) {
					await conn.close(); // a fresh session next time
					throw error;
				}
			},
		}),
	);
}

/**
 * MCP servers for an agent: its template's (MCP_SERVERS: tools known up front, connected on first use) and the
 * servers an app gives each thread (fetched from the platform's MCP_THREADS_URL with MCP_KEY, listed on connect).
 * Each thread's servers are an extension only that thread selects.
 */
export class AgentMcp {
	readonly extensions: Extension[] = [];
	private readonly connections: Connection[] = [];
	private readonly threadExts = new Map<string, { key: string; extension: Extension; connections: Connection[] }>();
	private toolSearch: Extension | undefined;

	constructor(
		private readonly env: Record<string, string | undefined>,
		private readonly install: (extension: Extension) => void,
		private readonly fetchImpl: typeof fetch = fetch,
	) {
		const servers = parseServers(env.MCP_SERVERS);
		const count = servers.reduce((n, s) => n + (s.tools?.length ?? 0), 0);
		const deferred = count > DEFER_ABOVE;
		const tools: ToolRegistration[] = [];
		for (const server of servers) {
			const conn = new Connection(server);
			this.connections.push(conn);
			tools.push(...toolsFor("mcp", server, server.tools ?? [], conn, deferred));
		}
		if (tools.length > 0) this.extensions.push(defineExtension({ name: "mcp", tools }));
		if (deferred) this.extensions.push(this.searchExtension());
	}

	/** tool_search, installed the first time a set of tools needs it (a thread's servers may be the first). */
	private searchExtension(): Extension {
		if (this.toolSearch === undefined) {
			this.toolSearch = createToolSearchExtension();
			this.install(this.toolSearch);
		}
		return this.toolSearch;
	}

	/** Whether apps may give this agent's threads MCP servers. */
	get clientServers(): boolean {
		return Boolean(this.env.MCP_THREADS_URL && this.env.MCP_KEY);
	}

	private async fetchServers(threadId?: string): Promise<McpServerConfig[]> {
		const url = new URL(this.env.MCP_THREADS_URL!);
		if (threadId) url.searchParams.set("thread", threadId);
		const res = await this.fetchImpl(url, { headers: { authorization: `Bearer ${this.env.MCP_KEY}` }, signal: AbortSignal.timeout(10_000) });
		if (!res.ok) throw new Error(`thread MCP servers: status ${res.status}`);
		return ((await res.json()) as { data: McpServerConfig[] }).data ?? [];
	}

	/** Installs every thread's servers (at start, so resumed runs find their tools). */
	async restore(): Promise<void> {
		if (!this.clientServers) return;
		const byThread = new Map<string, McpServerConfig[]>();
		for (const s of await this.fetchServers()) {
			if (!s.thread) continue;
			byThread.set(s.thread, [...(byThread.get(s.thread) ?? []), s]);
		}
		for (const [thread, servers] of byThread) await this.installThread(thread, servers).catch((e) => console.error(`[runtime] MCP servers of thread ${thread}:`, (e as Error).message));
	}

	/** The MCP extensions a thread selects: the template's, plus the servers the app gave it now (fetched). */
	async forThread(threadId: string): Promise<Extension[]> {
		if (!this.clientServers) return [...this.extensions];
		return this.withThread(await this.installThread(threadId, await this.fetchServers(threadId)));
	}

	/** The MCP extensions a thread selects as far as this process knows, without asking the platform. */
	installed(threadId: string): Extension[] {
		return this.withThread(this.threadExts.get(threadId)?.extension);
	}

	private withThread(ext: Extension | undefined): Extension[] {
		const out = [...this.extensions];
		if (ext) {
			out.push(ext);
			if ((ext.tools?.length ?? 0) > DEFER_ABOVE && !out.includes(this.searchExtension())) out.push(this.searchExtension());
		}
		return out;
	}

	private async installThread(threadId: string, servers: McpServerConfig[]): Promise<Extension | undefined> {
		const key = JSON.stringify(servers.map((s) => [s.slug, s.url, s.headers ?? {}]));
		const current = this.threadExts.get(threadId);
		if (current?.key === key) return current.extension;
		for (const c of current?.connections ?? []) await c.close();
		if (servers.length === 0) {
			this.threadExts.delete(threadId);
			return undefined;
		}
		const connections: Connection[] = [];
		const listed: { server: McpServerConfig; conn: Connection; tools: NonNullable<McpServerConfig["tools"]> }[] = [];
		for (const server of servers) {
			const conn = new Connection(server);
			connections.push(conn);
			try {
				const client = await conn.get();
				const tools = (await client.listTools())
					.filter((t) => modelVisible((t as { _meta?: unknown })._meta))
					.map((t) => ({ name: t.name, title: t.title, description: t.description, inputSchema: t.inputSchema as Record<string, unknown> }));
				listed.push({ server, conn, tools });
			} catch (error) {
				console.error(`[runtime] MCP server ${server.name} of thread ${threadId}:`, (error as Error).message);
			}
		}
		const total = listed.reduce((n, l) => n + l.tools.length, 0);
		const tools = listed.flatMap((l) => toolsFor("mcpc", l.server, l.tools, l.conn, total > DEFER_ABOVE));
		const extension = defineExtension({ name: `mcp-thread:${threadId}`, tools });
		this.install(extension);
		this.threadExts.set(threadId, { key, extension, connections });
		return extension;
	}

	async close(): Promise<void> {
		for (const c of this.connections) await c.close();
		for (const t of this.threadExts.values()) for (const c of t.connections) await c.close();
	}
}

function parseServers(raw: string | undefined): McpServerConfig[] {
	if (!raw?.trim()) return [];
	const parsed: unknown = JSON.parse(raw);
	if (!Array.isArray(parsed)) throw new Error("MCP_SERVERS must be a JSON array");
	return parsed as McpServerConfig[];
}
