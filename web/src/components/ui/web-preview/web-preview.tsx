"use client";

import { Collapsible } from "@base-ui/react/collapsible";
import { ArrowLeft, ArrowRight, ChevronDown, ExternalLink, Globe, RotateCw } from "lucide-react";
import {
  type ComponentPropsWithRef,
  type FormEvent,
  type ReactNode,
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
} from "react";
import { IconButton, type IconButtonProps } from "@/components/ui/button/button";
import { Input } from "@/components/ui/input/input";
import { Spinner } from "@/components/ui/spinner/spinner";
import { Tooltip } from "@/components/ui/tooltip/tooltip";
import { cx } from "@/lib/bitop-utils";
import styles from "./web-preview.module.css";

/*
 * WebPreview: an <iframe> preview of a generated app or page, with a
 * browser-like navigation bar (back, forward, reload, address field, open in
 * a new tab) and an optional console panel (Base UI Collapsible).
 *
 *   <WebPreview defaultUrl="https://preview.example.dev">
 *     <WebPreviewNavigation>
 *       <WebPreviewBack />
 *       <WebPreviewForward />
 *       <WebPreviewReload />
 *       <WebPreviewUrl />
 *       <WebPreviewOpen />
 *     </WebPreviewNavigation>
 *     <WebPreviewBody title="Preview of the generated landing page" />
 *     <WebPreviewConsole logs={logs} />
 *   </WebPreview>
 *
 * The frame is sandboxed by default ("allow-scripts allow-forms": no
 * same-origin access, no top navigation, no popups); widen `sandbox` only
 * for content you trust. Back/forward walk the addresses visited through
 * this component (a cross-origin frame's own history isn't readable).
 */

export const DEFAULT_WEB_PREVIEW_SANDBOX = "allow-scripts allow-forms";

type WebPreviewContextValue = {
  url: string;
  navigate: (url: string) => void;
  back: () => void;
  forward: () => void;
  reload: () => void;
  canGoBack: boolean;
  canGoForward: boolean;
  reloadKey: number;
  loading: boolean;
  setLoading: (loading: boolean) => void;
};

const WebPreviewContext = createContext<WebPreviewContextValue | null>(null);

/** The preview's navigation state and actions, for custom controls. */
export function useWebPreview(): WebPreviewContextValue {
  const ctx = useContext(WebPreviewContext);
  if (!ctx) throw new Error("WebPreview parts must be rendered inside <WebPreview>.");
  return ctx;
}

/**
 * Adds https:// to bare hosts ("example.com"; http:// for localhost), keeps
 * absolute and relative URLs, and refuses javascript: URLs (returns "").
 */
