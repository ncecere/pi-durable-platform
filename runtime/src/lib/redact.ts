/**
 * A bounded, redacted view of a tool call's arguments, for the people who decide an approval (docs/approvals.md). The
 * platform applies the same rules again (internal/agents/redact.go): the runtime is not trusted to.
 *
 * - Values under keys that look like credentials or headers read "[redacted]".
 * - Strings are cut at 2,000 characters, arrays at 100 items, objects at 100 keys, nesting at 6 levels.
 * - A view whose JSON is still over 16 KiB is replaced by `{"[truncated]": "<the first 2,000 characters>"}`.
 */
export const REDACTED = "[redacted]";
const MAX_STRING = 2000;
const MAX_ITEMS = 100;
const MAX_DEPTH = 6;
export const MAX_ARGUMENTS_BYTES = 16 * 1024;

/** Keys whose values are never shown: credentials, cookies, and headers (which carry them). */
const SECRET_CONTAINS = ["authorization", "cookie", "password", "passwd", "passphrase", "credential", "bearer", "privatekey"];
const SECRET_SUFFIX = ["token", "secret", "apikey", "accesskey", "secretkey", "signature", "sessionid"];
const SECRET_EXACT = new Set(["headers", "header", "key", "auth", "xapikey"]);

/** Whether a key names a secret: `Authorization`, `api_key`, `clientSecret`, `refresh-token`, `headers`, ... */
export function isSecretKey(key: string): boolean {
	const k = key.toLowerCase().replace(/[^a-z0-9]/g, "");
	return SECRET_EXACT.has(k) || SECRET_CONTAINS.some((s) => k.includes(s)) || SECRET_SUFFIX.some((s) => k.endsWith(s));
}

export function redactArguments(value: unknown): unknown {
	const view = redact(value, 0);
	const json = JSON.stringify(view) ?? "null";
	if (Buffer.byteLength(json, "utf8") <= MAX_ARGUMENTS_BYTES) return view;
	return { "[truncated]": json.slice(0, MAX_STRING) };
}

function redact(value: unknown, depth: number): unknown {
	if (value === null || typeof value === "boolean") return value;
	if (typeof value === "number") return Number.isFinite(value) ? value : null;
	if (typeof value === "string") return value.length > MAX_STRING ? `${value.slice(0, MAX_STRING)}…` : value;
	if (depth >= MAX_DEPTH) return "[…]";
	if (Array.isArray(value)) {
		const items = value.slice(0, MAX_ITEMS).map((item) => redact(item, depth + 1));
		if (value.length > MAX_ITEMS) items.push(`[… ${value.length - MAX_ITEMS} more]`);
		return items;
	}
	if (typeof value === "object") {
		const out: Record<string, unknown> = {};
		const entries = Object.entries(value as Record<string, unknown>);
		for (const [key, item] of entries.slice(0, MAX_ITEMS)) {
			const name = key.length > 200 ? `${key.slice(0, 200)}…` : key;
			out[name] = isSecretKey(key) ? REDACTED : redact(item, depth + 1);
		}
		if (entries.length > MAX_ITEMS) out["[…]"] = `${entries.length - MAX_ITEMS} more keys`;
		return out;
	}
	return null;
}
