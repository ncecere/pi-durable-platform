import { timingSafeEqual } from "node:crypto";
import type { RequestListener } from "node:http";
import { CONTRACT_VERSION, PI_DURABLE_VERSION } from "./version.ts";

/**
 * The upgrade check (docs/runtime-contract.md, Starting). pi-durable migrates a document when it reads it, and code
 * that finds a document written by a newer version refuses it, so an agent whose storage a newer pi-durable has
 * written can't run on an older one. Before it opens the agent's storage, the runtime tells the platform its versions
 * (`POST {PLATFORM_GATEWAY_URL}/runtime/start`); the platform records the newest pi-durable version that has written
 * each agent's storage and answers 409 `runtime_too_old` to an older one. The runtime then never opens the storage: it
 * serves the refusal (refusedHandler) until the platform stops it.
 */
export class RuntimeRefusedError extends Error {
	constructor(
		readonly code: string,
		message: string,
	) {
		super(message);
		this.name = "RuntimeRefusedError";
	}
}

type Fetch = typeof fetch;

/**
 * Asks the platform whether this runtime may open the agent's storage. Resolves when it may (or when the platform
 * can't be asked: no gateway configured, a platform without the check, or no answer after a few tries; the platform
 * checks again at `/status` after the start); throws RuntimeRefusedError when the platform refuses.
 */
export async function checkUpgrade(
	env: Readonly<Record<string, string | undefined>>,
	log: (...args: unknown[]) => void,
	options: { fetch?: Fetch; retryDelayMs?: number } = {},
): Promise<void> {
	const base = env.PLATFORM_GATEWAY_URL?.replace(/\/$/, "");
	const key = env.PLATFORM_KEY;
	if (!base || !key) return;
	const doFetch = options.fetch ?? fetch;
	const delay = options.retryDelayMs ?? 500;
	for (let attempt = 1; attempt <= 3; attempt++) {
		try {
			const res = await doFetch(`${base}/runtime/start`, {
				method: "POST",
				headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
				body: JSON.stringify({ contractVersion: CONTRACT_VERSION, piDurableVersion: PI_DURABLE_VERSION }),
				signal: AbortSignal.timeout(10_000),
			});
			if (res.ok || res.status === 404) return; // 404: a platform that predates the check
			if (res.status === 409) {
				const body = (await res.json().catch(() => ({}))) as { error?: { code?: unknown; message?: unknown } };
				if (body.error?.code === "runtime_too_old") {
					const message = typeof body.error.message === "string" ? clean(body.error.message) : "";
					throw new RuntimeRefusedError("runtime_too_old", message || "The platform refuses to start this agent on this runtime: it is older than the one that last wrote its data");
				}
				return; // another conflict (the agent was turned off): not this check's business
			}
			if (res.status < 500) return;
		} catch (error) {
			if (error instanceof RuntimeRefusedError) throw error;
		}
		if (attempt < 3) await new Promise((r) => setTimeout(r, delay * attempt));
	}
	log("could not report this runtime's versions to the platform; starting anyway (the platform checks them after the start)");
}

/** Control characters out, at most 500 characters: the message is served back to the platform. */
function clean(text: string): string {
	return text.replace(/[\u0000-\u001f\u007f]+/g, " ").trim().slice(0, 500);
}

/**
 * The HTTP API of a runtime the platform refused: `/healthz` answers (so launchers see it up and the platform can ask
 * why), `/status` reports an idle runtime with its versions and `refused`, and every other route answers
 * `409 runtime_too_old` with the platform's reason. The agent's storage is never opened.
 */
export function refusedHandler(info: { agentId: string; name: string; token: string }, refusal: RuntimeRefusedError): RequestListener {
	const expected = Buffer.from(`Bearer ${info.token}`);
	const startedAt = Date.now();
	return (request, response) => {
		const send = (status: number, body: unknown) =>
			response.writeHead(status, { "content-type": "application/json", "cache-control": "no-store" }).end(JSON.stringify(body));
		const url = new URL(request.url ?? "/", "http://runtime");
		if (request.method === "GET" && url.pathname === "/healthz") return send(200, { data: { ok: true } });
		const header = Buffer.from(request.headers.authorization ?? "");
		if (header.length !== expected.length || !timingSafeEqual(header, expected)) return send(401, { error: { code: "unauthorized", message: "Unauthorized" } });
		if (request.method === "GET" && url.pathname === "/status") {
			return send(200, {
				data: {
					agentId: info.agentId,
					name: info.name,
					model: "",
					busyThreads: 0,
					openStreams: 0,
					lastActivityAt: startedAt,
					contractVersion: CONTRACT_VERSION,
					piDurableVersion: PI_DURABLE_VERSION,
					refused: { code: refusal.code, message: refusal.message },
				},
			});
		}
		return send(409, { error: { code: refusal.code, message: refusal.message } });
	};
}
