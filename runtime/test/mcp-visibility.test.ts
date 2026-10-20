import { describe, expect, it } from "vitest";
import { modelVisible } from "../src/mcp.ts";

// The same cases as internal/mcp's TestListToolsLeavesOutAppOnlyTools: both hosts apply one rule.
describe("modelVisible (MCP Apps tool visibility)", () => {
	it("offers tools without UI metadata, or with visibility that includes the model", () => {
		expect(modelVisible(undefined)).toBe(true);
		expect(modelVisible({ ui: { resourceUri: "ui://app/view.html", visibility: ["model", "app"] } })).toBe(true);
		expect(modelVisible({ ui: { visibility: ["model"] } })).toBe(true);
	});
	it("keeps app-only tools from the model", () => {
		expect(modelVisible({ ui: { visibility: ["app"] } })).toBe(false);
		expect(modelVisible({ ui: { visibility: [] } })).toBe(false);
	});
	it("treats metadata it doesn't understand as the default (visible)", () => {
		expect(modelVisible({ ui: "not an object" })).toBe(true);
		expect(modelVisible({ "vendor/x": 1 })).toBe(true);
		expect(modelVisible({ ui: { visibility: "app" } })).toBe(true);
	});
});
