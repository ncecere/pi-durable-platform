"use client";

import { type ComponentPropsWithRef, type ReactNode, memo, useDeferredValue, useEffect, useMemo, useRef, useState } from "react";
import Markdown, { type Components, type ExtraProps } from "react-markdown";
import remarkGfm from "remark-gfm";
import { CodeBlock, type CodeBlockHighlighter } from "@/components/ui/code-block/code-block";
import { type CitationSource, InlineCitation } from "@/components/ui/inline-citation/inline-citation";
import { closeMarkdown } from "@/components/ui/response/close-markdown";
import { TextLink } from "@/components/ui/text-link/text-link";
import { cx } from "@/lib/bitop-utils";
import styles from "./response.module.css";

/*
 * Response: renders an assistant's Markdown answer (react-markdown +
 * remark-gfm: tables, task lists, strikethrough, autolinks) and is safe to
 * re-render on every streamed token:
 *
 *   - `streaming` runs closeMarkdown() so half-written syntax (an open code
 *     fence, **bold, [link](http…) renders as it will when complete, and
 *     defers the render (useDeferredValue) so typing stays responsive.
 *   - Raw HTML is never rendered: it shows as text (or is dropped with
 *     `skipHtml`). react-markdown's default URL filter strips javascript:
 *     and other unsafe protocols. The one exception: a bare <br>, <br/> or
 *     <br /> inside a table cell becomes a line break (GFM cells can't hold
 *     a newline, so models write <br>); no other tag or attribute is read.
 *   - Task-list items say "Done:" / "To do:" instead of rendering an
 *     unlabelled disabled checkbox.
 *   - Links to other sites open in a new tab (rel="noreferrer noopener",
 *     announced as such); code blocks use CodeBlock; wide tables scroll.
 *   - Markdown headings are demoted (# → h3 by default) so an answer never
 *     competes with the page's own h1/h2. Quoted text whose own outline
 *     doesn't fit the page's (a document's passage) can render every heading
 *     at one level (`headingLevel`), so no level is skipped.
 *   - A table header cell with no text is a plain cell, and a header row with
 *     no text at all is left out (documents converted from other formats
 *     often have one), so every header names its column.
 *   - With `citations`, markers like [1] or [1, 3] become InlineCitation
 *     chips for those (1-based) sources. Markers inside code (inline or
 *     block) or links, brackets attached to an identifier (a[3], m[i][2],
 *     [3]int) or starting a link ([1](url)), and numbers with no matching
 *     source, stay as text: the same rules as the server's
 *     (internal/agents/markers.go).
 *   - Images: an answer (e.g. one steered by a prompt-injected document)
 *     must not make the browser fetch arbitrary URLs, which could track the
 *     reader or leak data in the query string. So by default
 *     (`images="click"`) only same-origin, relative, data: and blob: images
 *     load; any other image is a "Load image from example.com" button (with
 *     its alt text) that loads it on request. `images="show"` loads every
 *     image; `images="alt"` never loads any and shows "[Image: alt]" text.
 *
 * To keep react-markdown out of your main bundle, render LazyResponse from
 * ./response-lazy instead: same props, the Markdown engine loads on first
 * use (see that file).
 */

