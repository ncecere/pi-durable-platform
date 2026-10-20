import { randomBytes } from "node:crypto";
import { CONTRACT_VERSION } from "../version.ts";

/** The parts of a Vitest- or Jest-compatible runner the suites use. */
export type ConformanceRunner = {
	describe(name: string, body: () => void): void;
	it(name: string, body: () => Promise<void>, timeout?: number): void;
	beforeAll(body: () => Promise<void>, timeout?: number): void;
	afterAll(body: () => Promise<void>, timeout?: number): void;
	expect: ExpectLike;
};

/** The few matchers the suites use, which Vitest's and Jest's `expect` both have. */
export type ExpectLike = (value: unknown) => {
	toBe(expected: unknown): void;
	toEqual(expected: unknown): void;
	toMatch(expected: RegExp): void;
	toBeGreaterThanOrEqual(expected: number): void;
	toBeLessThanOrEqual(expected: number): void;
	toContain(expected: unknown): void;
};

/** A running agent runtime under test: where it listens and the bearer token the platform would present. */
export type RuntimeUnderTest = { baseUrl: string; token: string; close(): Promise<void> };

/** A standby runtime under test, and an activation body that makes it a working agent. */
export type StandbyUnderTest = { baseUrl: string; activationToken: string; activation: Record<string, unknown> & { agentId: string; token: string }; close(): Promise<void> };

const TIMEOUT = 30_000;

/**
 * The runtime contract (docs/runtime-contract.md) as HTTP tests anyone can run against a runtime: the platform's own,
 * or one built some other way. `start` runs once for the suite; the runtime's model must answer messages (any text).
 * Every test uses its own random thread IDs.
 */
