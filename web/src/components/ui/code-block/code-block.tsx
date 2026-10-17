"use client";

import type { ComponentPropsWithRef, CSSProperties, ReactNode } from "react";
import { CopyButton } from "@/components/ui/copy-button/copy-button";
import { cx, dataFlag } from "@/lib/bitop-utils";
import styles from "./code-block.module.css";

/*
 * CodeBlock: a block of code with a header (language / filename, actions and
 * a copy button), optional line numbers and pluggable syntax highlighting.
 *
 * No highlighter is bundled. Pass `highlight={(code, lang) => node}` to plug
 * one in (shiki's codeToHast + hast-util-to-jsx-runtime, Prism, lowlight…);
 * it must return inline content for the <code> element. The copy button
 * always copies the raw `code`.
 *
 * The <pre> scrolls horizontally, so it is keyboard-focusable (WCAG 2.1.1).
 */

export type CodeBlockHighlighter = (code: string, language: string | undefined) => ReactNode;

export type CodeBlockProps = Omit<ComponentPropsWithRef<"div">, "children"> & {
  code: string;
  /** Language id shown in the header and passed to `highlight` (e.g. "tsx", "bash"). */
  language?: string;
  /** Shown instead of the language, e.g. "src/app.tsx". */
  filename?: ReactNode;
  showLineNumbers?: boolean;
  /** Syntax highlighter; without it the code is plain text. */
  highlight?: CodeBlockHighlighter;
  /** Extra header actions, before the copy button. */
  actions?: ReactNode;
  /** Show the copy button (default true). */
  copyable?: boolean;
  /** Wrap long lines instead of scrolling (disables line numbers alignment). */
  wrap?: boolean;
  /** Cap the height (any CSS length); the block scrolls vertically beyond it. */
  maxHeight?: string;
  /** Hide the header (the copy button then floats in the corner). */
  hideHeader?: boolean;
};

/** Human label for common language ids. */
export function languageLabel(language: string | undefined): string {
  if (!language) return "Code";
  const map: Record<string, string> = {
    js: "JavaScript",
    jsx: "JSX",
    ts: "TypeScript",
    tsx: "TSX",
    py: "Python",
    python: "Python",
    sh: "Shell",
    bash: "Bash",
    zsh: "Shell",
    shell: "Shell",
    json: "JSON",
    yaml: "YAML",
    yml: "YAML",
    md: "Markdown",
    markdown: "Markdown",
    html: "HTML",
    css: "CSS",
    sql: "SQL",
    go: "Go",
    rust: "Rust",
    rs: "Rust",
    text: "Text",
    plaintext: "Text",
  };
  return map[language.toLowerCase()] ?? language;
}

export function CodeBlock({
  code,
  language,
  filename,
  showLineNumbers = false,
  highlight,
  actions,
  copyable = true,
  wrap = false,
  maxHeight,
  hideHeader = false,
  className,
  style,
  ...props
}: CodeBlockProps) {
  const lines = code.replace(/\n$/, "").split("\n");
  const numbered = showLineNumbers && !wrap;
  const label = languageLabel(language);
  const copy = copyable && <CopyButton value={code} label={language ? `${label} code` : "code"} className={styles.copy} />;
  return (
    <div
      {...props}
      className={cx(styles.root, className)}
      data-language={language}
      data-wrap={dataFlag(wrap)}
      style={maxHeight ? ({ "--code-max-height": maxHeight, ...style } as CSSProperties) : style}
    >
      {!hideHeader && (
        <div className={styles.header}>
          <span className={styles.language}>{filename ?? label}</span>
          <span className={styles.actions}>
            {actions}
            {copy}
          </span>
        </div>
      )}
      <div className={styles.body}>
        {numbered && (
          <span aria-hidden className={styles.gutter}>
            {lines.map((_, i) => `${i + 1}\n`).join("")}
          </span>
        )}
        <pre tabIndex={0} className={styles.pre}>
          <code className={language ? `language-${language}` : undefined}>{highlight ? highlight(code, language) : code}</code>
        </pre>
        {hideHeader && <span className={styles.floating}>{copy}</span>}
      </div>
    </div>
  );
}