export function normalizeUrl(input: string): string {
  const value = input.trim();
  if (!value || /^javascript:/i.test(value)) return "";
  if (/^[a-z][a-z\d+.-]*:\/\//i.test(value) || /^(about|data|blob):/i.test(value)) return value;
  if (value.startsWith("/") || value.startsWith(".") || value.startsWith("?") || value.startsWith("#")) return value;
  if (/^(localhost|127\.0\.0\.1|\[::1\])(:\d+)?(\/|$)/i.test(value)) return `http://${value}`;
  return `https://${value}`;
}

export type WebPreviewProps = ComponentPropsWithRef<"div"> & {
  /** Current address (controlled). Changing it navigates. */
  url?: string;
  defaultUrl?: string;
  /** Called when the address changes through the bar, back/forward or `navigate`. */
  onUrlChange?: (url: string) => void;
};

export function WebPreview({ url: urlProp, defaultUrl = "", onUrlChange, className, children, ...props }: WebPreviewProps) {
  const initial = urlProp ?? defaultUrl;
  const [history, setHistory] = useState<{ entries: string[]; index: number }>({ entries: initial ? [initial] : [], index: initial ? 0 : -1 });
  const [reloadKey, setReloadKey] = useState(0);
  const [loading, setLoading] = useState(Boolean(initial));
  const current = history.entries[history.index] ?? "";

  // A new controlled `url` is a navigation (derived-state pattern).
  const [prevProp, setPrevProp] = useState(urlProp);
  if (urlProp !== prevProp) {
    setPrevProp(urlProp);
    if (urlProp !== undefined && urlProp !== current) {
      setHistory((h) => ({ entries: [...h.entries.slice(0, h.index + 1), urlProp], index: h.index + 1 }));
      setLoading(Boolean(urlProp));
    }
  }

  const navigate = useCallback(
    (next: string) => {
      const target = normalizeUrl(next);
      if (!target) return;
      setHistory((h) => ({ entries: [...h.entries.slice(0, h.index + 1), target], index: h.index + 1 }));
      setLoading(true);
      if (target === current) setReloadKey((k) => k + 1);
      onUrlChange?.(target);
    },
    [current, onUrlChange],
  );

  const go = useCallback(
    (delta: number) => {
      const index = history.index + delta;
      const target = history.entries[index];
      if (target === undefined) return;
      setHistory((h) => ({ ...h, index }));
      setLoading(true);
      onUrlChange?.(target);
    },
    [history, onUrlChange],
  );

  const value = useMemo<WebPreviewContextValue>(
    () => ({
      url: current,
      navigate,
      back: () => go(-1),
      forward: () => go(1),
      reload: () => {
        if (!current) return;
        setLoading(true);
        setReloadKey((k) => k + 1);
      },
      canGoBack: history.index > 0,
      canGoForward: history.index < history.entries.length - 1,
      reloadKey,
      loading,
      setLoading,
    }),
    [current, navigate, go, history, reloadKey, loading],
  );

  return (
    <WebPreviewContext.Provider value={value}>
      <div {...props} className={cx(styles.root, className)}>
        {children}
      </div>
    </WebPreviewContext.Provider>
  );
}

export type WebPreviewNavigationProps = ComponentPropsWithRef<"div"> & {
  /** Accessible name of the control group (default "Preview navigation"). */
  label?: string;
};

export function WebPreviewNavigation({ label = "Preview navigation", className, ...props }: WebPreviewNavigationProps) {
  return <div role="group" aria-label={label} {...props} className={cx(styles.navigation, className)} />;
}

export type WebPreviewNavigationButtonProps = IconButtonProps & {
  /** Tooltip text (default: the label). */
  tooltip?: ReactNode;
};

/** An icon button for the navigation bar; `label` is its accessible name. */
export function WebPreviewNavigationButton({ tooltip, label, size = "sm", ...props }: WebPreviewNavigationButtonProps) {
  return (
    <Tooltip content={tooltip ?? label}>
      <IconButton {...props} size={size} label={label} />
    </Tooltip>
  );
}

type PresetButtonProps = Omit<WebPreviewNavigationButtonProps, "icon" | "label"> & { label?: string; icon?: ReactNode };

export function WebPreviewBack({ label = "Back", icon, ...props }: PresetButtonProps) {
  const { back, canGoBack } = useWebPreview();
  return <WebPreviewNavigationButton {...props} label={label} icon={icon ?? <ArrowLeft aria-hidden />} disabled={!canGoBack || props.disabled} onClick={back} />;
}

export function WebPreviewForward({ label = "Forward", icon, ...props }: PresetButtonProps) {
  const { forward, canGoForward } = useWebPreview();
  return (
    <WebPreviewNavigationButton {...props} label={label} icon={icon ?? <ArrowRight aria-hidden />} disabled={!canGoForward || props.disabled} onClick={forward} />
  );
}

export function WebPreviewReload({ label = "Reload", icon, ...props }: PresetButtonProps) {
  const { reload, url } = useWebPreview();
  return <WebPreviewNavigationButton {...props} label={label} icon={icon ?? <RotateCw aria-hidden />} disabled={!url || props.disabled} onClick={reload} />;
}

/** Opens the current address in a new tab (a link). */
export function WebPreviewOpen({ label = "Open in new tab", icon, ...props }: PresetButtonProps) {
  const { url } = useWebPreview();
  if (!url) return null;
  return (
    <WebPreviewNavigationButton
      {...props}
      label={label}
      icon={icon ?? <ExternalLink aria-hidden />}
      render={<a href={url} target="_blank" rel="noopener noreferrer" />}
    />
  );
}

export type WebPreviewUrlProps = {
  /** Accessible name of the address field (default "Address"). */
  label?: string;
  placeholder?: string;
  className?: string;
};

/** The address field. Enter navigates; bare hosts get https://. */
export function WebPreviewUrl({ label = "Address", placeholder = "Enter a URL", className }: WebPreviewUrlProps) {
  const { url, navigate } = useWebPreview();
  const [draft, setDraft] = useState(url);
  const [prevUrl, setPrevUrl] = useState(url);
  if (url !== prevUrl) {
    setPrevUrl(url);
    setDraft(url);
  }
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    navigate(draft);
  }
  return (
    <form onSubmit={submit} className={cx(styles.urlForm, className)}>
      <Input
        size="sm"
        type="text"
        inputMode="url"
        autoComplete="off"
        spellCheck={false}
        aria-label={label}
        startIcon={<Globe />}
        placeholder={placeholder}
        value={draft}
        onValueChange={setDraft}
        onFocus={(e) => e.currentTarget.select()}
        className={styles.url}
      />
    </form>
  );
}

