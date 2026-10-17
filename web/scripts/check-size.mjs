#!/usr/bin/env node
/*
 * File size guard for the Pi Durable Platform web app ("no hell files"). Fails (exit 1),
 * one line per file, when a source file under src/ is longer than:
 *
 *   - 400 lines for source files (.ts, .tsx, .css), and
 *   - 700 lines for tests (src/test/**, *.test.ts[x]).
 *
 * Skipped: the bitop-ui components (src/components/ui/**, managed by the
 * shadcn CLI) and the generated API types (src/api/schema.gen.ts).
 * Aim lower when you write code: about 350 lines per file and 120 per
 * component; split by feature (see README.md).
 *
 * Usage: node scripts/check-size.mjs [--root <dir>]
 */
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const argRoot = process.argv.indexOf("--root");
const root =
  argRoot !== -1 && process.argv[argRoot + 1]
    ? path.resolve(process.argv[argRoot + 1])
    : path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const src = path.join(root, "src");

const MAX_SOURCE = 400;
const MAX_TEST = 700;
const SKIP = [/^components\/ui\//, /^api\/schema\.gen\.ts$/];
const isTest = (rel) => rel.startsWith("test/") || /\.test\.tsx?$/.test(rel);

function walk(dir, acc = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, acc);
    else if (/\.(tsx?|css)$/.test(e.name)) acc.push(p);
  }
  return acc;
}

const errors = [];
let checked = 0;
for (const file of walk(src)) {
  const rel = path.relative(src, file).split(path.sep).join("/");
  if (SKIP.some((re) => re.test(rel))) continue;
  checked++;
  const text = fs.readFileSync(file, "utf8");
  const lines = text.endsWith("\n") ? text.split("\n").length - 1 : text.split("\n").length;
  const max = isTest(rel) ? MAX_TEST : MAX_SOURCE;
  if (lines > max) {
    errors.push(`src/${rel}: ${lines} lines (limit ${max}${isTest(rel) ? " for tests" : ""}). Split it by feature: components, hooks and helpers in their own files.`);
  }
}

if (errors.length) {
  for (const e of errors) console.error(e);
  console.error(`\nFile size check failed: ${errors.length} file(s) too long.`);
  process.exit(1);
}
console.log(`File size OK: ${checked} files (source ≤ ${MAX_SOURCE} lines, tests ≤ ${MAX_TEST}).`);
