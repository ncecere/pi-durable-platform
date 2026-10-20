import type { AddressInfo } from "node:net";
import type { Server } from "node:http";
import { afterEach, describe, expect, it } from "vitest";
import { type RunningAgent, startAgent } from "../src/lib/agent.ts";
import { createStandbyServer, parseConfig } from "../src/lib/standby.ts";
import { ADMIN_URL, dropSchema, uniqueSchema, withAdmin } from "./support.ts";

const ACTIVATION = "activation-token-for-tests-only-0123456789";
const cleanup: (() => Promise<unknown>)[] = [];
afterEach(async () => {
	while (cleanup.length > 0) await cleanup.pop()!().catch(() => {});
});

/** A standby runtime on a random loopback port; the agents it activates are closed after the test. */
async function standby(): Promise<{ base: string; agents: RunningAgent[] }> {
	const agents: RunningAgent[] = [];
	const server: Server = createStandbyServer({
		activationToken: ACTIVATION,
		start: async (config) => {
			const agent = await startAgent(config);
			agents.push(agent);
			return agent;
		},
	});
	await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
	cleanup.push(async () => {
		for (const agent of agents) await agent.close();
		await new Promise((resolve) => server.close(resolve));
	});
	return { base: `http://127.0.0.1:${(server.address() as AddressInfo).port}`, agents };
}

async function agentSchema(): Promise<string> {
	const schema = uniqueSchema("agent_sb");
	await withAdmin((admin) => admin.query(`CREATE SCHEMA "${schema}"`));
	cleanup.push(() => dropSchema(schema));
	return schema;
}

const body = (schema: string, agentId = "agent-1", token = "runtime-token-0123456789") => ({
	agentId,
	name: "Standby test",
	schema,
	databaseUrl: ADMIN_URL,
	token,
	env: { MODEL_PROVIDER: "demo" },
});

const post = (base: string, auth: string, payload: unknown) =>
	fetch(`${base}/activate`, { method: "POST", headers: { authorization: `Bearer ${auth}`, "content-type": "application/json" }, body: JSON.stringify(payload) });

describe("standby runtime", () => {
	it("waits unactivated, refuses a wrong activation token, then becomes the agent", async () => {
		const { base } = await standby();
		const schema = await agentSchema();

		const health = await fetch(`${base}/healthz`);
		expect(health.status).toBe(200);
		expect((await health.json()).data).toEqual({ ok: true, activated: false });
		expect((await fetch(`${base}/status`)).status).toBe(503);

		expect((await post(base, "wrong-token", body(schema))).status).toBe(401);
		expect((await fetch(`${base}/status`)).status).toBe(503);

		const activated = await post(base, ACTIVATION, body(schema));
		expect(activated.status).toBe(200);
		expect((await (await fetch(`${base}/healthz`)).json()).data.activated).toBe(true);

		// Now it is the agent: its own token works, the activation token does not.
		const status = await fetch(`${base}/status`, { headers: { authorization: "Bearer runtime-token-0123456789" } });
		expect(status.status).toBe(200);
		expect((await status.json()).data.agentId).toBe("agent-1");
		expect((await fetch(`${base}/status`, { headers: { authorization: `Bearer ${ACTIVATION}` } })).status).toBe(401);

		// Retrying the same activation is fine; becoming another agent is not.
		expect((await post(base, ACTIVATION, body(schema))).status).toBe(200);
		const other = await post(base, ACTIVATION, body(schema, "agent-2"));
		expect(other.status).toBe(409);
		expect((await other.json()).error.code).toBe("already_activated");
	});

	it("answers owned_elsewhere while the previous runtime holds the agent, and stays in standby", async () => {
		const schema = await agentSchema();
		const first = await standby();
		expect((await post(first.base, ACTIVATION, body(schema))).status).toBe(200);

		const second = await standby();
		const refused = await post(second.base, ACTIVATION, body(schema));
		expect(refused.status).toBe(409);
		expect((await refused.json()).error.code).toBe("owned_elsewhere");
		expect((await (await fetch(`${second.base}/healthz`)).json()).data.activated).toBe(false);

		// The first runtime hibernates (releases the agent); the second takes over.
		await first.agents[0]!.close();
		expect((await post(second.base, ACTIVATION, body(schema))).status).toBe(200);
	});
});

describe("activation body", () => {
	const valid = { agentId: "a1", schema: "agent_abc", databaseUrl: "postgres://u:p@db/x", token: "0123456789abcdef" };

	it("accepts a valid configuration with model settings", () => {
		const config = parseConfig({ ...valid, env: { MODEL_PROVIDER: "demo", MODEL_ID: "m" } });
		expect(typeof config).toBe("object");
	});

	it.each([
		[{ ...valid, schema: "public" }, "schema"],
		[{ ...valid, databaseUrl: "http://evil" }, "databaseUrl"],
		[{ ...valid, token: "short" }, "token"],
		[{ ...valid, agentId: "../x" }, "agentId"],
		[{ ...valid, env: { NODE_OPTIONS: "--require /tmp/x" } }, "env.NODE_OPTIONS"],
		[{ ...valid, env: { MODEL_ID: 1 } }, "env.MODEL_ID"],
	])("rejects %j", (input, problem) => {
		expect(parseConfig(input)).toContain(problem);
	});
});
