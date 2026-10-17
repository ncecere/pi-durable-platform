"use client";

import { mergeProps } from "@base-ui/react/merge-props";
import { useRender } from "@base-ui/react/use-render";
import { GripVertical } from "lucide-react";
import {
  createContext,
  useCallback,
  useContext,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ComponentPropsWithRef,
  type CSSProperties,
  type KeyboardEvent,
  type PointerEvent,
} from "react";
import { cx, dataFlag } from "@/lib/bitop-utils";
import styles from "./resizable.module.css";

/*
 * Resizable panels: a group of panels separated by draggable handles. No
 * dependencies; parts render through Base UI useRender/mergeProps, so each
 * accepts `render` and merges your handlers with its own.
 *
 *   <ResizablePanelGroup orientation="horizontal" onLayoutChange={save} defaultLayout={saved}>
 *     <ResizablePanel defaultSize={30} minSize={20}>Sidebar</ResizablePanel>
 *     <ResizableHandle withHandle label="Resize sidebar" />
 *     <ResizablePanel>Editor</ResizablePanel>
 *   </ResizablePanelGroup>
 *
 * Sizes are percentages of the group. Panels and handles must be direct
 * children of their group (nest a whole group inside a panel for grids).
 *
 * Each handle is a focusable window splitter (WAI-ARIA APG): role
 * "separator" with aria-valuenow/min/max (the size of the panel before it)
 * and aria-controls pointing at that panel. Drag it with a pointer, or use
 * the arrow keys (by `keyboardStep`), Home (smallest) and End (largest).
 * In right-to-left layouts horizontal arrows follow the visual direction.
 *
 * `onLayoutChange(sizes)` fires when a resize finishes (pointer up or key
 * press); store the array and pass it back as `defaultLayout` to persist.
 */

export type ResizableOrientation = "horizontal" | "vertical";

type PanelConfig = { minSize: number; maxSize: number; defaultSize?: number };

type GroupContext = {
  orientation: ResizableOrientation;
  sizes: Record<string, number> | null;
  version: number;
  keyboardStep: number;
  register: (id: string, config: PanelConfig) => () => void;
  config: (id: string) => PanelConfig | undefined;
  resizeTo: (before: string, after: string, beforeSize: number) => void;
  commit: () => void;
};

const Group = createContext<GroupContext | null>(null);

function useGroup(part: string) {
  const ctx = useContext(Group);
  if (!ctx) throw new Error(`${part} must be inside <ResizablePanelGroup>`);
  return ctx;
}

const clamp = (v: number, lo: number, hi: number) => Math.min(Math.max(v, lo), hi);
const round = (v: number) => Math.round(v * 100) / 100;

/** Initial sizes: explicit defaults, the rest share what's left, then clamp and normalise to 100. */
function initialLayout(ids: string[], configs: (PanelConfig | undefined)[], defaultLayout?: number[]): number[] {
  if (defaultLayout && defaultLayout.length === ids.length && defaultLayout.every((n) => Number.isFinite(n) && n >= 0)) {
    const sum = defaultLayout.reduce((a, b) => a + b, 0);
    if (sum > 0) return defaultLayout.map((n) => (n / sum) * 100);
  }
  const fixed = configs.reduce((a, c) => a + (c?.defaultSize ?? 0), 0);
  const flexible = configs.filter((c) => c?.defaultSize === undefined).length;
  const share = flexible > 0 ? Math.max(0, 100 - fixed) / flexible : 0;
  const raw = configs.map((c) => clamp(c?.defaultSize ?? share, c?.minSize ?? 0, c?.maxSize ?? 100));
  const sum = raw.reduce((a, b) => a + b, 0) || 1;
  return raw.map((n) => (n / sum) * 100);
}

export type ResizablePanelGroupProps = ComponentPropsWithRef<"div"> & {
  /** `horizontal` puts panels side by side (default); `vertical` stacks them. */
  orientation?: ResizableOrientation;
  /** Sizes (percent, one per panel) to start from, e.g. a layout saved from onLayoutChange. */
  defaultLayout?: number[];
  /** Called with every panel's size (percent) when a resize finishes. */
  onLayoutChange?: (layout: number[]) => void;
  /** Percent moved per arrow key press (default 5). */
  keyboardStep?: number;
  render?: useRender.RenderProp;
};

