/*
 * A thread's delta stream (`…/events?format=delta&v=1`): a snapshot of the
 * chat view, then only what changed. Applying the events in order gives the
 * view a new snapshot would (the runtime's applyDelta is the reference; the
 * runtime's tests prove it against recorded runs).
 */
import type { Schemas } from "./client";

type ChatView = Schemas["ChatView"];
type ChatTool = Schemas["ChatToolSlot"];
export type ChatDeltaEvent = Schemas["ChatDeltaEvent"];

/** Every event name of the format (EventSource needs a listener per name). */
export const deltaEventTypes: ChatDeltaEvent["type"][] = [
  "snapshot",
  "message.added",
  "message.updated",
  "partial.set",
  "partial.appended",
  "partial.cleared",
  "tool.started",
  "tool.updated",
  "tool.finished",
  "tool.removed",
  "tools.set",
  "run",
];

/** The view after one event (undefined until the first snapshot). */
export function applyChatDelta(view: ChatView | undefined, e: ChatDeltaEvent): ChatView | undefined {
  if (e.type === "snapshot") return e.view;
  if (!view) return undefined;
  switch (e.type) {
    case "message.added":
      return e.message ? { ...view, messages: [...view.messages, e.message] } : view;
    case "message.updated":
      return e.message ? { ...view, messages: view.messages.map((m) => (m.id === e.message!.id ? e.message! : m)) } : view;
    case "partial.set":
      return { ...view, partial: e.partial };
    case "partial.appended": {
      const p = view.partial ?? { text: "", thinking: "" };
      return { ...view, partial: { text: p.text + (e.text ?? ""), thinking: p.thinking + (e.thinking ?? "") } };
    }
    case "partial.cleared": {
      const { partial: _partial, ...rest } = view;
      return rest;
    }
    case "tool.started":
      return e.tool ? { ...view, tools: [...view.tools, e.tool] } : view;
    case "tool.updated":
    case "tool.finished":
      return {
        ...view,
        tools: view.tools.map((t) => {
          if (t.callId !== e.callId) return t;
          const next: ChatTool = { ...t, ...(e.status === undefined ? {} : { status: e.status }) };
          if (e.output === null) delete next.output;
          else if (e.output !== undefined) next.output = e.output;
          if (e.outputAppended !== undefined) next.output = (next.output ?? "") + e.outputAppended;
          return next;
        }),
      };
    case "tool.removed":
      return { ...view, tools: view.tools.filter((t) => t.callId !== e.callId) };
    case "tools.set":
      return { ...view, tools: e.tools ?? [] };
    case "run":
      return { ...view, busy: Boolean(e.busy) };
  }
  return view;
}