export type ResponseProps = Omit<ComponentPropsWithRef<"div">, "children"> & {
  /** The Markdown text (partial while streaming). */
  children: string;
  /** Set while tokens are still arriving. */
  streaming?: boolean;
  /** Override or add element renderers (react-markdown `components`). */
  components?: Components;
  /** Sources for [n] citation markers (1-based: [1] is citations[0]). */
  citations?: CitationSource[];
  /** Custom citation renderer; receives the marker's numbers. Enables marker parsing on its own. */
  renderCitation?: (indices: number[]) => ReactNode;
  /** Syntax highlighter for fenced code (see CodeBlock). */
  highlight?: CodeBlockHighlighter;
  /** Heading levels to add: 2 renders "#" as h3 (default). */
  headingOffset?: number;
  /**
   * Render every Markdown heading at this level (1–6) instead, keeping its
   * size: for quoted text (a document's passage) whose outline isn't the
   * page's, so headings never skip a level. Overrides `headingOffset`.
   */
  headingLevel?: number;
  /** Drop raw HTML instead of showing it as text. */
  skipHtml?: boolean;
  /** Extra remark plugins, after remark-gfm. */
  remarkPlugins?: NonNullable<Parameters<typeof Markdown>[0]["remarkPlugins"]>;
  /**
   * How Markdown images load.
   * - `click` (default): same-origin, relative, data: and blob: images load;
   *   images from other origins show a "Load image from <host>" button and
   *   are fetched only when it's pressed. Model-written Markdown can point at
   *   any URL, and fetching it can track the reader or leak data in the URL.
   * - `show`: load every image (lazy-loaded). Only for trusted content.
   * - `alt`: never load images; show "[Image: alt]" text instead.
   */
  images?: ResponseImages;
};

export type ResponseImages = "click" | "show" | "alt";

/* ---------- [n] citation markers: a tiny remark plugin (no unist deps) ---------- */

type MdNode = { type: string; value?: string; children?: MdNode[]; data?: Record<string, unknown> };

const MARKER = /\[(\d{1,3}(?:\s*,\s*\d{1,3})*)\]/g;
const IDENT = /[A-Za-z0-9_]/;
const SKIP = new Set(["link", "linkReference", "inlineCode", "code", "definition", "html", "image", "imageReference"]);

/**
 * Whether the marker at value[start, end) is attached to an identifier or a
 * link rather than being a citation: a[3], arr_2[0], m[i][2] (after a "]"
 * that doesn't end a marker), [3]int, [1](url).
 */
function attached(value: string, start: number, end: number, lastEnd: number): boolean {
  const before = value[start - 1] ?? "";
  const after = value[end] ?? "";
  return IDENT.test(before) || (before === "]" && lastEnd !== start) || IDENT.test(after) || after === "(";
}

function splitMarkers(value: string): MdNode[] | null {
  const out: MdNode[] = [];
  let last = 0;
  let lastEnd = -1; // end of the last marker
  for (const m of value.matchAll(MARKER)) {
    if (attached(value, m.index, m.index + m[0].length, lastEnd)) continue;
    lastEnd = m.index + m[0].length;
    if (m.index > last) out.push({ type: "text", value: value.slice(last, m.index) });
    const numbers = m[1]!.split(/\s*,\s*/).join(",");
    out.push({
      type: "citationMarker",
      data: { hName: "sup", hProperties: { dataCitation: numbers } },
      children: [{ type: "text", value: m[0] }],
    });
    last = m.index + m[0].length;
  }
  if (!out.length) return null;
  if (last < value.length) out.push({ type: "text", value: value.slice(last) });
  return out;
}

function visit(node: MdNode) {
  if (!node.children || SKIP.has(node.type)) return;
  // Brackets that don't form a link can arrive as separate text nodes: merge them first.
  const merged: MdNode[] = [];
  for (const child of node.children) {
    const prev = merged[merged.length - 1];
    if (child.type === "text" && prev?.type === "text") prev.value = (prev.value ?? "") + (child.value ?? "");
    else merged.push(child.type === "text" ? { ...child } : child);
  }
  node.children = merged.flatMap((child) => {
    if (child.type === "text") return splitMarkers(child.value ?? "") ?? [child];
    visit(child);
    return [child];
  });
}

export function remarkCitationMarkers() {
  return (tree: MdNode) => visit(tree);
}

/* ---------- <br> in table cells ---------- */

/** Exactly one bare br tag, nothing else (no attributes). */
const BR = /^<br\s*\/?>$/i;

function breakCells(node: MdNode, inCell: boolean) {
  if (!node.children) return;
  const cell = inCell || node.type === "tableCell";
  node.children = node.children.map((child) => {
    if (cell && child.type === "html" && BR.test((child.value ?? "").trim())) return { type: "break" };
    breakCells(child, cell);
    return child;
  });
}

