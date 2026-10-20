import { describe, expect, it } from "vitest";
import { numberPlaceholders, translateSql } from "../src/lib/storage/translate.ts";

describe("translateSql", () => {
	it("numbers placeholders outside literals", () => {
		expect(numberPlaceholders("SELECT '?', \"a?\" FROM t WHERE a = ? AND b = 'it''s ?' AND c = ?")).toBe(
			"SELECT '?', \"a?\" FROM t WHERE a = $1 AND b = 'it''s ?' AND c = $2",
		);
	});

	it("rewrites INSERT OR IGNORE", () => {
		expect(translateSql("INSERT OR IGNORE INTO record_ids (id, record_type) VALUES (?, ?)")).toBe(
			"INSERT INTO record_ids (id, record_type) VALUES ($1, $2) ON CONFLICT DO NOTHING",
		);
	});

	it("rewrites DDL types, STRICT, and json_valid checks", () => {
		const sql = translateSql(`CREATE TABLE entries (
			id INTEGER PRIMARY KEY,
			record TEXT NOT NULL CHECK (json_valid(record))
		) STRICT`);
		expect(sql).toContain("id BIGINT PRIMARY KEY");
		expect(sql).not.toMatch(/json_valid|STRICT/);
	});

	it("rejects constructs it does not translate", () => {
		expect(() => translateSql("PRAGMA journal_mode = WAL")).toThrow(/Unsupported/);
		expect(() => translateSql("INSERT OR REPLACE INTO t VALUES (?)")).toThrow(/Unsupported/);
		expect(() => translateSql("SELECT json_extract(record, '$.a') FROM t")).toThrow(/Unsupported/);
	});
});