export type WebPreviewBodyProps = Omit<ComponentPropsWithRef<"iframe">, "title"> & {
  /** Describes the frame's content for assistive technology (required). */
  title: string;
  /** Custom loading indicator shown over the frame while it loads. */
  loading?: ReactNode;
  /** Shown when there is no address yet. */
  emptyText?: ReactNode;
  /** Class for the wrapper; `className` goes on the iframe. */
  frameClassName?: string;
};

export function WebPreviewBody({
  title,
  loading: loadingNode,
  emptyText = "Enter an address to load the preview.",
  sandbox = DEFAULT_WEB_PREVIEW_SANDBOX,
  src,
  className,
  frameClassName,
  onLoad,
  ...props
}: WebPreviewBodyProps) {
  const { url, reloadKey, loading, setLoading } = useWebPreview();
  const address = src ?? url;
  const hasContent = Boolean(address || props.srcDoc);
  return (
    <div className={cx(styles.body, frameClassName)} aria-busy={(hasContent && loading) || undefined}>
      {hasContent ? (
        <>
          <iframe
            key={`${address}#${reloadKey}`}
            referrerPolicy="no-referrer"
            {...props}
            title={title}
            src={address || undefined}
            sandbox={sandbox}
            className={cx(styles.frame, className)}
            onLoad={(e) => {
              setLoading(false);
              onLoad?.(e);
            }}
          />
          {loading && address && (
            <div className={styles.loading}>{loadingNode ?? <Spinner size="lg" label="Loading preview" />}</div>
          )}
        </>
      ) : (
        <p className={styles.empty}>{emptyText}</p>
      )}
    </div>
  );
}

export type WebPreviewLog = {
  level: "log" | "info" | "warn" | "error";
  message: string;
  timestamp?: Date;
};

export type WebPreviewConsoleProps = {
  logs?: WebPreviewLog[];
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Trigger text (default "Console"). */
  label?: ReactNode;
  emptyText?: ReactNode;
  /** Extra content after the logs. */
  children?: ReactNode;
  className?: string;
};

const levelLabel: Record<WebPreviewLog["level"], string> = { log: "Log", info: "Info", warn: "Warning", error: "Error" };

export function WebPreviewConsole({ logs = [], open, defaultOpen, onOpenChange, label = "Console", emptyText = "No console output.", children, className }: WebPreviewConsoleProps) {
  const errors = logs.filter((l) => l.level === "error").length;
  const warnings = logs.filter((l) => l.level === "warn").length;
  const counts = [errors && `${errors} ${errors === 1 ? "error" : "errors"}`, warnings && `${warnings} ${warnings === 1 ? "warning" : "warnings"}`].filter(Boolean).join(", ");
  return (
    <Collapsible.Root open={open} defaultOpen={defaultOpen} onOpenChange={onOpenChange ? (o) => onOpenChange(o) : undefined} className={cx(styles.console, className)}>
      <Collapsible.Trigger className={styles.consoleTrigger}>
        <ChevronDown aria-hidden className={styles.chevron} />
        <span>{label}</span>
        {counts && <span className={styles.counts}>{counts}</span>}
      </Collapsible.Trigger>
      <Collapsible.Panel className={styles.consolePanel}>
        <div className={styles.consoleBody} tabIndex={0} role="region" aria-label="Console output">
          {logs.length === 0 && !children ? (
            <p className={styles.empty}>{emptyText}</p>
          ) : (
            <ol className={styles.logs}>
              {logs.map((log, i) => (
                <li key={i} className={styles.log} data-level={log.level}>
                  <span className={styles.level}>{levelLabel[log.level]}</span>
                  {log.timestamp && (
                    <time className={styles.timestamp} dateTime={log.timestamp.toISOString()}>
                      {log.timestamp.toLocaleTimeString()}
                    </time>
                  )}
                  <span className={styles.message}>{log.message}</span>
                </li>
              ))}
            </ol>
          )}
          {children}
        </div>
      </Collapsible.Panel>
    </Collapsible.Root>
  );
}
