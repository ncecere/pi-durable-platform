/* TEST_TIME_SCALE (test-scale.ts): one setting that stretches every test timeout on a slower or shared machine, 1 (nothing changes) by default. */
import { getConfig } from "@testing-library/react";
import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { parseTimeScale, scaled } from "../../test-scale";

describe("parseTimeScale", () => {
  it("is 1 when unset or empty: CI is unchanged", () => {
    expect(parseTimeScale(undefined)).toBe(1);
    expect(parseTimeScale("")).toBe(1);
  });

  it("takes a number from 1 to 10", () => {
    expect(parseTimeScale("3")).toBe(3);
    expect(parseTimeScale("1.5")).toBe(1.5);
    expect(parseTimeScale("10")).toBe(10);
  });

  it("falls back to 1 for anything that would shorten the limits or make no sense", () => {
    for (const bad of ["0", "0.5", "-2", "abc", "NaN", "Infinity", "11", "100", "3x"]) expect(parseTimeScale(bad), bad).toBe(1);
  });
});

describe("scaled", () => {
  it("multiplies a limit and keeps it a whole number of milliseconds", () => {
    expect(scaled(20_000, "3")).toBe(60_000);
    expect(scaled(5000, "1.5")).toBe(7500);
    expect(scaled(1000, "1.0004")).toBe(1000);
    expect(scaled(20_000, "")).toBe(20_000); // an empty setting is no setting
  });
});

describe("the limits this run is actually using", () => {
  const scale = parseTimeScale(process.env.TEST_TIME_SCALE);

  it("findBy and waitFor wait 5 s times the scale", () => {
    expect(getConfig().asyncUtilTimeout).toBe(5000 * scale);
  });

  it("a test may take 20 s times the scale", ({ task }) => {
    expect(task.timeout).toBe(20_000 * scale);
  });
});

describe("no test sets a limit of its own that the scale would not reach", () => {
  const dir = path.dirname(new URL(import.meta.url).pathname);
  const files = fs.readdirSync(dir, { recursive: true, encoding: "utf8" }).filter((f) => /\.tsx?$/.test(f) && !f.endsWith("time-scale.test.ts"));

  it("every explicit timeout and per-test limit goes through scaled()", () => {
    const bare: string[] = [];
    for (const f of files) {
      fs.readFileSync(path.join(dir, f), "utf8").split("\n").forEach((line, i) => {
        // `{ timeout: 4000 }` for a findBy/waitFor, and `}, 15_000);` closing an it(...): both would ignore TEST_TIME_SCALE
        if (/\btimeout: ?\d/.test(line) || /^\s*\}, ?[1-9][\d_]*\);/.test(line)) bare.push(`${f}:${i + 1}: ${line.trim().slice(0, 80)}`);
      });
    }
    expect(bare, "wrap these limits in scaled(...) from test-scale.ts").toEqual([]);
  });
});
