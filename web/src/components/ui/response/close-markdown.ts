/*
 * closeMarkdown(): make a partial (still streaming) Markdown string render
 * the way it will once complete, instead of flickering through raw syntax.
 *
 *   - an unterminated ``` / ~~~ fence is closed (so the rest renders as code)
 *   - an unterminated `inline code` span is closed
 *   - open **strong**, *em*, _em_, __strong__ and ~~strike~~ are closed
 *     (innermost first); an opener with nothing after it yet is dropped
 *   - an incomplete link "[text](https://exa" shows just "text" (never a link
 *     to a truncated URL); an incomplete image is hidden; a dangling "[" is
 *     dropped, and a half-typed citation marker "[1" is hidden
 *
 * Only the last block (after the last blank line / closing fence) is
 * touched: inline syntax can't span blocks. It is a display-only transform;
 * never store its output. Pure and dependency-free.
 */

type Opener = { char: string; len: number; pos: number };

type Scan = {
  /** Unclosed inline code span: opener position and backtick count. */
  code: { pos: number; len: number } | null;
  /** Unclosed emphasis delimiters, bottom → top. */
  emphasis: Opener[];
  /** An incomplete link/image: "[text](partial" (inUrl) or "[text" (no "]"). */
  link: { start: number; image: boolean; textEnd: number; inUrl: boolean } | null;
};

const FENCE = /^ {0,3}(`{3,}|~{3,})(.*)$/;
const isWs = (c: string) => c === "" || /\s/.test(c);
const isPunct = (c: string) => c !== "" && /[!-/:-@[-`{-~\p{P}\p{S}]/u.test(c);

function scan(text: string): Scan {
  const emphasis: Opener[] = [];
  const brackets: { pos: number; image: boolean }[] = [];
  let code: Scan["code"] = null;
  let url: { start: number; image: boolean; textEnd: number; depth: number } | null = null;
  let i = 0;
  while (i < text.length) {
    const c = text[i]!;
    if (code) {
      if (c === "`") {
        let n = 0;
        while (text[i + n] === "`") n++;
        if (n === code.len) code = null;
        i += n;
      } else i++;
      continue;
    }
    if (c === "\\") {
      i += 2;
      continue;
    }
    if (url) {
      if (c === "(") url.depth++;
      else if (c === ")" && --url.depth === 0) url = null;
      else if (c === "\n" || c === " ") {
        // A space or newline inside the destination means it wasn't a link after all.
        if (!/^\s*["'(]/.test(text.slice(i))) url = null;
      }
      i++;
      continue;
    }
    if (c === "`") {
      let n = 0;
      while (text[i + n] === "`") n++;
      code = { pos: i, len: n };
      i += n;
      continue;
    }
    if (c === "[" || (c === "!" && text[i + 1] === "[")) {
      const image = c === "!";
      brackets.push({ pos: i, image });
      i += image ? 2 : 1;
      continue;
    }
    if (c === "]") {
      const open = brackets.pop();
      if (open && text[i + 1] === "(") {
        url = { start: open.pos, image: open.image, textEnd: i, depth: 1 };
        i += 2;
        continue;
      }
      i++;
      continue;
    }
    if (c === "*" || c === "_" || c === "~") {
      let n = 0;
      while (text[i + n] === c) n++;
      const prev = i > 0 ? text[i - 1]! : "";
      const next = text[i + n] ?? "";
      const left = !isWs(next) && !(isPunct(next) && !isWs(prev) && !isPunct(prev));
      const right = !isWs(prev) && !(isPunct(prev) && !isWs(next) && !isPunct(next));
      const canOpen = c === "_" ? left && (!right || isPunct(prev)) : left;
      const canClose = c === "_" ? right && (!left || isPunct(next)) : right;
      let remaining = c === "~" ? (n >= 2 ? 2 : 1) : n;
      const top = emphasis[emphasis.length - 1];
      if (canClose && top && top.char === c) {
        while (remaining > 0 && emphasis.length && emphasis[emphasis.length - 1]!.char === c) {
          const t = emphasis[emphasis.length - 1]!;
          const use = Math.min(t.len, remaining);
          t.len -= use;
          remaining -= use;
          if (t.len === 0) emphasis.pop();
        }
      }
      if (remaining > 0 && canOpen) {
        if (c === "*" && remaining === 3) {
          emphasis.push({ char: c, len: 2, pos: i }, { char: c, len: 1, pos: i + 2 });
        } else emphasis.push({ char: c, len: remaining, pos: i + (n - remaining) });
      }
      i += n;
      continue;
    }
    i++;
  }
  let link: Scan["link"] = null;
  if (url) link = { start: url.start, image: url.image, textEnd: url.textEnd, inUrl: true };
  else if (brackets.length) {
    const b = brackets[brackets.length - 1]!;
    link = { start: b.pos, image: b.image, textEnd: text.length, inUrl: false };
  }
  return { code, emphasis, link };
}

/** Closes the inline syntax of the last paragraph. */
function closeInline(input: string): string {
  // A delimiter run at the very end after whitespace is an opener still waiting for text.
  let text = input.replace(/(^|\s)[*_~]+$/, "$1");
  for (let pass = 0; pass < 4; pass++) {
    const { link } = scan(text);
    if (!link) break;
    const open = link.image ? 2 : 1;
    if (link.inUrl) {
      // "[label](https://partial" → "label"; images are hidden until complete.
      text = text.slice(0, link.start) + (link.image ? "" : text.slice(link.start + open, link.textEnd));
    } else if (/^\[[\d,\s]*$/.test(text.slice(link.start))) {
      // A half-typed citation marker such as "[1" or "[1, 2".
      text = text.slice(0, link.start);
    } else {
      text = text.slice(0, link.start) + text.slice(link.start + open);
    }
  }

  const { code, emphasis } = scan(text);
  const closers: string[] = [];
  if (code) {
    if (text.slice(code.pos + code.len).trim() === "") text = text.slice(0, code.pos);
    else closers.push("`".repeat(code.len));
  }
  for (let k = emphasis.length - 1; k >= 0; k--) {
    const o = emphasis[k]!;
    if (code && o.pos > code.pos) continue;
    const after = text.slice(o.pos + o.len);
    if (closers.length === 0 && after.trim() === "") {
      text = text.slice(0, o.pos) + after;
    } else closers.push(o.char.repeat(o.len));
  }
  if (closers.length === 0) return text;
  return text.replace(/\s+$/, "") + closers.join("");
}

export function closeMarkdown(markdown: string): string {
  if (!markdown) return markdown;
  const lines = markdown.split("\n");
  let fence: { char: string; len: number } | null = null;
  let tailStart = 0; // line index where the last block starts
  for (let n = 0; n < lines.length; n++) {
    const line = lines[n]!;
    const m = line.match(FENCE);
    if (fence) {
      if (m && m[1]![0] === fence.char && m[1]!.length >= fence.len && m[2]!.trim() === "") {
        fence = null;
        tailStart = n + 1;
      }
      continue;
    }
    if (m && !(m[1]![0] === "`" && m[2]!.includes("`"))) {
      fence = { char: m[1]![0]!, len: m[1]!.length };
      continue;
    }
    if (line.trim() === "") tailStart = n + 1;
  }
  if (fence) {
    return `${markdown}${markdown.endsWith("\n") ? "" : "\n"}${fence.char.repeat(fence.len)}`;
  }
  const head = lines.slice(0, tailStart).join("\n");
  const tail = lines.slice(tailStart).join("\n");
  if (!tail.trim()) return markdown;
  const closed = closeInline(tail);
  return tailStart === 0 ? closed : `${head}\n${closed}`;
}