/**
 * Turns a literal <br> (<br/>, <br />) inside a GFM table cell into a line
 * break. Only that exact tag, only in table cells: all other raw HTML still
 * shows as text (or is dropped with `skipHtml`). Always on in Response.
 */
export function remarkTableCellBreaks() {
  return (tree: MdNode) => breakCells(tree, false);
}

/* ---------- element renderers ---------- */

type HastNode = { type: string; value?: string; tagName?: string; properties?: Record<string, unknown>; children?: HastNode[] };

function hastText(node: HastNode | undefined): string {
  if (!node) return "";
  if (node.type === "text") return node.value ?? "";
  return (node.children ?? []).map(hastText).join("");
}

const isExternal = (href: string | undefined) => !!href && /^(https?:)?\/\//i.test(href);

/**
 * The host an image would be fetched from, or null when it is safe to load
 * without asking: same-origin, relative, data: and blob: URLs. Returns ""
 * for a cross-origin URL whose host can't be read.
 */
export function crossOriginImageHost(src: string | undefined): string | null {
  if (!src || /^(data|blob):/i.test(src)) return null;
  const page = typeof window !== "undefined" ? window.location : undefined;
  try {
    if (!page) {
      // Server render: relative URLs are same-origin, absolute ones are not.
      if (src.startsWith("//")) return new URL(`https:${src}`).host;
      return new URL(src).host; // throws for relative URLs
    }
    const url = new URL(src, page.href);
    if (url.protocol === "data:" || url.protocol === "blob:" || url.origin === page.origin) return null;
    return url.host;
  } catch {
    return page ? "" : null;
  }
}

type ImgProps = ComponentPropsWithRef<"img">;

/** A cross-origin image behind a button: fetched only when the reader asks. */
function ClickToLoadImage({ host, alt, ...props }: ImgProps & { host: string }) {
  const [loaded, setLoaded] = useState(false);
  const frame = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    // The button is replaced by the image: keep focus there, not on <body>.
    if (loaded) frame.current?.focus();
  }, [loaded]);
  if (loaded) {
    return (
      <span ref={frame} tabIndex={-1} className={styles.imageFrame}>
        <img {...props} alt={alt ?? ""} className={styles.image} />
      </span>
    );
  }
  return (
    <button
      type="button"
      className={styles.imageLoad}
      onClick={() => setLoaded(true)}
    >
      <span>{host ? `Load image from ${host}` : "Load image from another site"}</span>
      {alt && (
        <>
          <span className="sr-only">: </span>
          <span className={styles.imageLoadAlt}>{alt}</span>
        </>
      )}
    </button>
  );
}