export function ResizablePanelGroup({
  orientation = "horizontal",
  defaultLayout,
  onLayoutChange,
  keyboardStep = 5,
  className,
  render,
  ref,
  ...props
}: ResizablePanelGroupProps) {
  const groupRef = useRef<HTMLDivElement | null>(null);
  const configs = useRef(new Map<string, PanelConfig>());
  const [version, setVersion] = useState(0);
  const [sizes, setSizes] = useState<Record<string, number> | null>(null);
  const sizesRef = useRef(sizes);
  sizesRef.current = sizes;
  const initialLayoutRef = useRef(defaultLayout);
  const onLayoutChangeRef = useRef(onLayoutChange);
  onLayoutChangeRef.current = onLayoutChange;

  const orderedIds = useCallback(() => {
    const el = groupRef.current;
    if (!el) return [];
    return Array.from(el.children)
      .map((c) => (c as HTMLElement).dataset.panelId)
      .filter((id): id is string => Boolean(id));
  }, []);

  const register = useCallback((id: string, config: PanelConfig) => {
    configs.current.set(id, config);
    setVersion((v) => v + 1);
    return () => {
      configs.current.delete(id);
      setVersion((v) => v + 1);
    };
  }, []);

  // Lay out once panels have registered (children's layout effects run first),
  // and again whenever panels are added or removed.
  useLayoutEffect(() => {
    const ids = orderedIds();
    const current = sizesRef.current;
    if (current && ids.length === Object.keys(current).length && ids.every((id) => id in current)) return;
    const layout = initialLayout(
      ids,
      ids.map((id) => configs.current.get(id)),
      current ? undefined : initialLayoutRef.current,
    );
    setSizes(Object.fromEntries(ids.map((id, i) => [id, layout[i]!])));
  }, [version, orderedIds]);

  const resizeTo = useCallback((before: string, after: string, beforeSize: number) => {
    const current = sizesRef.current;
    if (!current || current[before] === undefined || current[after] === undefined) return;
    const b = configs.current.get(before) ?? { minSize: 0, maxSize: 100 };
    const a = configs.current.get(after) ?? { minSize: 0, maxSize: 100 };
    const total = current[before] + current[after];
    const lo = Math.max(b.minSize, total - a.maxSize);
    const hi = Math.min(b.maxSize, total - a.minSize);
    const next = clamp(beforeSize, lo, Math.max(lo, hi));
    if (next === current[before]) return;
    const updated = { ...current, [before]: next, [after]: total - next };
    sizesRef.current = updated;
    setSizes(updated);
  }, []);

  const commit = useCallback(() => {
    const current = sizesRef.current;
    if (!current) return;
    onLayoutChangeRef.current?.(orderedIds().map((id) => round(current[id] ?? 0)));
  }, [orderedIds]);

  const config = useCallback((id: string) => configs.current.get(id), []);

  const value = useMemo<GroupContext>(
    () => ({ orientation, sizes, version, keyboardStep, register, config, resizeTo, commit }),
    [orientation, sizes, version, keyboardStep, register, config, resizeTo, commit],
  );

  const setRef = useCallback(
    (node: HTMLDivElement | null) => {
      groupRef.current = node;
      if (typeof ref === "function") ref(node);
      else if (ref) ref.current = node;
    },
    [ref],
  );

  const element = useRender({
    render,
    defaultTagName: "div",
    ref: setRef,
    props: mergeProps<"div">(props, {
      className: cx(styles.group, className),
      "data-orientation": orientation,
    } as ComponentPropsWithRef<"div">),
  });
  return <Group.Provider value={value}>{element}</Group.Provider>;
}

export type ResizablePanelProps = ComponentPropsWithRef<"div"> & {
  /** Initial size in percent. Panels without one share the remaining space. */
  defaultSize?: number;
  /** Smallest size in percent (default 0). */
  minSize?: number;
  /** Largest size in percent (default 100). */
  maxSize?: number;
  render?: useRender.RenderProp;
};

export function ResizablePanel({ defaultSize, minSize = 0, maxSize = 100, id, className, style, render, ref, ...props }: ResizablePanelProps) {
  const { register, sizes } = useGroup("ResizablePanel");
  const autoId = useId();
  const panelId = id ?? autoId;

  useLayoutEffect(() => register(panelId, { minSize, maxSize, defaultSize }), [register, panelId, minSize, maxSize, defaultSize]);

  // Until the group has laid out (first render, server render), a panel with
  // a defaultSize takes that percentage and panels without one share what's
  // left. Flex weights alone can't do this: "30" beside a default weight of 1
  // would give the other panel ~3% instead of 70%.
  const size = sizes?.[panelId];
  const pendingDefault = size === undefined && defaultSize !== undefined;
  const sizeStyle =
    size !== undefined
      ? ({ "--panel-size": size } as CSSProperties)
      : pendingDefault
        ? ({ "--panel-default-size": defaultSize } as CSSProperties)
        : undefined;

  return useRender({
    render,
    defaultTagName: "div",
    ref,
    props: mergeProps<"div">(props, {
      id: panelId,
      className: cx(styles.panel, className),
      style: { ...sizeStyle, ...style },
      "data-panel-id": panelId,
      "data-size-pending": pendingDefault ? "" : undefined,
    } as ComponentPropsWithRef<"div">),
  });
}

export type ResizableHandleProps = Omit<ComponentPropsWithRef<"div">, "children"> & {
  /** Show a visible grip in the middle of the handle. */
  withHandle?: boolean;
  /** Accessible name (default "Resize panels"). Say what it resizes, e.g. "Resize sidebar". */
  label?: string;
  disabled?: boolean;
  render?: useRender.RenderProp;
};

