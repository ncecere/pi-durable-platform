/* The console's delta applier follows the runtime's reference (runtime/src/delta.ts) on a recorded run's events. */
import { applyChatDelta, type ChatDeltaEvent } from "../api/chat-delta";
import type { Schemas } from "../api/client";

type View = Schemas["ChatView"];

describe("applyChatDelta", () => {
  it("rebuilds the view a snapshot would show from a run's events", () => {
    const events: ChatDeltaEvent[] = [
      { type: "snapshot", seq: 1, v: 1, view: { busy: false, tools: [], messages: [{ id: "e1", role: "user", text: "hi" }] } },
      { type: "run", seq: 2, busy: true },
      { type: "partial.set", seq: 3, partial: { text: "Let", thinking: "" } },
      { type: "partial.appended", seq: 4, text: " me look", thinking: "hmm" },
      { type: "message.added", seq: 5, message: { id: "e2", role: "assistant", text: "Let me look", toolCalls: [{ id: "c1", name: "t", arguments: {} }] } },
      { type: "partial.cleared", seq: 6 },
      { type: "tool.started", seq: 7, tool: { callId: "c1", name: "t", status: "running", output: "a" }, startedAt: 1, elapsedMs: 0 },
      { type: "tool.updated", seq: 8, callId: "c1", outputAppended: "bc", elapsedMs: 100 },
      { type: "tool.finished", seq: 9, callId: "c1", status: "done", output: null, elapsedMs: 200 },
      { type: "message.updated", seq: 10, message: { id: "e2", role: "assistant", text: "Let me look", toolCalls: [{ id: "c1", name: "t", arguments: {}, endedAt: 5 }] } },
      { type: "message.added", seq: 11, message: { id: "e3", role: "tool", toolCallId: "c1", name: "t", text: "abc", isError: false } },
      { type: "tool.removed", seq: 12, callId: "c1" },
      { type: "message.added", seq: 13, message: { id: "e4", role: "user", text: "steer" } },
      { type: "run", seq: 14, busy: false },
    ];
    let view: View | undefined;
    const seen: View[] = [];
    for (const e of events) {
      view = applyChatDelta(view, e);
      seen.push(view!);
    }
    expect(seen[3]!.partial).toEqual({ text: "Let me look", thinking: "hmm" });
    expect(seen[7]!.tools).toEqual([{ callId: "c1", name: "t", status: "running", output: "abc" }]);
    expect(seen[8]!.tools).toEqual([{ callId: "c1", name: "t", status: "done" }]);
    expect(view).toEqual({
      busy: false,
      tools: [],
      messages: [
        { id: "e1", role: "user", text: "hi" },
        { id: "e2", role: "assistant", text: "Let me look", toolCalls: [{ id: "c1", name: "t", arguments: {}, endedAt: 5 }] },
        { id: "e3", role: "tool", toolCallId: "c1", name: "t", text: "abc", isError: false },
        { id: "e4", role: "user", text: "steer" },
      ],
    });
  });

  it("ignores changes before the first snapshot", () => {
    expect(applyChatDelta(undefined, { type: "run", seq: 1, busy: true })).toBeUndefined();
  });
});
