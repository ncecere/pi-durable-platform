/**
 * Translates the SQL that pi-durable's portable `SqliteStorage` issues into PostgreSQL.
 *
 * `SqliteStorage` keeps all storage semantics (ID ownership, fork ancestry, document deltas) and talks to a small
 * `SqliteDatabase` facade. Its SQL is plain enough that a few mechanical rewrites cover it:
 *
 * - `?` placeholders become `$1, $2, …` (outside string literals).
 * - `INSERT OR IGNORE INTO …` becomes `INSERT INTO … ON CONFLICT DO NOTHING`.
 * - DDL: `INTEGER` becomes `BIGINT` (IDs reach `Number.MAX_SAFE_INTEGER`), the `STRICT` table option is dropped, and
 *   `CHECK (json_valid(col))` is dropped. Pi deliberately stores JSON with `\u0000` and lone surrogate escapes, which
 *   PostgreSQL's `jsonb` rejects; the columns stay `TEXT` exactly as in SQLite.
 *
 * Anything else SQLite-specific is rejected loudly, so an upstream change cannot silently produce different behavior.
 * The pi-durable version is pinned for that reason; the storage conformance suite guards each upgrade.
 */

const UNSUPPORTED = [/\bPRAGMA\b/i, /\bGLOB\b/i, /\blast_insert_rowid\b/i, /\bchanges\(\)/i, /\bjson_\w+\(/i];

export function translateSql(sql: string): string {
	let out = sql;
	const isDdl = /^\s*CREATE\s+(TABLE|INDEX)\b/i.test(out);
	if (isDdl) {
		out = out
			.replace(/\s+CHECK\s*\(\s*json_valid\(\s*\w+\s*\)\s*\)/gi, "")
			.replace(/\)\s*STRICT\s*$/i, ")")
			.replace(/\bINTEGER\b/g, "BIGINT");
	}
	if (/^\s*INSERT\s+OR\s+IGNORE\s+INTO\b/i.test(out)) {
		out = `${out.replace(/^(\s*)INSERT\s+OR\s+IGNORE\s+INTO\b/i, "$1INSERT INTO").trimEnd()} ON CONFLICT DO NOTHING`;
	} else if (/\bINSERT\s+OR\s+\w+/i.test(out)) {
		throw new Error("Unsupported SQLite statement for PostgreSQL storage (INSERT OR …)");
	}
	for (const pattern of UNSUPPORTED) {
		if (pattern.test(stripLiterals(out))) throw new Error(`Unsupported SQLite construct for PostgreSQL storage: ${pattern}`);
	}
	return numberPlaceholders(out);
}

/** Replace `?` with `$n`, skipping single-quoted literals and double-quoted identifiers. */
export function numberPlaceholders(sql: string): string {
	let out = "";
	let n = 0;
	let quote: "'" | '"' | undefined;
	for (let i = 0; i < sql.length; i++) {
		const ch = sql[i]!;
		if (quote !== undefined) {
			out += ch;
			if (ch === quote) {
				if (sql[i + 1] === quote) out += sql[++i]!; // escaped quote
				else quote = undefined;
			}
			continue;
		}
		if (ch === "'" || ch === '"') {
			quote = ch;
			out += ch;
		} else if (ch === "?") {
			out += `$${++n}`;
		} else {
			out += ch;
		}
	}
	if (quote !== undefined) throw new Error("Unterminated quoted string in SQL");
	return out;
}

function stripLiterals(sql: string): string {
	return sql.replace(/'(?:[^']|'')*'/g, "''");
}
