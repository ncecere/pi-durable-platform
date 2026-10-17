"use client";

import { ArrowDown } from "lucide-react";
import {
  type ComponentPropsWithRef,
  type ReactNode,
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { IconButton } from "@/components/ui/button/button";
import { Tooltip } from "@/components/ui/tooltip/tooltip";
import { cx } from "@/lib/bitop-utils";
import styles from "./conversation.module.css";

/*
 * Conversation: the scrolling message list of a chat.
 *
 *   <Conversation>
 *     <ConversationContent>{messages}</ConversationContent>
 *     <ConversationScrollButton />
 *   </Conversation>
 *   <ConversationAnnouncer status={status} />
 *
 * Stick to bottom: while the user is at the bottom, growing content (a
 * streamed answer, an image loading) keeps the view pinned to the latest
 * message. Scrolling up (wheel, touch, keyboard, scrollbar) releases it and
 * shows "Scroll to latest"; reaching the bottom again re-pins it.
 * `scrollToElement(el)` (from useConversation) releases it too, to show a
 * message from its start (an answer that arrived whole).
 * `scrollToBottom("smooth")` follows content that grows during the next
 * 600 ms smoothly too (a row added under the last message glides into view
 * instead of jumping). No
 * dependency: a ResizeObserver + MutationObserver on the content and the
 * pure `nextStickState()` below.
 *
 * Screen readers: the viewport is role="log" but aria-live="off" by default,
 * because a polite log would read every streamed token. Render one
 * <ConversationAnnouncer status={…} /> (a polite status region) to announce
 * "Assistant is responding" / "Response complete" instead; users then read
 * the answer at their own pace. The viewport is focusable so keyboard users
 * can scroll it with the arrow keys.
 */

/* ---------- pure stick-to-bottom logic ---------- */

export type ScrollMetrics = { scrollTop: number; scrollHeight: number; clientHeight: number };

export type StickState = {
  /** Follow new content. */
  stuck: boolean;
  /** Within `threshold` px of the bottom. */
  atBottom: boolean;
  lastScrollTop: number;
  lastScrollHeight: number;
};

export const initialStickState: StickState = { stuck: true, atBottom: true, lastScrollTop: 0, lastScrollHeight: 0 };

export function distanceFromBottom({ scrollTop, scrollHeight, clientHeight }: ScrollMetrics): number {
  return Math.max(0, scrollHeight - clientHeight - scrollTop);
}

/**
 * Reduces a scroll event. Reaching the bottom (re)sticks. Moving up while the
 * content height is unchanged, or right after a wheel / touch / key gesture
 * (`userIntent`), is a user scroll and unsticks. Moving up because content
 * shrank (a panel collapsed) keeps the current stickiness.
 */
export function nextStickState(prev: StickState, m: ScrollMetrics, threshold = 64, userIntent = false): StickState {
  const atBottom = distanceFromBottom(m) <= threshold;
  let stuck = prev.stuck;
  const movedUp = m.scrollTop < prev.lastScrollTop - 1;
  if (atBottom) stuck = true;
  else if (movedUp && (userIntent || m.scrollHeight === prev.lastScrollHeight)) stuck = false;
  return { stuck, atBottom, lastScrollTop: m.scrollTop, lastScrollHeight: m.scrollHeight };
}

const prefersReducedMotion = () => typeof window !== "undefined" && !!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;

export type UseStickToBottomOptions = {
  /** Distance from the bottom (px) that still counts as "at the bottom". */
  threshold?: number;
  /**
   * Follow the content to the bottom (default true). Pass false while an
   * empty state shows (a welcome taller than the panel stays scrolled to its
   * top); turning it on pins the view to the bottom once.
   */
  enabled?: boolean;
};

/** The stick-to-bottom behaviour on its own, for custom layouts. */
export function useStickToBottom({ threshold = 64, enabled = true }: UseStickToBottomOptions = {}) {
  const [viewport, setViewport] = useState<HTMLElement | null>(null);
  const [content, setContent] = useState<HTMLElement | null>(null);
  const state = useRef<StickState>(initialStickState);
  const intentUntil = useRef(0);
  // After scrollToElement: until then, nothing re-pins (a scroll event from an earlier pin, still at the bottom, arrives
  // while a smooth scroll is only starting). A pin (scrollToBottom, reaching the bottom by hand later) ends it.
  const holdUntil = useRef(0);
  // After scrollToBottom("smooth"): until then, content that grows (a row added at the end) is followed smoothly too,
  // instead of a jump that would cut the smooth scroll short.
  const smoothUntil = useRef(0);
  const [atBottom, setAtBottom] = useState(true);

  const measure = useCallback((el: HTMLElement): ScrollMetrics => ({ scrollTop: el.scrollTop, scrollHeight: el.scrollHeight, clientHeight: el.clientHeight }), []);

  const pin = useCallback(
    (el: HTMLElement, behavior: ScrollBehavior = "auto") => {
      const top = el.scrollHeight;
      if (behavior === "smooth" && !prefersReducedMotion() && typeof el.scrollTo === "function") el.scrollTo({ top, behavior });
      else el.scrollTop = top;
      state.current = { ...state.current, stuck: true };
      holdUntil.current = 0;
    },
    [],
  );

  const sync = useCallback(
    (el: HTMLElement) => {
      state.current = nextStickState(state.current, measure(el), threshold, Date.now() < intentUntil.current);
      if (Date.now() < holdUntil.current) state.current = { ...state.current, stuck: false };
      setAtBottom(state.current.atBottom);
    },
    [measure, threshold],
  );

  const scrollToBottom = useCallback(
    (behavior: ScrollBehavior = "smooth") => {
      if (!viewport) return;
      pin(viewport, behavior);
      smoothUntil.current = behavior === "smooth" ? Date.now() + 600 : 0;
      sync(viewport);
    },
    [viewport, pin, sync],
  );

  /**
   * Stop following and bring `el` (inside the viewport) to `offset` px below the viewport's top: for a message that
   * should be read from its start, such as an answer that arrived whole. Following resumes when the user reaches the
   * bottom again. Content that keeps growing meanwhile doesn't pull the view back down.
   */
  const scrollToElement = useCallback(
    (el: HTMLElement, { offset = 0, behavior = "smooth" }: { offset?: number; behavior?: ScrollBehavior } = {}) => {
      if (!viewport) return;
      state.current = { ...state.current, stuck: false };
      intentUntil.current = Date.now() + 600;
      holdUntil.current = Date.now() + 800;
      const top = Math.max(0, viewport.scrollTop + el.getBoundingClientRect().top - viewport.getBoundingClientRect().top - offset);
      if (behavior === "smooth" && !prefersReducedMotion() && typeof viewport.scrollTo === "function") viewport.scrollTo({ top, behavior });
      else viewport.scrollTop = top;
    },
    [viewport],
  );

  // Scroll / wheel / touch: detect the user leaving the bottom.
  useEffect(() => {
    if (!viewport) return;
    state.current = { ...state.current, lastScrollTop: viewport.scrollTop, lastScrollHeight: viewport.scrollHeight };
    const onScroll = () => sync(viewport);
    // A gesture towards the top: the next upward scroll is the user's. If the
    // view can already move up, stop following right away so growth can't
    // pull it back down mid-gesture.
    const release = () => {
      intentUntil.current = Date.now() + 300;
      if (viewport.scrollTop > 0) state.current = { ...state.current, stuck: false };
    };
    const onWheel = (e: WheelEvent) => e.deltaY < 0 && release();
    let touchY = 0;
    const onTouchStart = (e: TouchEvent) => (touchY = e.touches[0]?.clientY ?? 0);
    const onTouchMove = (e: TouchEvent) => (e.touches[0]?.clientY ?? 0) > touchY && release();
    const upKeys = new Set(["ArrowUp", "PageUp", "Home"]);
    const onKey = (e: KeyboardEvent) => e.target === viewport && upKeys.has(e.key) && release();
    viewport.addEventListener("scroll", onScroll, { passive: true });
    viewport.addEventListener("keydown", onKey);
    viewport.addEventListener("wheel", onWheel, { passive: true });
    viewport.addEventListener("touchstart", onTouchStart, { passive: true });
    viewport.addEventListener("touchmove", onTouchMove, { passive: true });
    return () => {
      viewport.removeEventListener("scroll", onScroll);
      viewport.removeEventListener("keydown", onKey);
      viewport.removeEventListener("wheel", onWheel);
      viewport.removeEventListener("touchstart", onTouchStart);
      viewport.removeEventListener("touchmove", onTouchMove);
    };
  }, [viewport, sync]);

  // Content growth: follow it while stuck (and enabled).
  useLayoutEffect(() => {
    if (!viewport || !content) return;
    if (!enabled) {
      viewport.scrollTop = 0;
      state.current = { ...state.current, stuck: true };
    }
    const onChange = () => {
      if (enabled && state.current.stuck) pin(viewport, Date.now() < smoothUntil.current ? "smooth" : "auto");
      sync(viewport);
    };
    onChange();
    const ro = typeof ResizeObserver !== "undefined" ? new ResizeObserver(onChange) : undefined;
    ro?.observe(content);
    ro?.observe(viewport);
    const mo = typeof MutationObserver !== "undefined" ? new MutationObserver(onChange) : undefined;
    mo?.observe(content, { childList: true, subtree: true, characterData: true });
    return () => {
      ro?.disconnect();
      mo?.disconnect();
    };
  }, [viewport, content, pin, sync, enabled]);

  return { viewportRef: setViewport, contentRef: setContent, viewport, atBottom, scrollToBottom, scrollToElement, isStuck: () => state.current.stuck };
}

/* ---------- components ---------- */

type ConversationContextValue = ReturnType<typeof useStickToBottom>;
const ConversationContext = createContext<ConversationContextValue | null>(null);

/** Stick-to-bottom state of the nearest Conversation. */
export function useConversation() {
  const ctx = useContext(ConversationContext);
  if (!ctx) throw new Error("useConversation must be used inside <Conversation>");
  return ctx;
}

export type ConversationProps = Omit<ComponentPropsWithRef<"div">, "role"> & {
  /** Accessible name of the message log. */
  "aria-label"?: string;
  /** aria-live of the log. Keep "off" and use ConversationAnnouncer (see above). */
  live?: "off" | "polite";
  threshold?: number;
  /**
   * Follow new content to the bottom (default true). Pass false while the
   * conversation is empty, so a tall welcome starts at its top.
   */
  stickToBottom?: boolean;
  /** Class for the inner scrolling element. */
  viewportClassName?: string;
};

export function Conversation({
  "aria-label": label = "Conversation",
  live = "off",
  threshold,
  stickToBottom = true,
  className,
  viewportClassName,
  children,
  ...props
}: ConversationProps) {
  const stick = useStickToBottom({ threshold, enabled: stickToBottom });
  return (
    <ConversationContext.Provider value={stick}>
      <div {...props} className={cx(styles.root, className)}>
        <div ref={stick.viewportRef} role="log" aria-live={live} aria-label={label} tabIndex={0} className={cx(styles.viewport, viewportClassName)}>
          {children}
        </div>
      </div>
    </ConversationContext.Provider>
  );
}

export type ConversationContentProps = ComponentPropsWithRef<"div">;

/** The growing column of messages. */
export function ConversationContent({ className, ...props }: ConversationContentProps) {
  const { contentRef } = useConversation();
  return <div {...props} ref={contentRef} className={cx(styles.content, className)} />;
}

export type ConversationEmptyStateProps = Omit<ComponentPropsWithRef<"div">, "title"> & {
  title?: ReactNode;
  description?: ReactNode;
  /** Decorative icon, shown in a tinted tile. */
  icon?: ReactNode;
  /** Decorative media shown as-is above the title instead of the icon tile, e.g. an assistant's MessageAvatar. */
  media?: ReactNode;
  /** Heading level for the title (default: a paragraph). */
  titleAs?: "h1" | "h2" | "h3" | "p";
};

/** Shown before the first message: a greeting, and usually Suggestions as children. */
export function ConversationEmptyState({
  title = "How can I help?",
  description,
  icon,
  media,
  titleAs: Title = "p",
  className,
  children,
  ...props
}: ConversationEmptyStateProps) {
  return (
    <div {...props} className={cx(styles.empty, className)}>
      {media ? (
        <span aria-hidden className={styles.emptyMedia}>
          {media}
        </span>
      ) : (
        icon && (
          <span aria-hidden className={styles.emptyIcon}>
            {icon}
          </span>
        )
      )}
      <Title className={styles.emptyTitle}>{title}</Title>
      {description && <p className={styles.emptyDescription}>{description}</p>}
      {children && <div className={styles.emptyChildren}>{children}</div>}
    </div>
  );
}

export type ConversationScrollButtonProps = {
  label?: string;
  className?: string;
};

/** Appears when the user has scrolled away from the latest message. */
export function ConversationScrollButton({ label = "Scroll to latest message", className }: ConversationScrollButtonProps) {
  const { atBottom, scrollToBottom, viewport } = useConversation();
  if (atBottom) return null;
  return (
    <span className={cx(styles.scrollButton, className)}>
      <Tooltip content={label}>
        <IconButton
          variant="secondary"
          size="sm"
          icon={<ArrowDown aria-hidden />}
          label={label}
          onClick={() => {
            // The button unmounts once at the bottom: keep focus in the log.
            viewport?.focus({ preventScroll: true });
            scrollToBottom("smooth");
          }}
        />
      </Tooltip>
    </span>
  );
}

/* ---------- screen-reader announcements ---------- */

export type ChatStatus = "ready" | "submitted" | "streaming" | "error";

export type ConversationAnnouncerProps = {
  status: ChatStatus;
  /** Override the announcement for each transition. Return "" to stay silent. */
  messages?: Partial<Record<"submitted" | "streaming" | "complete" | "error", string>>;
};

const defaultMessages = {
  submitted: "Message sent",
  streaming: "Assistant is responding",
  complete: "Response complete",
  error: "The response failed",
};

/**
 * A visually hidden polite status that announces chat transitions, so the
 * message log itself can stay silent while tokens stream in.
 */
export function ConversationAnnouncer({ status, messages }: ConversationAnnouncerProps) {
  const text = useMemo(() => ({ ...defaultMessages, ...messages }), [messages]);
  const prev = useRef<ChatStatus>(status);
  const [message, setMessage] = useState({ text: "", alt: false });
  useEffect(() => {
    const was = prev.current;
    prev.current = status;
    if (was === status) return;
    let next: string | undefined;
    if (status === "submitted") next = text.submitted;
    else if (status === "streaming") next = text.streaming;
    else if (status === "error") next = text.error;
    else if (status === "ready" && (was === "streaming" || was === "submitted")) next = text.complete;
    if (next !== undefined) setMessage((m) => ({ text: next, alt: next === m.text ? !m.alt : m.alt }));
  }, [status, text]);
  return (
    <span role="status" className="sr-only">
      {message.text}
      {/* The same text twice in a row toggles a trailing no-break space, so the region still changes and is announced again. */}
      {message.text && message.alt ? "\u00a0" : ""}
    </span>
  );
}
