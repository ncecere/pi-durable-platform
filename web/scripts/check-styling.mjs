#!/usr/bin/env node
/*
 * Styling and behaviour policy for the Pi Durable Platform web app, adapted from bitop-ui's
 * scripts/check-styling.mjs. The UI library (src/components/ui, installed
 * from bitop-ui with the shadcn CLI) and every page follow the same rules:
 * Base UI + CSS Modules + CSS variables. Fails (exit 1) with one actionable
 * line per violation:
 *
 *  1. Banned packages: package.json must not include Tailwind, Radix,
 *     CSS-in-JS, class-name utilities or other component kits, and no
 *     source file may import them.
 *  2. CSS Modules only: every .css under src/ is a *.module.css, except the
 *     bitop-ui core and themes (src/components/ui/styles, …/themes). No
 *     Tailwind directives (@tailwind, @apply) anywhere.
 *  3. Tokens only: no literal colours (hex, rgb[a], hsl[a], hwb, lab, lch,
 *     oklab, oklch) in .ts / .tsx / .module.css. Comments are ignored.
 *     Exception: colour *data* (values stored per record, such as agent
 *     accent presets) lives in `*.colors.ts` files, which may contain
 *     literal colours and nothing that renders. Inline style objects must
 *     not contain literal colours or px / numeric lengths. Every var(--x)
 *     in a .module.css must be defined by bitop-ui's tokens/themes or in
 *     some stylesheet under src/ (or have a fallback).
 *  4. Base UI for behaviour: every src/components/ui/<item> that is not on
 *     the DISPLAY_ONLY list imports @base-ui/react, directly or through
 *     another ui item. Nothing outside src/components/ui hand-rolls popups
 *     (dialog/menu/listbox/tooltip/tab roles, aria-haspopup, createPortal):
 *     use the ui components. The ui folder holds bitop-ui items only
 *     (<name>/<name>.tsx); app-specific components go in src/components
 *     or src/pages.
 *
 * Tests (src/test, *.test.ts[x]) and the generated API schema are skipped.
 *
 * Usage: node scripts/check-styling.mjs [--root <dir>]
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
const uiDir = path.join(src, "components/ui");

/** bitop-ui items with no interactive behaviour of their own (kept in sync with bitop-ui's list). */
const DISPLAY_ONLY = new Set([
  "alert",
  "badge",
  "bar-chart",
  "card",
  "description-list",
  "empty-state",
  "kbd",
  "loader",
  "page-header",
  "save-bar",
  "skeleton",
  "sparkline",
  "spinner",
  "stat-card",
  "table",
  "time",
]);
/** Folders in the ui dir that aren't components (bitop-ui core). */
const CORE_DIRS = new Set(["styles", "themes"]);

const BANNED = [
  [/^(tailwindcss|@tailwindcss\/.+|tailwind-merge|tailwind-variants|tailwindcss-animate|tw-animate-css)$/, "Tailwind"],
  [/^(@radix-ui\/.+|radix-ui)$/, "Radix (use @base-ui/react)"],
  [/^(styled-components|@emotion\/.+|@stitches\/.+|@vanilla-extract\/.+|@linaria\/.+|@pandacss\/.+|goober|styled-jsx)$/, "CSS-in-JS (use CSS Modules)"],
  [/^(clsx|classnames|class-variance-authority|cva)$/, "class-name utilities (use cx() from @/lib/bitop-utils and data attributes)"],
  [/^(@mui\/.+|@material-ui\/.+|@chakra-ui\/.+|@mantine\/.+|antd|@ant-design\/.+|@headlessui\/.+|react-aria|react-aria-components|@react-aria\/.+|@react-stately\/.+|@ark-ui\/.+)$/, "another component kit (use @base-ui/react / bitop-ui)"],
];

const errors = [];
const fail = (where, msg, fix) => errors.push(`${where}: ${msg}${fix ? `\n      fix: ${fix}` : ""}`);
const rel = (abs) => path.relative(root, abs).split(path.sep).join("/");
const bannedReason = (name) => BANNED.find(([re]) => re.test(name))?.[1];
const packageName = (spec) => (spec.startsWith("@") ? spec.split("/").slice(0, 2).join("/") : spec.split("/")[0]);