function makeComponents(
  headingOffset: number,
  highlight: CodeBlockHighlighter | undefined,
  cite: ((indices: number[]) => ReactNode) | undefined,
  images: ResponseImages,
  headingLevel?: number,
): Components {
  const heading = (level: number) => {
    const at = headingLevel ? Math.min(6, Math.max(1, Math.round(headingLevel))) : Math.min(6, level + headingOffset);
    const Tag = `h${at}` as "h3";
    return function Heading({ node: _node, ...props }: ComponentPropsWithRef<"h3"> & ExtraProps) {
      return <Tag {...props} data-level={level} className={styles.heading} />;
    };
  };
  return {
    h1: heading(1),
    h2: heading(2),
    h3: heading(3),
    h4: heading(4),
    h5: heading(5),
    h6: heading(6),
    a({ node: _node, href, children, ...props }) {
      return (
        <TextLink {...props} href={href} external={isExternal(href)}>
          {children}
        </TextLink>
      );
    },
    pre({ node }) {
      const code = node?.children?.find((c) => c.type === "element" && c.tagName === "code") as HastNode | undefined;
      const classes = code?.properties?.className;
      const lang = (Array.isArray(classes) ? classes : [])
        .map(String)
        .find((c) => c.startsWith("language-"))
        ?.slice("language-".length);
      return <CodeBlock code={hastText(code).replace(/\n$/, "")} language={lang} highlight={highlight} className={styles.codeBlock} />;
    },
    code({ node: _node, className, ...props }) {
      return <code {...props} className={cx(styles.inlineCode, className)} />;
    },
    table({ node: _node, ...props }) {
      return (
        // Wide tables scroll; a scroll container must be keyboard-focusable (WCAG 2.1.1).
        <div className={styles.tableWrap} tabIndex={0}>
          <table {...props} className={styles.table} />
        </div>
      );
    },
    // An empty header row says nothing (axe: empty-table-header); an empty header cell is a plain cell.
    thead({ node, ...props }) {
      if (node && !hastText(node as HastNode).trim()) return null;
      return <thead {...props} />;
    },
    th({ node, ...props }) {
      if (node && !hastText(node as HastNode).trim()) return <td {...(props as ComponentPropsWithRef<"td">)} />;
      return <th {...props} />;
    },
    input({ node: _node, type, checked, ...props }) {
      // GFM task-list boxes are read-only: show a mark and say the state in words
      // instead of an unlabelled, disabled checkbox.
      if (type !== "checkbox") return <input {...props} type={type} checked={checked} />;
      return (
        <>
          <span aria-hidden className={styles.taskBox} data-checked={checked ? "" : undefined} />
          <span className="sr-only">{checked ? "Done: " : "To do: "}</span>
        </>
      );
    },
    img({ node: _node, alt, ...props }) {
      if (images === "alt") return <span className={styles.imageAlt}>{alt ? `[Image: ${alt}]` : "[Image]"}</span>;
      if (images === "click") {
        const host = crossOriginImageHost(typeof props.src === "string" ? props.src : undefined);
        if (host !== null) return <ClickToLoadImage {...props} alt={alt} host={host} />;
      }
      return <img {...props} alt={alt ?? ""} loading="lazy" className={styles.image} />;
    },
    sup({ node: _node, children, ...props }) {
      const raw = (props as Record<string, unknown>)["data-citation"];
      if (cite && typeof raw === "string") {
        const indices = raw.split(",").map(Number);
        const rendered = cite(indices);
        if (rendered !== null && rendered !== undefined) return <>{rendered}</>;
        return <>{children}</>; // no matching source: keep the marker as plain text
      }
      return <sup {...props}>{children}</sup>;
    },
  };
}

function ResponseImpl({
  children,
  streaming = false,
  components,
  citations,
  renderCitation,
  highlight,
  headingOffset = 2,
  headingLevel,
  skipHtml = false,
  remarkPlugins,
  images = "click",
  className,
  ...props
}: ResponseProps) {
  // While streaming, let React skip intermediate tokens under load.
  const deferred = useDeferredValue(children);
  const source = streaming ? closeMarkdown(deferred) : children;

  const cite = useMemo(() => {
    if (renderCitation) return renderCitation;
    if (!citations?.length) return undefined;
    return (indices: number[]) => {
      const valid = indices.filter((n) => n >= 1 && n <= citations.length);
      if (valid.length !== indices.length) return null; // unknown number: keep the text
      return <InlineCitation index={valid} sources={valid.map((n) => citations[n - 1]!)} />;
    };
  }, [citations, renderCitation]);

  const merged = useMemo(
    () => ({ ...makeComponents(headingOffset, highlight, cite, images, headingLevel), ...components }),
    [headingOffset, headingLevel, highlight, cite, images, components],
  );
  const plugins = useMemo(
    () => [remarkGfm, remarkTableCellBreaks, ...(cite ? [remarkCitationMarkers] : []), ...(remarkPlugins ?? [])],
    [cite, remarkPlugins],
  );

  return (
    <div {...props} className={cx(styles.response, className)} data-streaming={streaming ? "" : undefined}>
      <Markdown remarkPlugins={plugins} components={merged} skipHtml={skipHtml}>
        {source}
      </Markdown>
    </div>
  );
}

/** Memoised: re-renders only when its text or options change. */
export const Response = memo(ResponseImpl);