export function registerRuntimeConformance(runner: ConformanceRunner, name: string, start: () => Promise<RuntimeUnderTest>): void {
	const { describe, it, beforeAll, afterAll, expect } = runner;
	describe(`runtime contract v${CONTRACT_VERSION}: ${name}`, () => {
		let rt: RuntimeUnderTest;
		beforeAll(async () => {
			rt = await start();
		}, TIMEOUT);
		afterAll(async () => {
			await rt?.close();
		}, TIMEOUT);

		const call = async (method: string, path: string, body?: unknown, headers: Record<string, string> = {}) => {
			const res = await fetch(rt.baseUrl + path, {
				method,
				headers: { authorization: `Bearer ${rt.token}`, ...(body === undefined ? {} : { "content-type": "application/json" }), ...headers },
				...(body === undefined ? {} : { body: typeof body === "string" ? body : JSON.stringify(body) }),
			});
			const text = await res.text();
			let json: { data?: any; error?: { code: string; message: string } } = {};
			try {
				json = JSON.parse(text);
			} catch {
				// Not JSON (an event stream): `text` has it.
			}
			return { status: res.status, headers: res.headers, json, text };
		};
		const thread = () => `conf_${randomBytes(6).toString("hex")}`;
		const expectError = (reply: { status: number; json: { error?: { code: string; message: string } } }, status: number, code: string) => {
			expect(reply.status).toBe(status);
			expect(reply.json.error?.code).toBe(code);
			expect(typeof reply.json.error?.message).toBe("string");
		};
		const settled = async (threadId: string, messages: number) => {
			const deadline = Date.now() + 20_000;
			for (;;) {
				const reply = await call("GET", `/threads/${threadId}`);
				const view = reply.json.data;
				if (reply.status === 200 && !view.busy && view.messages.length >= messages && view.messages.at(-1)?.role === "assistant") return view;
				if (Date.now() > deadline) throw new Error(`thread ${threadId} was not answered: ${reply.text.slice(0, 500)}`);
				await new Promise((r) => setTimeout(r, 50));
			}
		};

		it("answers /healthz without a token", async () => {
			const res = await fetch(`${rt.baseUrl}/healthz`);
			expect(res.status).toBe(200);
			expect(((await res.json()) as { data: { ok: boolean } }).data.ok).toBe(true);
		});

		it("refuses every other route without the agent's bearer token", async () => {
			for (const [method, path] of [
				["GET", "/status"],
				["GET", "/threads"],
				["GET", "/threads/x"],
				["POST", "/threads/x/messages"],
				["POST", "/threads/x/stop"],
				["GET", "/threads/x/events"],
				["GET", "/threads/x/documents/acme.notes"],
				["DELETE", "/threads/x"],
			] as const) {
				for (const authorization of [undefined, "Bearer wrong-token", `Bearer ${rt.token}x`]) {
					const res = await fetch(rt.baseUrl + path, { method, headers: authorization ? { authorization } : {} });
					expect(res.status).toBe(401);
					expect(((await res.json()) as { error: { code: string } }).error.code).toBe("unauthorized");
				}
			}
		});

		it("reports identity, activity and versions at /status", async () => {
			const { status, json } = await call("GET", "/status");
			expect(status).toBe(200);
			const s = json.data;
			for (const field of ["agentId", "name", "model", "piDurableVersion"]) expect(typeof s[field]).toBe("string");
			for (const field of ["busyThreads", "openStreams", "lastActivityAt", "contractVersion"]) expect(Number.isInteger(s[field])).toBe(true);
			expect(s.busyThreads).toBeGreaterThanOrEqual(0);
			expect(s.openStreams).toBeGreaterThanOrEqual(0);
			expect(s.lastActivityAt).toBeLessThanOrEqual(Date.now() + 1000);
			expect(s.contractVersion).toBe(CONTRACT_VERSION);
			expect(s.piDurableVersion).toMatch(/^\d+\.\d+\.\d+/);
		});

		it("starts a thread with a message, lists it and answers it", async () => {
			const id = thread();
			const before = Date.now();
			const sent = await call("POST", `/threads/${id}/messages`, { text: "Hello from the conformance suite\nsecond line" });
			expect(sent.status).toBe(202);
			expect(Number.isInteger(sent.json.data.submissionId)).toBe(true);
			const view = await settled(id, 2);
			expect(view.messages[0].role).toBe("user");
			expect(view.messages[0].text).toBe("Hello from the conformance suite\nsecond line");
			expect(Array.isArray(view.tools)).toBe(true);
			for (const message of view.messages) expect(typeof message.id).toBe("string");
			const list = await call("GET", "/threads");
			expect(list.status).toBe(200);
			const record = list.json.data.find((t: { threadId: string }) => t.threadId === id);
			expect(record?.title).toBe("Hello from the conformance suite");
			expect(record.createdAt).toBeGreaterThanOrEqual(before - 1000);
			expect(record.updatedAt).toBeGreaterThanOrEqual(record.createdAt);
		});

		it("deduplicates a retried requestId", async () => {
			const id = thread();
			const first = await call("POST", `/threads/${id}/messages`, { text: "once", requestId: "req-1" });
			const again = await call("POST", `/threads/${id}/messages`, { text: "once", requestId: "req-1" });
			expect(first.status).toBe(202);
			expect(again.status).toBe(202);
			expect(again.json.data.submissionId).toBe(first.json.data.submissionId);
			const view = await settled(id, 2);
			expect(view.messages.filter((m: { role: string }) => m.role === "user").length).toBe(1);
		});

		it("accepts a follow-up and a steer, and records the author the platform sends", async () => {
			const id = thread();
			const author = { kind: "user", id: "u-1", name: "Conformance" };
			expect((await call("POST", `/threads/${id}/messages`, { text: "first", author })).status).toBe(202);
			expect((await call("POST", `/threads/${id}/messages`, { text: "steer", whenBusy: "steer" })).status).toBe(202);
			expect((await call("POST", `/threads/${id}/messages`, { text: "follow", whenBusy: "followUp" })).status).toBe(202);
			const view = await settled(id, 4);
			const users = view.messages.filter((m: { role: string }) => m.role === "user");
			expect(users.length).toBe(3);
			expect(users[0].author).toEqual(author);
		});

		it("validates messages and thread IDs", async () => {
			const id = thread();
			expectError(await call("POST", `/threads/${"x".repeat(65)}/messages`, { text: "hi" }), 400, "invalid_thread_id");
			expectError(await call("POST", "/threads/bad.id/messages", { text: "hi" }), 400, "invalid_thread_id");
			expectError(await call("POST", `/threads/${id}/messages`, { text: "   " }), 400, "invalid_text");
			expectError(await call("POST", `/threads/${id}/messages`, { text: "x".repeat(32_001) }), 400, "invalid_text");
			expectError(await call("POST", `/threads/${id}/messages`, { text: "hi", requestId: "no spaces" }), 400, "invalid_request_id");
			expectError(await call("POST", `/threads/${id}/messages`, { text: "hi", whenBusy: "now" }), 400, "invalid_when_busy");
			const bad = await call("POST", `/threads/${id}/messages`, "{not json");
			expect(bad.status).toBe(400);
			// None of these started the thread.
			expectError(await call("GET", `/threads/${id}`), 404, "thread_not_found");
		});

		it("answers 404 thread_not_found for a thread it doesn't have", async () => {
			const id = thread();
			expectError(await call("GET", `/threads/${id}`), 404, "thread_not_found");
			expectError(await call("POST", `/threads/${id}/stop`), 404, "thread_not_found");
			expectError(await call("GET", `/threads/${id}/events?once=true`), 404, "thread_not_found");
			expectError(await call("DELETE", `/threads/${id}`), 404, "thread_not_found");
			expectError(await call("GET", `/threads/${id}/documents/acme.notes`), 404, "thread_not_found");
			expectError(await call("GET", "/nothing-here"), 404, "not_found");
		});

		it("answers 404 document_not_found for a document a thread doesn't expose", async () => {
			const id = thread();
			await call("POST", `/threads/${id}/messages`, { text: "documents?" });
			await settled(id, 2);
			expectError(await call("GET", `/threads/${id}/documents/conformance.never-exposed`), 404, "document_not_found");
			expectError(await call("GET", `/threads/${id}/documents/pi.live`), 404, "document_not_found");
			expectError(await call("GET", `/threads/${id}/documents/pdp.threads`), 404, "document_not_found");
		});

		it("pages a transcript", async () => {
			const id = thread();
			await call("POST", `/threads/${id}/messages`, { text: "page me" });
			await settled(id, 2);
			const page = await call("GET", `/threads/${id}?limit=1`);
			expect(page.status).toBe(200);
			expect(page.json.data.messages.length).toBeLessThanOrEqual(2);
			expect(page.json.data.messages.at(-1).role).toBe("assistant");
			expect(typeof page.json.data.earlierCursor).toBe("string");
			const earlier = await call("GET", `/threads/${id}?limit=1&before=${encodeURIComponent(page.json.data.earlierCursor)}`);
			expect(earlier.status).toBe(200);
			expect(earlier.json.data.messages[0].text).toBe("page me");
			expect((await call("GET", `/threads/${id}?limit=0`)).status).toBe(400);
		});

		it("streams a thread as views, or as a snapshot and deltas", async () => {
			const id = thread();
			await call("POST", `/threads/${id}/messages`, { text: "stream me" });
			await settled(id, 2);
			const views = await call("GET", `/threads/${id}/events?once=true`);
			expect(views.status).toBe(200);
			expect(views.headers.get("content-type")).toContain("text/event-stream");
			expect(views.text).toContain("event: view\n");
			const view = JSON.parse(views.text.split("\n").find((l) => l.startsWith("data: "))!.slice(6));
			expect(view.messages[0].text).toBe("stream me");
			const deltas = await call("GET", `/threads/${id}/events?once=true&format=delta&v=1`);
			expect(deltas.status).toBe(200);
			expect(deltas.text).toContain("event: snapshot\n");
			expect(deltas.text).toMatch(/^id: \d+$/m);
			expectError(await call("GET", `/threads/${id}/events?format=delta&v=99`), 400, "unsupported_version");
			expectError(await call("GET", `/threads/${id}/events?format=xml`), 400, "invalid_format");
		});

		it("counts open event streams at /status", async () => {
			const id = thread();
			await call("POST", `/threads/${id}/messages`, { text: "watch me" });
			await settled(id, 2);
			const abort = new AbortController();
			const res = await fetch(`${rt.baseUrl}/threads/${id}/events`, { headers: { authorization: `Bearer ${rt.token}` }, signal: abort.signal });
			const reader = res.body!.getReader();
			await reader.read();
			expect((await call("GET", "/status")).json.data.openStreams).toBeGreaterThanOrEqual(1);
			abort.abort();
			await reader.cancel().catch(() => {});
			const deadline = Date.now() + 5000;
			while ((await call("GET", "/status")).json.data.openStreams !== 0) {
				if (Date.now() > deadline) throw new Error("the closed stream is still counted");
				await new Promise((r) => setTimeout(r, 50));
			}
		});

		it("stops and deletes a thread", async () => {
			const id = thread();
			await call("POST", `/threads/${id}/messages`, { text: "delete me" });
			const stopped = await call("POST", `/threads/${id}/stop`);
			expect(stopped.status).toBe(202);
			const deleted = await call("DELETE", `/threads/${id}`);
			expect(deleted.status).toBe(200);
			expect(deleted.json.data.ok).toBe(true);
			expectError(await call("GET", `/threads/${id}`), 404, "thread_not_found");
			const list = await call("GET", "/threads");
			expect(list.json.data.some((t: { threadId: string }) => t.threadId === id)).toBe(false);
		});
	});
}

