import { timingSafeEqual } from "node:crypto";
import { createServer, type IncomingMessage, type RequestListener, type Server, type ServerResponse } from "node:http";
import { type AgentConfig, AgentOwnedElsewhereError, type AgentProcess } from "./agent.ts";
import { DEFAULT_INSTRUCTIONS } from "./assistant.ts";

/**
 * Standby: a pre-started runtime (for example in an Agent Sandbox warm pool) that isn't any agent yet. The platform
 * claims it and sends one agent's configuration to POST /activate, authenticated with the pool's activation token;
 * from then on the process is that agent, exactly as if it had been started with its environment variables.
 *
 * Activating again with the same agent and token answers 200 (the platform may retry); anything else is refused
 * (409 already_activated). When another process still owns the agent (its previous runtime is shutting down), it
 * answers 409 owned_elsewhere and stays in standby, so the platform can retry. Until activated, other routes answer
 * 503 not_activated. /healthz answers in both states, so the pod is Ready (and counted by the pool) while it waits.
 */
export function createStandbyServer(options: {
	activationToken: string;
	start: (config: AgentConfig) => Promise<AgentProcess>;
	onActivated?: (agent: AgentProcess, config: AgentConfig) => void;
}): Server {
	const expected = Buffer.from(`Bearer ${options.activationToken}`);
	let active: { config: AgentConfig; agent?: AgentProcess; starting: Promise<AgentProcess> } | undefined;
	let delegate: RequestListener | undefined;

	const handler: RequestListener = (request, response) => {
		const url = new URL(request.url ?? "/", "http://runtime");
		if (request.method === "GET" && url.pathname === "/healthz") {
			return json(response, 200, { ok: true, activated: delegate !== undefined });
		}
		if (request.method === "POST" && url.pathname === "/activate") {
			activate(request, response).catch((error: unknown) => {
				// A half-started agent must not linger: exit, and the restarted runtime is activated afresh.
				console.error("[runtime] activation failed", error);
				if (response.headersSent) process.exit(1);
				response.on("finish", () => process.exit(1));
				fail(response, 500, "activation_failed", "The agent could not start");
			});
			return;
		}
		if (delegate) return delegate(request, response);
		return fail(response, 503, "not_activated", "This runtime is waiting to be activated");
	};

	const activate = async (request: IncomingMessage, response: ServerResponse) => {
		const header = Buffer.from(request.headers.authorization ?? "");
		if (header.length !== expected.length || !timingSafeEqual(header, expected)) {
			return fail(response, 401, "unauthorized", "Unauthorized");
		}
		const config = parseConfig(await readJson(request));
		if (typeof config === "string") return fail(response, 400, "invalid_config", config);
		if (active) {
			if (active.config.agentId !== config.agentId || active.config.token !== config.token) {
				return fail(response, 409, "already_activated", "This runtime already runs another agent");
			}
			try {
				await active.starting;
			} catch (error) {
				if (error instanceof AgentOwnedElsewhereError) return fail(response, 409, "owned_elsewhere", "Another runtime still owns this agent");
				throw error;
			}
			return json(response, 200, { activated: true, agentId: config.agentId });
		}
		const starting = options.start(config);
		active = { config, starting };
		let agent: AgentProcess;
		try {
			agent = await starting;
		} catch (error) {
			if (error instanceof AgentOwnedElsewhereError) {
				active = undefined;
				return fail(response, 409, "owned_elsewhere", "Another runtime still owns this agent");
			}
			throw error;
		}
		active.agent = agent;
		delegate = agent.handler;
		options.onActivated?.(agent, config);
		console.log(`[runtime] activated as agent ${config.agentId}`);
		return json(response, 200, { activated: true, agentId: config.agentId });
	};

	return createServer(handler);
}

const SCHEMA = /^agent_[a-z0-9_]{1,56}$/;
const ID = /^[A-Za-z0-9-]{1,64}$/;
const MODEL_ENV = /^(MODEL_[A-Z_]+|MCP_SERVERS|MCP_THREADS_URL|MCP_KEY|PLATFORM_GATEWAY_URL|PLATFORM_KEY|TOOL_APPROVALS|DEMO_TOKENS_PER_SECOND)$/;

/** Validates the activation body; returns the problem as a string. */
export function parseConfig(body: unknown): AgentConfig | string {
	if (typeof body !== "object" || body === null) return "a JSON object is required";
	const b = body as Record<string, unknown>;
	const str = (k: string, max: number) => (typeof b[k] === "string" && (b[k] as string).length > 0 && (b[k] as string).length <= max ? (b[k] as string) : undefined);
	const agentId = str("agentId", 64);
	if (!agentId || !ID.test(agentId)) return "agentId is required";
	const schema = str("schema", 63);
	if (!schema || !SCHEMA.test(schema)) return "schema must be an agent schema name";
	const databaseUrl = str("databaseUrl", 2048);
	if (!databaseUrl || !/^postgres(ql)?:\/\//.test(databaseUrl)) return "databaseUrl must be a PostgreSQL URL";
	const token = str("token", 512);
	if (!token || token.length < 16) return "token is required (at least 16 characters)";
	const modelEnv: Record<string, string> = {};
	if (b.env !== undefined) {
		if (typeof b.env !== "object" || b.env === null) return "env must be an object of strings";
		for (const [k, v] of Object.entries(b.env as Record<string, unknown>)) {
			if (!MODEL_ENV.test(k) || typeof v !== "string") return `env.${k} is not allowed`;
			modelEnv[k] = v;
		}
	}
	return {
		agentId,
		schema,
		databaseUrl,
		token,
		name: str("name", 100) ?? "Assistant",
		instructions: str("instructions", 20000) ?? DEFAULT_INSTRUCTIONS,
		modelEnv,
	};
}

function json(response: ServerResponse, status: number, body: unknown): void {
	response.writeHead(status, { "content-type": "application/json", "cache-control": "no-store" }).end(JSON.stringify({ data: body }));
}

function fail(response: ServerResponse, status: number, code: string, message: string): void {
	response.writeHead(status, { "content-type": "application/json", "cache-control": "no-store" }).end(JSON.stringify({ error: { code, message } }));
}

async function readJson(request: IncomingMessage): Promise<unknown> {
	const chunks: Buffer[] = [];
	let size = 0;
	for await (const chunk of request) {
		size += (chunk as Buffer).length;
		if (size > 4 * 1024 * 1024) return undefined; // MCP_SERVERS carries the tools' schemas
		chunks.push(chunk as Buffer);
	}
	try {
		return JSON.parse(Buffer.concat(chunks).toString("utf8"));
	} catch {
		return undefined;
	}
}
