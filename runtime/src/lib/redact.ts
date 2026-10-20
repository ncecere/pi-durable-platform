/**
 * A bounded, redacted view of a tool call's arguments, for the people who decide an approval (docs/approvals.md). The
 * platform applies the same rules again (internal/agents/redact.go, which must agree): the runtime is not trusted to.
 *
 * - A value is redacted when its key clearly names a credential (`isSecretKey`) or when it looks like one
 *   (`isSecretValue`), and reads `[redacted, N characters]` (N: the string's length in characters, or its JSON's for
 *   another value). A value already redacted so is kept.
 * - Strings are cut at 2,000 characters, arrays at 100 items, objects at 100 keys, nesting at 6 levels.
 * - A view whose JSON is still over 16 KiB is replaced by `{"[truncated]": "<the first 2,000 characters>"}`.
 */
const MAX_STRING = 2000;
const MAX_ITEMS = 100;
const MAX_DEPTH = 6;
export const MAX_ARGUMENTS_BYTES = 16 * 1024;

/**
 * Keys (lower case, letters and digits only) that name a credential, whole or as their end. Names that only might
 * (`key`, `auth`, `headers`, `signature`) are left to the value checks: an S3 object's key is not a secret.
 */
const SECRET_EXACT = new Set(["authorization", "proxyauthorization", "cookie", "setcookie", "credentials", "credential", "bearer", "sessionid", "xapikey"]);
const SECRET_SUFFIX = ["password", "passwd", "passphrase", "secret", "apikey", "secretkey", "privatekey", "secretaccesskey", "token"];
/** Names ending in "token" that are not credentials (pagination). */
const NOT_SECRET = new Set(["pagetoken", "nextpagetoken", "prevpagetoken", "previouspagetoken", "continuationtoken", "synctoken", "nexttoken", "cursortoken"]);

/**
 * Values that look like credentials: known token prefixes (the platform's own, OpenAI and Stripe, GitHub, GitLab, Slack,
 * AWS key IDs, Google API keys, Hugging Face, npm), an Authorization header's value, a JWT, a PEM private key, and a long
 * random-looking string (32-512 characters of base64 or base64url with upper and lower case letters and digits, and at
 * least 4 bits of entropy per character).
 */
const SECRET_PREFIX = /^(?:pdp[a-z]{0,3}[_.]|sk-|sk_(?:live|test)_|rk_(?:live|test)_|gh[pousr]_|github_pat_|glpat-|xox[abeprs]-|AKIA|ASIA|AIza|hf_|npm_)[A-Za-z0-9_.-]{16,}$/;
const AUTH_VALUE = /^(?:bearer|basic|token)\s+\S{8,}$/i;
const JWT = /^eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*$/;
const RANDOM = /^[A-Za-z0-9+/=_-]{32,512}$/;
const REDACTED_MARK = /^\[redacted(?:, [0-9]{1,9} characters?)?\]$/;

/** Whether a key names a credential: `Authorization`, `api_key`, `clientSecret`, `refresh-token`, `password`, ... */
export function isSecretKey(key: string): boolean {
	const k = key.toLowerCase().replace(/[^a-z0-9]/g, "");
	if (SECRET_EXACT.has(k)) return true;
	if (NOT_SECRET.has(k)) return false;
	return SECRET_SUFFIX.some((s) => k.endsWith(s));
}

/** Whether a string looks like a credential. Conservative: identifiers, paths, one-case hashes, UUIDs and prose show. */
export function isSecretValue(value: string): boolean {
	const v = value.trim();
	if (SECRET_PREFIX.test(v) || AUTH_VALUE.test(v) || JWT.test(v)) return true;
	if (v.includes("-----BEGIN") && v.includes("PRIVATE KEY")) return true;
	return RANDOM.test(v) && /[A-Z]/.test(v) && /[a-z]/.test(v) && /[0-9]/.test(v) && entropy(v) >= 4;
}

/** A string's Shannon entropy in bits per character. */
function entropy(s: string): number {
	const chars = [...s];
	const counts = new Map<string, number>();
	for (const c of chars) counts.set(c, (counts.get(c) ?? 0) + 1);
	let h = 0;
	for (const n of counts.values()) {
		const p = n / chars.length;
		h -= p * Math.log2(p);
	}
	return h;
}

/** What a redacted value reads: its length in characters (a string's, or its JSON's), never any of it. */
export function redactedMark(value: unknown): string {
	if (typeof value === "string" && REDACTED_MARK.test(value)) return value;
	const n = typeof value === "string" ? [...value].length : [...(JSON.stringify(value) ?? "")].length;
	return n === 1 ? "[redacted, 1 character]" : `[redacted, ${n} characters]`;
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
	if (typeof value === "string") {
		if (isSecretValue(value)) return redactedMark(value);
		return value.length > MAX_STRING ? `${value.slice(0, MAX_STRING)}…` : value;
	}
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
			out[name] = isSecretKey(key) ? redactedMark(item) : redact(item, depth + 1);
		}
		if (entries.length > MAX_ITEMS) out["[…]"] = `${entries.length - MAX_ITEMS} more keys`;
		return out;
	}
	return null;
}