/**
 * Standby and activation (docs/runtime-contract.md): a runtime started without an agent waits, refuses a wrong
 * activation token and other routes, becomes the agent on POST /activate, accepts the same activation again and
 * refuses another agent. `start` runs once per test (an activation can't be undone).
 */
export function registerStandbyConformance(runner: ConformanceRunner, name: string, start: () => Promise<StandbyUnderTest>): void {
	const { describe, it, expect } = runner;
	describe(`runtime contract v${CONTRACT_VERSION} (standby): ${name}`, () => {
		const post = (sb: StandbyUnderTest, token: string, body: unknown) =>
			fetch(`${sb.baseUrl}/activate`, { method: "POST", headers: { authorization: `Bearer ${token}`, "content-type": "application/json" }, body: JSON.stringify(body) });
		const health = async (sb: StandbyUnderTest) => ((await (await fetch(`${sb.baseUrl}/healthz`)).json()) as { data: { ok: boolean; activated: boolean } }).data;

		it(
			"waits, is activated once as one agent, and then is that agent",
			async () => {
				const sb = await start();
				try {
					expect(await health(sb)).toEqual({ ok: true, activated: false });
					const waiting = await fetch(`${sb.baseUrl}/status`, { headers: { authorization: `Bearer ${sb.activation.token}` } });
					expect(waiting.status).toBe(503);
					expect(((await waiting.json()) as { error: { code: string } }).error.code).toBe("not_activated");
					expect((await post(sb, "wrong-activation-token", sb.activation)).status).toBe(401);
					const invalid = await post(sb, sb.activationToken, { ...sb.activation, schema: "public" });
					expect(invalid.status).toBe(400);
					expect(((await invalid.json()) as { error: { code: string } }).error.code).toBe("invalid_config");
					expect((await health(sb)).activated).toBe(false);

					const activated = await post(sb, sb.activationToken, sb.activation);
					expect(activated.status).toBe(200);
					expect(((await activated.json()) as { data: { activated: boolean; agentId: string } }).data).toEqual({ activated: true, agentId: sb.activation.agentId });
					expect((await health(sb)).activated).toBe(true);
					const status = await fetch(`${sb.baseUrl}/status`, { headers: { authorization: `Bearer ${sb.activation.token}` } });
					expect(status.status).toBe(200);
					const data = ((await status.json()) as { data: { agentId: string; contractVersion: number } }).data;
					expect(data.agentId).toBe(sb.activation.agentId);
					expect(data.contractVersion).toBe(CONTRACT_VERSION);
					// The activation token is not the agent's.
					expect((await fetch(`${sb.baseUrl}/status`, { headers: { authorization: `Bearer ${sb.activationToken}` } })).status).toBe(401);

					expect((await post(sb, sb.activationToken, sb.activation)).status).toBe(200);
					const other = await post(sb, sb.activationToken, { ...sb.activation, agentId: `${sb.activation.agentId}-other` });
					expect(other.status).toBe(409);
					expect(((await other.json()) as { error: { code: string } }).error.code).toBe("already_activated");
				} finally {
					await sb.close();
				}
			},
			TIMEOUT,
		);
	});
}
