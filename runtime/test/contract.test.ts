import { type ChildProcess, spawn } from "node:child_process";
import { createServer } from "node:net";
import { fileURLToPath } from "node:url";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { openPostgresAgentStorage } from "@pdp/runtime";
import { registerRuntimeConformance, registerStandbyConformance, startTestAgent } from "@pdp/runtime/testing";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { ADMIN_URL, dropSchema, uniqueSchema, withAdmin } from "./support.ts";

const RUNTIME_DIR = fileURLToPath(new URL("..", import.meta.url));
const TOKEN = "contract-runtime-token-0123456789";
const runner = { describe, it, expect, beforeAll, afterAll };

async function freePort(): Promise<number> {
	const server = createServer();
	await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
	const port = (server.address() as { port: number }).port;
	await new Promise((resolve) => server.close(resolve));
	return port;
}

type Proc = { child: ChildProcess; baseUrl: string; exited: Promise<number | null>; output: () => string };

/** The platform's runtime as the local launcher runs it: `node --import tsx src/main.ts`, configured by environment. */
async function spawnRuntime(env: Record<string, string>, { waitForListening = true } = {}): Promise<Proc> {
	const port = await freePort();
	const child = spawn(process.execPath, ["--import", "tsx", "src/main.ts"], {
		cwd: RUNTIME_DIR,
		env: { PATH: process.env.PATH ?? "", HOST: "127.0.0.1", PORT: String(port), ...env },
		stdio: ["ignore", "pipe", "pipe"],
	});
	let output = "";
	child.stdout!.on("data", (chunk: Buffer) => (output += chunk.toString()));
	child.stderr!.on("data", (chunk: Buffer) => (output += chunk.toString()));
	const exited = new Promise<number | null>((resolve) => child.once("exit", (code) => resolve(code)));
	const proc = { child, baseUrl: `http://127.0.0.1:${port}`, exited, output: () => output };
	if (!waitForListening) return proc;
	const deadline = Date.now() + 20_000;
	while (!output.includes("listening on")) {
		if (child.exitCode !== null || Date.now() > deadline) throw new Error(`the runtime didn't start (exit ${child.exitCode}):\n${output}`);
		await new Promise((r) => setTimeout(r, 50));
	}
	return proc;
}

async function stop(proc: Proc): Promise<number | null> {
	if (proc.child.exitCode === null) proc.child.kill("SIGTERM");
	return proc.exited;
}

async function agentSchema(): Promise<string> {
	const schema = uniqueSchema("agent_ct");
	await withAdmin((admin) => admin.query(`CREATE SCHEMA "${schema}"`));
	return schema;
}

const agentEnv = (schema: string) => ({
	AGENT_ID: "agent-contract",
	AGENT_NAME: "Contract",
	AGENT_SCHEMA: schema,
	AGENT_DATABASE_URL: ADMIN_URL,
	RUNTIME_TOKEN: TOKEN,
	MODEL_PROVIDER: "demo",
	DEMO_TOKENS_PER_SECOND: "100000",
});

registerRuntimeConformance(runner, "the platform's runtime process (PostgreSQL, demo model)", async () => {
	const schema = await agentSchema();
	const proc = await spawnRuntime(agentEnv(schema));
	return {
		baseUrl: proc.baseUrl,
		token: TOKEN,
		close: async () => {
			await stop(proc);
			await dropSchema(schema);
		},
	};
});

registerRuntimeConformance(runner, "the test kit's agent (in memory, fake model)", async () => {
	const agent = await startTestAgent();
	return { baseUrl: agent.baseUrl, token: agent.token, close: () => agent.close() };
});

registerStandbyConformance(runner, "the platform's runtime process", async () => {
	const activationToken = "contract-activation-token-0123456789";
	const schema = await agentSchema();
	const proc = await spawnRuntime({ RUNTIME_ACTIVATION_TOKEN: activationToken });
	return {
		baseUrl: proc.baseUrl,
		activationToken,
		activation: { agentId: "agent-standby", name: "Standby", schema, databaseUrl: ADMIN_URL, token: TOKEN, env: { MODEL_PROVIDER: "demo" } },
		close: async () => {
			await stop(proc);
			await dropSchema(schema);
		},
	};
});

describe("runtime contract: the process", () => {
	it("exits 0 on SIGTERM, releasing the agent for the next start", async () => {
		const schema = await agentSchema();
		try {
			const first = await spawnRuntime(agentEnv(schema));
			expect(await stop(first)).toBe(0);
			const second = await spawnRuntime(agentEnv(schema));
			expect(await stop(second)).toBe(0);
		} finally {
			await dropSchema(schema);
		}
	}, 30_000);

	it("exits 1 when its database connection is lost", async () => {
		const schema = await agentSchema();
		try {
			const proc = await spawnRuntime(agentEnv(schema));
			await withAdmin((admin) => admin.query("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = $1", [`pdp-agent:${schema}`]));
			const code = await Promise.race([proc.exited, new Promise((r) => setTimeout(() => r("still running"), 15_000))]);
			if (code === "still running") await stop(proc);
			expect(code).toBe(1);
		} finally {
			await dropSchema(schema);
		}
	}, 30_000);

	it("exits 3 while another process owns the agent", async () => {
		const schema = await agentSchema();
		const owner = await openPostgresAgentStorage({ connection: { connectionString: ADMIN_URL }, schema });
		try {
			const proc = await spawnRuntime(agentEnv(schema), { waitForListening: false });
			expect(await proc.exited).toBe(3);
		} finally {
			await owner.storage.close(BACKGROUND_CONTEXT);
			await dropSchema(schema);
		}
	}, 30_000);

	it("exits 2 without an agent or an activation token", async () => {
		const proc = await spawnRuntime({}, { waitForListening: false });
		expect(await proc.exited).toBe(2);
		expect(proc.output()).toContain("AGENT_ID (an agent) or RUNTIME_ACTIVATION_TOKEN (standby) is required");
	}, 30_000);
});
