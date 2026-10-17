"use client";

import { Check, ChevronLeft, ChevronRight, Copy } from "lucide-react";
import {
  Children,
  type ComponentPropsWithRef,
  type CSSProperties,
  type ReactNode,
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { Avatar, type AvatarProps } from "@/components/ui/avatar/avatar";
import { IconButton } from "@/components/ui/button/button";
import { CopyStatus, useCopyToClipboard } from "@/components/ui/copy-button/copy-button";
import { Tooltip } from "@/components/ui/tooltip/tooltip";
import { cx, dataFlag } from "@/lib/bitop-utils";
import styles from "./message.module.css";

/*
 * Message: one turn of a chat.
 *
 *   <Message from="user"><MessageContent>Hi!</MessageContent></Message>
 *   <Message from="assistant">
 *     <MessageContent><Response>{text}</Response></MessageContent>
 *     <MessageActions>
 *       <MessageCopyAction value={text} />
 *       <MessageAction label="Good response" pressed={vote === "up"} onClick={…}><ThumbsUp aria-hidden /></MessageAction>
 *     </MessageActions>
 *   </Message>
 *
 * Each message is an <article> named "You said" / "Assistant said", so
 * screen-reader users can jump between turns. User messages are bubbles on
 * the right; assistant messages are plain full-width prose.
 */

export type MessageRole = "user" | "assistant";

const MessageContext = createContext<MessageRole>("assistant");

export type MessageProps = ComponentPropsWithRef<"article"> & {
  from: MessageRole;
  /** Accessible name of the turn. Defaults to "You said" / "Assistant said". */
  label?: string;
};

export function Message({ from, label, className, children, ...props }: MessageProps) {
  return (
    <MessageContext.Provider value={from}>
      <article
        {...props}
        aria-label={label ?? (from === "user" ? "You said" : "Assistant said")}
        data-from={from}
        className={cx(styles.message, className)}
      >
        {children}
      </article>
    </MessageContext.Provider>
  );
}

export type MessageContentProps = ComponentPropsWithRef<"div"> & {
  /** `bubble` (default for user) or `plain` (default for assistant). */
  variant?: "bubble" | "plain";
};

export function MessageContent({ variant, className, ...props }: MessageContentProps) {
  const from = useContext(MessageContext);
  return <div {...props} data-variant={variant ?? (from === "user" ? "bubble" : "plain")} className={cx(styles.content, className)} />;
}

export type MessageAvatarProps = {
  name: string;
  src?: string;
  /**
   * Tile colour behind the initials: any CSS colour, e.g. an agent's or
   * brand's accent. The initials use --color-primary-contrast, so pick a
   * colour with at least 4.5:1 against it (ColorField checks this).
   * Without it the Avatar's name-based tint is used.
   */
  color?: string;
  size?: AvatarProps["size"];
  shape?: AvatarProps["shape"];
  /** Custom content (e.g. a logo) instead of the Avatar. */
  children?: ReactNode;
  className?: string;
};

/** Decorative: the article's label already says who is speaking. Also usable on its own as an assistant's tile (directories, headers). */
export function MessageAvatar({ name, src, color, size = "sm", shape = "circle", children, className }: MessageAvatarProps) {
  return (
    <span
      aria-hidden
      className={cx(styles.avatar, className)}
      data-color={dataFlag(Boolean(color))}
      style={color ? ({ "--message-avatar-color": color } as CSSProperties) : undefined}
    >
      {children ?? <Avatar name={name} src={src} size={size} shape={shape} decorative />}
    </span>
  );
}

export type MessageActionsProps = ComponentPropsWithRef<"div"> & {
  /** Accessible name of the toolbar group. */
  label?: string;
  /**
   * `always` (default) or `hover`: fade in on hover / focus within the
   * message. Always visible on touch screens and to keyboard users.
   */
  visibility?: "always" | "hover";
};

export function MessageActions({ label = "Message actions", visibility = "always", className, ...props }: MessageActionsProps) {
  return <div {...props} role="group" aria-label={label} data-visibility={visibility} className={cx(styles.actions, className)} />;
}

export type MessageActionProps = Omit<ComponentPropsWithRef<"button">, "children" | "className"> & {
  /** Accessible name, also the tooltip text. */
  label: string;
  /** Tooltip text if different from the label. */
  tooltip?: ReactNode;
  /** Toggle state (thumbs up / down): sets aria-pressed. */
  pressed?: boolean;
  /** The icon (mark it aria-hidden). */
  children: ReactNode;
  className?: string;
};

export function MessageAction({ label, tooltip, pressed, children, className, ...props }: MessageActionProps) {
  return (
    <Tooltip content={tooltip ?? label}>
      <IconButton
        {...props}
        size="sm"
        icon={children}
        label={label}
        aria-pressed={pressed}
        data-pressed={dataFlag(pressed)}
        className={cx(styles.action, className)}
      />
    </Tooltip>
  );
}

export type MessageCopyActionProps = Omit<MessageActionProps, "children" | "label" | "onClick"> & {
  /** The text to copy (usually the raw Markdown). */
  value: string;
  label?: string;
  onCopy?: (text: string) => void;
};

/** Copy the message text; confirms with a check mark and a polite announcement. */
export function MessageCopyAction({ value, label = "Copy message", onCopy, ...props }: MessageCopyActionProps) {
  const { state, copy } = useCopyToClipboard({ onCopy });
  return (
    <>
      <MessageAction {...props} label={label} tooltip={state === "copied" ? "Copied" : label} onClick={() => copy(value)}>
        {state === "copied" ? <Check aria-hidden /> : <Copy aria-hidden />}
      </MessageAction>
      <CopyStatus state={state} label="message" />
    </>
  );
}

/* ---------- branches: alternative responses ---------- */

type BranchContext = { index: number; count: number; setIndex: (i: number) => void; setCount: (n: number) => void };
const MessageBranchContext = createContext<BranchContext | null>(null);

function useBranch() {
  const ctx = useContext(MessageBranchContext);
  if (!ctx) throw new Error("MessageBranch parts must be inside <MessageBranch>");
  return ctx;
}

export type MessageBranchProps = {
  /** Controlled branch index (0-based). */
  branch?: number;
  defaultBranch?: number;
  onBranchChange?: (index: number) => void;
  children: ReactNode;
};

/**
 * Holds alternative versions of a response (e.g. after "Regenerate").
 * If the versions shrink below the current index, the last one is shown and
 * the clamped index is reported once through onBranchChange.
 */
export function MessageBranch({ branch, defaultBranch = 0, onBranchChange, children }: MessageBranchProps) {
  const [internal, setInternal] = useState(defaultBranch);
  const [count, setCount] = useState(0);
  const requested = branch ?? internal;
  const index = count > 0 ? Math.min(Math.max(requested, 0), count - 1) : requested;
  const setIndex = useCallback(
    (i: number) => {
      if (branch === undefined) setInternal(i);
      onBranchChange?.(i);
    },
    [branch, onBranchChange],
  );
  const setIndexRef = useRef(setIndex);
  setIndexRef.current = setIndex;
  // Report a clamp once per out-of-range (index, count) pair, not on every render.
  useEffect(() => {
    if (count > 0 && requested !== index) setIndexRef.current(index);
  }, [requested, index, count]);
  return <MessageBranchContext.Provider value={{ index, count, setIndex, setCount }}>{children}</MessageBranchContext.Provider>;
}

/** Renders the current branch out of its children (one child per version). */
export function MessageBranchContent({ children }: { children: ReactNode }) {
  const { index, setCount } = useBranch();
  const items = Children.toArray(children);
  useLayoutEffect(() => setCount(items.length), [items.length, setCount]);
  return <>{items[Math.min(index, items.length - 1)]}</>;
}

export type MessageBranchSelectorProps = {
  /** What a branch is called in labels ("Previous response", "Response 2 of 3"). */
  noun?: string;
  className?: string;
};

/** ‹ 2 / 3 › — hidden when there is only one version. */
export function MessageBranchSelector({ noun = "response", className }: MessageBranchSelectorProps) {
  const { index, count, setIndex } = useBranch();
  if (count < 2) return null;
  const Noun = noun.charAt(0).toUpperCase() + noun.slice(1);
  return (
    <div role="group" aria-label={`${Noun} versions`} className={cx(styles.branch, className)}>
      <MessageAction label={`Previous ${noun}`} disabled={index === 0} onClick={() => setIndex(index - 1)}>
        <ChevronLeft aria-hidden />
      </MessageAction>
      <span className={styles.branchPage}>
        <span aria-hidden>
          {index + 1} / {count}
        </span>
        <span className="sr-only" aria-live="polite">
          {Noun} {index + 1} of {count}
        </span>
      </span>
      <MessageAction label={`Next ${noun}`} disabled={index >= count - 1} onClick={() => setIndex(index + 1)}>
        <ChevronRight aria-hidden />
      </MessageAction>
    </div>
  );
}