function walk(dir) {
  if (!fs.existsSync(dir)) return [];
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? walk(path.join(dir, e.name)) : [path.join(dir, e.name)]));
}

/** Removes comments but keeps line breaks, so reported line numbers stay right. */
function stripComments(text, css) {
  const blank = (m) => m.replace(/[^\n]/g, " ");
  let out = text.replace(/\/\*[\s\S]*?\*\//g, blank);
  if (!css) out = out.replace(/(^|[^:"'`\\])\/\/[^\n]*/g, (m, pre) => pre + blank(m.slice(pre.length)));
  return out;
}
const lineOf = (text, index) => text.slice(0, index).split("\n").length;

const isTest = (abs) => abs.startsWith(path.join(src, "test") + path.sep) || /\.test\.tsx?$/.test(abs);
const isGenerated = (abs) => /\.gen\.ts$/.test(abs);
const inUi = (abs) => abs.startsWith(uiDir + path.sep);
const isGlobalUiCss = (abs) => inUi(abs) && CORE_DIRS.has(path.relative(uiDir, abs).split(path.sep)[0]);
const isColourData = (abs) => /\.colors\.ts$/.test(abs);

// ---------- 1. Banned packages ----------
const pkg = JSON.parse(fs.readFileSync(path.join(root, "package.json"), "utf8"));
for (const field of ["dependencies", "devDependencies", "peerDependencies", "optionalDependencies"]) {
  for (const name of Object.keys(pkg[field] ?? {})) {
    const reason = bannedReason(name);
    if (reason) fail(`package.json ${field}`, `"${name}" is not allowed (${reason})`, `npm uninstall ${name}`);
  }
}

// ---------- 2–3. Files ----------
const COLOR = /#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch)\(/g;
const STYLE_PX = /-?\d*\.?\d+px\b/;
const STYLE_NUMERIC_LENGTH = /\b(width|height|min[A-Z]\w*|max[A-Z]\w*|top|left|right|bottom|inset\w*|margin\w*|padding\w*|gap|rowGap|columnGap|fontSize|lineHeight|borderRadius|borderWidth|outlineOffset|flexBasis)\s*:\s*-?\d/;

function styleObjects(text) {
  const found = [];
  const re = /style=\{\{|style:\s*\{/g;
  let m;
  while ((m = re.exec(text))) {
    let i = m.index + m[0].length;
    let depth = 1;
    while (i < text.length && depth > 0) {
      if (text[i] === "{") depth++;
      else if (text[i] === "}") depth--;
      i++;
    }
    found.push({ text: text.slice(m.index, i), index: m.index });
  }
  return found;
}

function checkFile(file, text, css) {
  const code = stripComments(text, css);
  if (!isColourData(file)) {
    for (const m of code.matchAll(COLOR)) {
      fail(
        `${rel(file)}:${lineOf(code, m.index)}`,
        `literal colour "${m[0].replace(/\($/, "(…)")}"`,
        "use a semantic token such as var(--color-text-muted); if it is a data value (e.g. a stored accent), move it into a *.colors.ts data file",
      );
    }
  } else if (/<[A-Za-z]|\bstyle\b|className/.test(code)) {
    fail(rel(file), "a *.colors.ts data file must only hold colour values, not JSX or styling", "move the rendering code into a .tsx file");
  }
  if (css && /@tailwind\b|@apply\b/.test(code)) fail(rel(file), "Tailwind directive (@tailwind/@apply)", "write plain CSS in the module");
  if (!css) {
    for (const { text: t, index } of styleObjects(code)) {
      const where = `${rel(file)}:${lineOf(code, index)}`;
      if (STYLE_PX.test(t)) fail(where, `inline style with a px value: ${t.replace(/\s+/g, " ").slice(0, 80)}`, "move the value into the CSS Module or a --token");
      else if (STYLE_NUMERIC_LENGTH.test(t)) fail(where, `inline style with a numeric length (React adds px): ${t.replace(/\s+/g, " ").slice(0, 80)}`, "move the value into the CSS Module, or pass it as a CSS custom property");
    }
    for (const m of text.matchAll(/(?:from|import)\s*\(?\s*["']([^"']+)["']/g)) {
      const spec = m[1];
      if (spec.startsWith(".") || spec.startsWith("@/") || spec.startsWith("/")) {
        if (/\.css$/.test(spec) && !/\.module\.css$/.test(spec) && !/components\/ui\/(styles|themes)\//.test(spec)) {
          fail(`${rel(file)}:${lineOf(text, m.index)}`, `imports global CSS "${spec}"`, "import a *.module.css instead");
        }
        continue;
      }
      const reason = bannedReason(packageName(spec));
      if (reason) fail(`${rel(file)}:${lineOf(text, m.index)}`, `imports "${spec}" (${reason})`, "use a bitop-ui component (npx shadcn@latest add @bitop/<name>) or @base-ui/react + a CSS Module");
    }
  }
}

const files = walk(src).filter((f) => !isTest(f) && !isGenerated(f));
const cssFiles = [];
for (const abs of files) {
  if (abs.endsWith(".css")) {
    if (!isGlobalUiCss(abs) && !abs.endsWith(".module.css")) {
      fail(rel(abs), "global stylesheet", `rename it to ${path.basename(abs, ".css")}.module.css and import it as a CSS Module (only bitop-ui's styles/ and themes/ are global)`);
    }
    if (!isGlobalUiCss(abs)) {
      checkFile(abs, fs.readFileSync(abs, "utf8"), true);
      cssFiles.push(abs);
    }
  } else if (/\.(tsx?|jsx?)$/.test(abs)) {
    checkFile(abs, fs.readFileSync(abs, "utf8"), false);
  }
}

// Tokens: every var(--x) must exist.
const defined = new Set();
for (const abs of walk(src).filter((f) => f.endsWith(".css"))) {
  for (const m of fs.readFileSync(abs, "utf8").matchAll(/(--[\w-]+)\s*:/g)) defined.add(m[1]);
}
// Custom properties set from TSX (style={{ "--x": … }}) count as defined.
for (const abs of files.filter((f) => /\.tsx?$/.test(f))) {
  for (const m of fs.readFileSync(abs, "utf8").matchAll(/["'](--[\w-]+)["']\s*:/g)) defined.add(m[1]);
}
const BASE_UI_VARS = /^--(anchor|available|transform-origin|collapsible|accordion|active-tab|indicator|toast|nested|popup|positioner|scroll-area|slider|field|checkbox|radio|switch|progress|meter|viewport|swipe|offset|z-index|gap|peek|height|width|tab)/;
for (const abs of cssFiles) {
  const css = stripComments(fs.readFileSync(abs, "utf8"), true);
  for (const m of css.matchAll(/var\((--[\w-]+)\s*(,?)/g)) {
    if (m[1].startsWith("--palette-") && !inUi(abs)) fail(`${rel(abs)}:${lineOf(css, m.index)}`, `reads palette primitive ${m[1]}`, "use a semantic --color-* token");
    if (!defined.has(m[1]) && m[2] !== "," && !BASE_UI_VARS.test(m[1])) {
      fail(`${rel(abs)}:${lineOf(css, m.index)}`, `undefined token ${m[1]}`, "use a token from components/ui/styles/tokens.css or themes/, or define it");
    }
  }
}

// ---------- 4. Base UI for behaviour ----------
const HANDROLLED = /role=["'{`]+(dialog|alertdialog|menu|menuitem|menubar|listbox|option|combobox|tooltip|tablist|tab|tabpanel|tree|treeitem)\b|aria-haspopup|createPortal\(/;
const INTERACTIVE = /\bon(Key(Down|Up|Press))\b|\btabIndex\b|\.focus\(|aria-haspopup|aria-expanded|aria-controls|role=["'{`]+(button|dialog|menu|listbox|option|combobox|tooltip|tab|slider|switch|checkbox|textbox)\b|<(input|select|textarea)\b|createPortal\(/;

// Portals that aren't popups: the record and form page host moves a page into
// the shell's main area (keeping its context providers). No popup roles, focus
// traps or dismissal there; the files are otherwise checked as usual.
const LAYOUT_PORTALS = new Set(["src/components/templates/takeover.tsx"]);

for (const abs of files.filter((f) => /\.tsx?$/.test(f) && !inUi(f))) {
  let code = stripComments(fs.readFileSync(abs, "utf8"), false);
  if (LAYOUT_PORTALS.has(rel(abs))) code = code.replace(/createPortal\(/g, "placeInSlot(");
  const h = code.match(HANDROLLED);
  if (h) fail(`${rel(abs)}:${lineOf(code, h.index)}`, `hand-rolled popup semantics (${h[0]})`, "use a bitop-ui component (Dialog, Menu, Popover, Select, Tabs, Tooltip…) instead");
}

const items = fs.existsSync(uiDir)
  ? fs.readdirSync(uiDir, { withFileTypes: true }).filter((e) => e.isDirectory() && !CORE_DIRS.has(e.name)).map((e) => e.name)
  : [];
const info = new Map();
for (const name of items) {
  const dir = path.join(uiDir, name);
  if (!/^[a-z][a-z0-9-]*$/.test(name) || !fs.existsSync(path.join(dir, `${name}.tsx`))) {
    fail(rel(dir), "is not a bitop-ui item (expected <name>/<name>.tsx)", "install it with `npx shadcn@latest add @bitop/<name>`, or move app-specific code to src/components or src/pages");
  }
  let base = false;
  let handrolled = null;
  let interactive = null;
  const deps = new Set();
  for (const abs of walk(dir).filter((f) => /\.tsx?$/.test(f))) {
    const code = stripComments(fs.readFileSync(abs, "utf8"), false);
    if (/["']@base-ui\/react(\/[^"']*)?["']/.test(code)) base = true;
    for (const m of code.matchAll(/["']@\/components\/ui\/([a-z-]+)\//g)) if (m[1] !== name) deps.add(m[1]);
    const h = code.match(HANDROLLED);
    if (h && !handrolled) handrolled = `${rel(abs)}:${lineOf(code, h.index)} (${h[0]})`;
    const scrollRegion = /role\s*[:=]\s*\{?\s*["']region["']/.test(code);
    const scanned = scrollRegion ? code.replace(/\btabIndex\s*[:=]\s*\{?\s*0\b/g, (m) => m.replace(/\S/g, " ")) : code;
    const i = scanned.match(INTERACTIVE);
    if (i && !interactive) interactive = `${rel(abs)}:${lineOf(code, i.index)} (${i[0]})`;
  }
  info.set(name, { base, deps, handrolled, interactive });
}
const memo = new Map();
function reachesBaseUi(name, seen = new Set()) {
  if (memo.has(name)) return memo.get(name);
  if (seen.has(name)) return false;
  seen.add(name);
  const i = info.get(name);
  const ok = Boolean(i && (i.base || [...i.deps].some((d) => reachesBaseUi(d, seen))));
  memo.set(name, ok);
  return ok;
}
for (const [name, i] of info) {
  if (DISPLAY_ONLY.has(name)) {
    if (i.interactive) fail(`ui item ${name}`, `is display-only but has interactive code at ${i.interactive}`, "reinstall it from bitop-ui (npx shadcn@latest add @bitop/" + name + " --overwrite)");
    continue;
  }
  if (!reachesBaseUi(name)) fail(`ui item ${name}`, "is not built on Base UI (no @base-ui/react import, directly or through another ui item)", "reinstall it from bitop-ui");
  if (i.handrolled && !i.base) fail(`ui item ${name}`, `has popup/disclosure semantics at ${i.handrolled} but doesn't import @base-ui/react`, "reinstall it from bitop-ui");
}

if (errors.length) {
  console.error(`Styling policy failed (${errors.length}):\n` + errors.map((e) => `  - ${e}`).join("\n"));
  console.error("\nPolicy: Base UI for behaviour, CSS Modules for styles, CSS variables for colours. See web/README.md.");
  process.exit(1);
}
console.log(`Styling policy OK: ${files.length} files, ${info.size} ui items (${[...info.keys()].filter((n) => DISPLAY_ONLY.has(n)).length} display-only).`);
