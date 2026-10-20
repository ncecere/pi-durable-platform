import { describe, expect, it } from "vitest";
import { AgentMcp, toolName } from "../src/lib/mcp.ts";

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
		const many = new AgentMcp({ MCP_SERVERS: JSON.stringify([{ slug: "big", name: "Big", url: "http://x", tools }]) }, () => {});
		expect(many.extensions).toHaveLength(2); // the tools, and tool_search to find them
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
});