export function ResizableHandle({ withHandle = false, label = "Resize panels", disabled = false, className, render, ref, ...props }: ResizableHandleProps) {
  const { orientation, sizes, version, keyboardStep, config, resizeTo, commit } = useGroup("ResizableHandle");
  const self = useRef<HTMLDivElement | null>(null);
  const [neighbours, setNeighbours] = useState<[string, string] | null>(null);
  const [dragging, setDragging] = useState(false);
  const drag = useRef<{ start: number; startSize: number; pxPerPercent: number; sign: number } | null>(null);
  const horizontal = orientation === "horizontal";

  useLayoutEffect(() => {
    const el = self.current;
    const before = (el?.previousElementSibling as HTMLElement | null)?.dataset.panelId;
    const after = (el?.nextElementSibling as HTMLElement | null)?.dataset.panelId;
    setNeighbours(before && after ? [before, after] : null);
  }, [version]);

  const setRef = useCallback(
    (node: HTMLDivElement | null) => {
      self.current = node;
      if (typeof ref === "function") ref(node);
      else if (ref) ref.current = node;
    },
    [ref],
  );

  const before = neighbours?.[0];
  const after = neighbours?.[1];
  const beforeSize = before ? sizes?.[before] : undefined;
  const afterSize = after ? sizes?.[after] : undefined;
  const bc = before ? config(before) : undefined;
  const ac = after ? config(after) : undefined;
  const total = (beforeSize ?? 0) + (afterSize ?? 0);
  const min = Math.max(bc?.minSize ?? 0, total - (ac?.maxSize ?? 100));
  const max = Math.min(bc?.maxSize ?? 100, total - (ac?.minSize ?? 0));
  const ready = before !== undefined && after !== undefined && beforeSize !== undefined;
  const pair = ready ? { before, after, beforeSize } : null;
  const isRtl = () => horizontal && self.current !== null && getComputedStyle(self.current).direction === "rtl";

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (!pair || disabled) return;
    const { before, after, beforeSize } = pair;
    const rtl = isRtl();
    let next: number | null = null;
    if (horizontal && event.key === "ArrowLeft") next = beforeSize + (rtl ? keyboardStep : -keyboardStep);
    else if (horizontal && event.key === "ArrowRight") next = beforeSize + (rtl ? -keyboardStep : keyboardStep);
    else if (!horizontal && event.key === "ArrowUp") next = beforeSize - keyboardStep;
    else if (!horizontal && event.key === "ArrowDown") next = beforeSize + keyboardStep;
    else if (event.key === "Home") next = min;
    else if (event.key === "End") next = max;
    if (next === null) return;
    event.preventDefault();
    resizeTo(before, after, next);
    commit();
  }

  function onPointerDown(event: PointerEvent<HTMLDivElement>) {
    if (!pair || disabled || event.button !== 0) return;
    const { before, after, beforeSize } = pair;
    const a = document.getElementById(before);
    const b = document.getElementById(after);
    if (!a || !b) return;
    const ra = a.getBoundingClientRect();
    const rb = b.getBoundingClientRect();
    const px = horizontal ? ra.width + rb.width : ra.height + rb.height;
    event.preventDefault();
    event.currentTarget.focus();
    event.currentTarget.setPointerCapture?.(event.pointerId);
    drag.current = {
      start: horizontal ? event.clientX : event.clientY,
      startSize: beforeSize,
      pxPerPercent: px > 0 ? px / total : 0,
      sign: isRtl() ? -1 : 1,
    };
    setDragging(true);
  }

  function onPointerMove(event: PointerEvent<HTMLDivElement>) {
    const d = drag.current;
    if (!d || !pair || d.pxPerPercent === 0) return;
    const delta = ((horizontal ? event.clientX : event.clientY) - d.start) * d.sign;
    resizeTo(pair.before, pair.after, d.startSize + delta / d.pxPerPercent);
  }

  function endDrag(event: PointerEvent<HTMLDivElement>) {
    if (!drag.current) return;
    drag.current = null;
    setDragging(false);
    if (event.currentTarget.hasPointerCapture?.(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId);
    commit();
  }

  return useRender({
    render,
    defaultTagName: "div",
    ref: setRef,
    props: mergeProps<"div">(props, {
      role: "separator",
      tabIndex: disabled ? undefined : 0,
      "aria-label": label,
      // A horizontal group has a vertical splitter between side-by-side panels.
      "aria-orientation": horizontal ? "vertical" : "horizontal",
      "aria-controls": before,
      "aria-valuenow": pair ? Math.round(pair.beforeSize) : undefined,
      "aria-valuemin": pair ? Math.round(min) : undefined,
      "aria-valuemax": pair ? Math.round(max) : undefined,
      "aria-disabled": disabled || undefined,
      className: cx(styles.handle, className),
      "data-orientation": orientation,
      "data-dragging": dataFlag(dragging),
      "data-disabled": dataFlag(disabled),
      onKeyDown,
      onPointerDown,
      onPointerMove,
      onPointerUp: endDrag,
      onPointerCancel: endDrag,
      children: withHandle ? (
        <span aria-hidden className={styles.grip}>
          <GripVertical />
        </span>
      ) : undefined,
    } as ComponentPropsWithRef<"div">),
  });
}
