import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { describe, expect, it } from "vitest";
import { AgentMcp, toolName } from "../src/lib/mcp.ts";

/** A Streamable HTTP MCP server as a fetch: records each request's method and PDP-Thread header. */
function fakeServer(seen: string[]) {
	return (async (_url: URL, init: RequestInit) => {
		const msg = JSON.parse(String(init.body)) as { id?: number; method: string };
		seen.push(`${msg.method} ${new Headers(init.headers).get("pdp-thread") ?? "-"}`);
		if (msg.id === undefined) return new Response(null, { status: 202 });
		const result =
			msg.method === "initialize"
				? { protocolVersion: "2025-06-18", capabilities: { tools: {} }, serverInfo: { name: "fake", version: "1" } }
				: { content: [{ type: "text", text: "ok" }] };
		return new Response(JSON.stringify({ jsonrpc: "2.0", id: msg.id, result }), { status: 200, headers: { "content-type": "application/json" } });
	}) as unknown as typeof fetch;
}

describe("agent MCP", () => {
	it("names tools as providers accept them", () => {
		expect(toolName("mcp", "github", "search.issues")).toBe("mcp__github__search_issues");
		expect(toolName("mcpc", "x", "a".repeat(80))).toHaveLength(64);
	});

	it("defines the template's tools from MCP_SERVERS without connecting, deferring large sets", () => {
		const installed: string[] = [];
		const few = new AgentMcp(
			{ MCP_SERVERS: JSON.stringify([{ slug: "fake", name: "Fake", url: "http://gw/mcp-proxy/server/1", tools: [{ name: "echo", description: "Echo" }] }]) },
			(ext) => installed.push(ext.name),
		);
		expect(few.extensions.map((e) => e.name)).toEqual(["mcp"]);
		expect(few.extensions[0]?.tools?.map((t) => t.name)).toEqual(["mcp__fake__echo"]);
		expect(few.extensions[0]?.tools?.[0]?.deferred).toBeUndefined();
		expect(few.clientServers).toBe(false);

		const tools = Array.from({ length: 25 }, (_, i) => ({ name: `t${i}` }));
		const installedMany: string[] = [];
		const many = new AgentMcp({ MCP_SERVERS: JSON.stringify([{ slug: "big", name: "Big", url: "http://x", tools }]) }, (ext) => installedMany.push(ext.name));
		expect(many.extensions).toHaveLength(2); // the tools, and tool_search to find them
		// tool_search is installed when it is first needed (a thread's servers may be the first), not only selected.
		expect(installedMany).toEqual(["tool-search"]);
		expect(many.extensions[0]?.tools?.every((t) => t.deferred === true)).toBe(true);
	});

	it("fetches a thread's servers from the platform with its key", async () => {
		const seen: string[] = [];
		const fetchImpl = (async (url: URL, init: RequestInit) => {
			seen.push(`${url} ${(init.headers as Record<string, string>).authorization}`);
			return new Response(JSON.stringify({ data: [] }), { status: 200 });
		}) as unknown as typeof fetch;
		const mcp = new AgentMcp({ MCP_THREADS_URL: "http://gw/thread-mcp", MCP_KEY: "pdpm.a.t" }, () => {}, fetchImpl);
		expect(mcp.clientServers).toBe(true);
		const exts = await mcp.forThread("t1");
		expect(exts).toEqual([]);
		expect(seen).toEqual(["http://gw/thread-mcp?thread=t1 Bearer pdpm.a.t"]);
	});

	it("names a tool call's thread to the platform's proxy only, and not in the handshake", async () => {
		const seen: string[] = [];
		const servers = [
			{ slug: "proxied", name: "Proxied", url: "http://gw/mcp-proxy/server/1", sendThread: true, tools: [{ name: "echo" }] },
			{ slug: "direct", name: "Direct", url: "https://mcp.example.com/mcp", tools: [{ name: "echo" }] },
		];
		const mcp = new AgentMcp({ MCP_SERVERS: JSON.stringify(servers) }, () => {}, fakeServer(seen));
		mcp.threadOf = async (conversationId) => (conversationId === 7 ? "t_private" : undefined);
		const [proxied, direct] = mcp.extensions[0]!.tools!;
		const api = (conversationId: number) => ({ conversationId }) as never;
		await proxied!.execute({}, api(7), BACKGROUND_CONTEXT);
		await proxied!.execute({}, api(8), BACKGROUND_CONTEXT); // a conversation that is no thread (a task's)
		await direct!.execute({}, api(7), BACKGROUND_CONTEXT);
		expect(seen).toEqual([
			"initialize -",
			"notifications/initialized -",
			"tools/call t_private",
			"tools/call -",
			"initialize -",
			"notifications/initialized -",
			"tools/call -",
		]);
		await mcp.close();
	});
});
