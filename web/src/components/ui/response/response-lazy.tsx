"use client";

import { Suspense, lazy } from "react";
import type { ResponseProps } from "@/components/ui/response/response";
import { cx } from "@/lib/bitop-utils";
import styles from "./response.module.css";

/*
 * LazyResponse: <Response> with its Markdown engine (react-markdown,
 * remark-gfm, code-block, inline-citation) code-split into its own chunk and
 * loaded on first render. Same props as Response.
 *
 *   useEffect(() => preloadResponse(), []); // e.g. when the chat page mounts
 *   <LazyResponse streaming={streaming} citations={sources}>{text}</LazyResponse>
 *
 * Use it when chat is one screen of a larger app, so pages without chat
 * don't download ~40 kB (gzip) of Markdown parsing. Until the chunk arrives
 * the text shows as plain paragraphs in the same prose styles (the layout
 * doesn't jump); call preloadResponse() early to make that window tiny.
 * Import Response from ./response directly when every page renders
 * Markdown anyway, or on the server.
 */

// Relative on purpose: both files sit in the same folder in every install.
const load = () => import("./response");

const Markdown = lazy(() => load().then((m) => ({ default: m.Response })));

function Fallback({ children, className, streaming, id, ...rest }: ResponseProps) {
  const blocks = children.split(/\n{2,}/).filter((b) => b.trim());
  return (
    <div
      id={id}
      aria-label={rest["aria-label"]}
      aria-labelledby={rest["aria-labelledby"]}
      className={cx(styles.response, styles.fallback, className)}
      data-streaming={streaming ? "" : undefined}
    >
      {blocks.map((b, i) => (
        <p key={i}>{b}</p>
      ))}
    </div>
  );
}

/** Response, loaded on demand. */
export function LazyResponse(props: ResponseProps) {
  return (
    <Suspense fallback={<Fallback {...props} />}>
      <Markdown {...props} />
    </Suspense>
  );
}

/** Starts downloading the Markdown engine without rendering anything. Safe to call repeatedly. */
export function preloadResponse(): void {
  void load();
}
