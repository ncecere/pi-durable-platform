import type { Usage } from "@earendil-works/pi-ai";
import { describe, expect, it } from "vitest";
import { UsageReporter } from "../src/lib/usage.ts";

const usage = (input: number, output: number, cacheRead = 0): Usage =>
	({ input, output, cacheRead, cacheWrite: 0, totalTokens: input + output, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } }) as Usage;

function fakeFetch(statuses: number[]) {
	const calls: { auth: string; body: Record<string, number> }[] = [];
	const impl = (async (_url: string, init: RequestInit) => {
		calls.push({ auth: (init.headers as Record<string, string>).authorization ?? "", body: JSON.parse(String(init.body)) });
		return new Response("{}", { status: statuses.shift() ?? 200 });
	}) as unknown as typeof fetch;
	return { impl, calls };
}

describe("usage reporter", () => {
	it("batches responses and sends them with the agent's credential", async () => {
		const f = fakeFetch([]);
		const r = new UsageReporter("http://gw/usage", "pdpm.a.t", 60_000, f.impl);
		r.add(usage(100, 20, 10));
		r.add(usage(50, 5));
		r.add(undefined);
		await r.flush();
		expect(f.calls).toEqual([
			{ auth: "Bearer pdpm.a.t", body: { requests: 2, input: 150, output: 25, cacheRead: 10, cacheWrite: 0 } },
		]);
		await r.flush(); // nothing pending: nothing sent
		expect(f.calls).toHaveLength(1);
	});

	it("keeps counts after a failed send and sends them with the next batch", async () => {
		const f = fakeFetch([503, 200]);
		const r = new UsageReporter("http://gw/usage", "k", 60_000, f.impl);
		r.add(usage(10, 1));
		await r.flush();
		r.add(usage(5, 1));
		await r.flush();
		expect(f.calls.map((c) => c.body.requests)).toEqual([1, 2]);
		expect(f.calls[1]?.body.input).toBe(15);
	});
});
