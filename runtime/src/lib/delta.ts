import { type ChatDocumentRef, type ChatMessage, type ChatPartial, type ChatTool, type ChatView, pageView } from "./view.ts";

/**
 * Incremental event streams (`GET /threads/{id}/events?format=delta`, version 1): a snapshot of the chat view, then
 * only what changed. Each flush of the stream (at most 10 a second, as for whole views) compares the view the client
 * has with the current one and sends the difference, so applying the events in order to the snapshot gives exactly the
 * view a snapshot would show now (`applyDelta`, which clients can copy, is the reference). The reply in progress grows
 * by appends, so the bytes sent follow the new tokens, not the transcript's length. Whatever can't be expressed as a
 * change (the transcript was compacted or reset, the window's first message left) is sent as a new snapshot.
 */
export const DELTA_VERSION = 1;

export type DeltaEvent =
	/** The whole view (windowed when the stream was opened with `limit`); replaces whatever the client had. */
	| { type: "snapshot"; seq: number; v: typeof DELTA_VERSION; view: ChatView }
	/** A message appended to the transcript. */
	| { type: "message.added"; seq: number; message: ChatMessage }
	/** A message of the transcript changed (a tool call's end time, for instance): replace it, matched by id. */
	| { type: "message.updated"; seq: number; message: ChatMessage }
	/** The reply in progress started over or appeared: replace it. */
	| { type: "partial.set"; seq: number; partial: ChatPartial }
	/** Text and thinking appended to the reply in progress (either may be absent). */
	| { type: "partial.appended"; seq: number; text?: string; thinking?: string }
	/** No reply in progress any more (it was added to the transcript as a message, or stopped). */
	| { type: "partial.cleared"; seq: number }
	/** A tool call of the current round appeared (status pending or running, or done for a call not offered). */
	| ({ type: "tool.started"; seq: number; tool: ChatTool } & ToolTiming)
	/**
	 * A tool call's status, output or details changed: `outputAppended` extends the output, `output` replaces it (null:
	 * none); `details` replaces the details (null: none) and `detailsOmitted` says whether they were too large to send.
	 * `tool.finished` is the same change when the status becomes done.
	 */
	| ({
			type: "tool.updated" | "tool.finished";
			seq: number;
			callId: string;
			status?: string;
			output?: string | null;
			outputAppended?: string;
			details?: unknown;
			detailsOmitted?: boolean;
	  } & ToolTiming)
	/** The current round's tool call is gone (the round ended). */
	| { type: "tool.removed"; seq: number; callId: string }
	/** The tool list was reordered (rare): replace it. */
	| { type: "tools.set"; seq: number; tools: ChatTool[] }
	/** The run state: busy while the agent works on a message. */
	| { type: "run"; seq: number; busy: boolean }
	/**
	 * An exposed document of the thread changed (or appeared): its new revision. Read it with
	 * `GET /threads/{thread}/documents/{kind}`; the event never carries the document itself.
	 */
	| { type: "document.updated"; seq: number; kind: string; revision: string };

/**
 * Adds a `document.updated` for every exposed document whose revision changed or that appeared; false when one the
 * client has is gone (rare: a new snapshot says so).
 */
function diffDocuments(
	held: Readonly<Record<string, ChatDocumentRef>> | undefined,
	now: Readonly<Record<string, ChatDocumentRef>> | undefined,
	changes: Change[],
): boolean {
	for (const kind of Object.keys(held ?? {})) if (now?.[kind] === undefined) return false;
	for (const [kind, ref] of Object.entries(now ?? {})) {
		if (held?.[kind]?.revision !== ref.revision) changes.push({ type: "document.updated", kind, revision: ref.revision });
	}
	return true;
}

/** Live metadata of a tool event, not part of the view: when the call started (Unix ms) and how long ago. */
export type ToolTiming = { startedAt?: number; elapsedMs?: number };

type Change = DeltaEvent extends infer E ? (E extends DeltaEvent ? Omit<E, "seq"> : never) : never;

/**
 * Applies one event to a view and returns the new view (the input is not changed). The reference for clients: a
 * client that applies every event of a stream in order holds the view the stream's next snapshot would send.
 */
export function applyDelta(view: ChatView | undefined, event: DeltaEvent): ChatView | undefined {
	if (event.type === "snapshot") return event.view;
	if (view === undefined) return undefined;
	switch (event.type) {
		case "message.added":
			return { ...view, messages: [...view.messages, event.message] };
		case "message.updated":
			return { ...view, messages: view.messages.map((m) => (m.id === event.message.id ? event.message : m)) };
		case "partial.set":
			return { ...view, partial: event.partial };
		case "partial.appended": {
			const partial = view.partial ?? { text: "", thinking: "" };
			return { ...view, partial: { text: partial.text + (event.text ?? ""), thinking: partial.thinking + (event.thinking ?? "") } };
		}
		case "partial.cleared": {
			const { partial: _, ...rest } = view;
			return rest;
		}
		case "tool.started":
			return { ...view, tools: [...view.tools, event.tool] };
		case "tool.updated":
		case "tool.finished":
			return {
				...view,
				tools: view.tools.map((tool) => {
					if (tool.callId !== event.callId) return tool;
					const next: ChatTool = { ...tool, ...(event.status === undefined ? {} : { status: event.status }) };
					if (event.output === null) delete next.output;
					else if (event.output !== undefined) next.output = event.output;
					if (event.outputAppended !== undefined) next.output = (next.output ?? "") + event.outputAppended;
					if (event.details === null) delete next.details;
					else if (event.details !== undefined) next.details = event.details;
					if (event.detailsOmitted === true) next.detailsOmitted = true;
					else if (event.detailsOmitted === false) delete next.detailsOmitted;
					return next;
				}),
			};
		case "tool.removed":
			return { ...view, tools: view.tools.filter((tool) => tool.callId !== event.callId) };
		case "tools.set":
			return { ...view, tools: event.tools };
		case "run":
			return { ...view, busy: event.busy };
		case "document.updated":
			return { ...view, documents: { ...(view.documents ?? {}), [event.kind]: { revision: event.revision } } };
	}
}

/**
 * One stream's side of the delta format: what the client holds, and the changes that bring it to the current view.
 * `limit` keeps the snapshot to the latest messages (the client pages earlier ones with `before`); later messages are
 * added to that window.
 */
export class DeltaStream {
	private seq: number;
	/** The view the client holds (after the last event sent). */
	private sent: ChatView | undefined;
	/** The JSON of each message sent, to find the ones that changed. */
	private sentJson: string[] = [];
	/** When each tool call was first seen running, for calls the transcript doesn't time. */
	private readonly seen = new Map<string, number>();

	constructor(
		private readonly limit: number | undefined,
		lastSeq = 0,
		private readonly now: () => number = Date.now,
	) {
		this.seq = lastSeq;
	}

	/**
	 * The events that bring the client to `view` (the whole, unpaged view); none when nothing changed.
	 * `messagesChanged` false says the transcript is the one of the previous call (only the live part moved), which
	 * spares comparing it.
	 */
	next(view: ChatView, messagesChanged = true): DeltaEvent[] {
		const sent = this.sent;
		if (sent === undefined) return [this.snapshot(view)];
		const changes: Change[] = [];
		if (messagesChanged && !this.diffMessages(sent.messages, view.messages, changes)) return [this.snapshot(view)];
		this.diffPartial(sent.partial, view.partial, changes);
		if (!this.diffTools(sent.tools, view.tools, view.messages, changes)) changes.push({ type: "tools.set", tools: view.tools });
		if (sent.busy !== view.busy) changes.push({ type: "run", busy: view.busy });
		if (!diffDocuments(sent.documents, view.documents, changes)) return [this.snapshot(view)];
		if (changes.length === 0) return [];
		const events = changes.map((change) => ({ ...change, seq: ++this.seq }) as DeltaEvent);
		let held: ChatView | undefined = sent;
		for (const event of events) held = applyDelta(held, event);
		this.sent = held;
		return events;
	}

	private snapshot(view: ChatView): DeltaEvent {
		const windowed = this.limit === undefined ? view : pageView(view, { limit: this.limit });
		this.sent = windowed;
		this.sentJson = windowed.messages.map((m) => JSON.stringify(m));
		return { type: "snapshot", seq: ++this.seq, v: DELTA_VERSION, view: windowed };
	}

	/**
	 * Adds the message changes to `changes`; false when the client's messages are no longer the start of the current
	 * ones (from the window's first message on): a snapshot is needed.
	 */
	private diffMessages(held: readonly ChatMessage[], all: readonly ChatMessage[], changes: Change[]): boolean {
		let from = 0;
		if (held.length > 0) {
			from = all.findIndex((m) => m.id === held[0]!.id);
			if (from < 0) return false;
		} else if (this.sent?.earlierCursor !== undefined) {
			// An empty window with earlier messages: there's no first message to align on.
			return false;
		}
		const now = all.slice(from);
		if (now.length < held.length) return false;
		const json: string[] = [];
		for (let i = 0; i < now.length; i++) {
			const message = now[i]!;
			if (i < held.length && message.id !== held[i]!.id) return false;
			const text = JSON.stringify(message);
			json.push(text);
			if (i >= held.length) changes.push({ type: "message.added", message });
			else if (text !== this.sentJson[i]) changes.push({ type: "message.updated", message });
		}
		this.sentJson = json;
		return true;
	}

	private diffPartial(held: ChatPartial | undefined, now: ChatPartial | undefined, changes: Change[]): void {
		if (now === undefined) {
			if (held !== undefined) changes.push({ type: "partial.cleared" });
			return;
		}
		if (held === undefined || !now.text.startsWith(held.text) || !now.thinking.startsWith(held.thinking)) {
			changes.push({ type: "partial.set", partial: now });
			return;
		}
		const text = now.text.slice(held.text.length);
		const thinking = now.thinking.slice(held.thinking.length);
		if (text === "" && thinking === "") return;
		changes.push({ type: "partial.appended", ...(text === "" ? {} : { text }), ...(thinking === "" ? {} : { thinking }) });
	}

	/** Adds the tool changes; false when the client's tools aren't in the current order (send them whole). */
	private diffTools(held: readonly ChatTool[], now: readonly ChatTool[], messages: readonly ChatMessage[], changes: Change[]): boolean {
		const ids = new Set(now.map((t) => t.callId));
		const kept = held.filter((t) => ids.has(t.callId));
		if (!kept.every((t, i) => now[i]?.callId === t.callId)) return false;
		const out: Change[] = [];
		for (const t of held) {
			if (!ids.has(t.callId)) {
				out.push({ type: "tool.removed", callId: t.callId });
				this.seen.delete(t.callId);
			}
		}
		for (let i = 0; i < now.length; i++) {
			const tool = now[i]!;
			const timing = this.timing(tool, messages);
			const before = kept[i];
			if (before === undefined) {
				out.push({ type: "tool.started", tool, ...timing });
				continue;
			}
			const change: { status?: string; output?: string | null; outputAppended?: string; details?: unknown; detailsOmitted?: boolean } = {};
			if (tool.status !== before.status) change.status = tool.status;
			if (JSON.stringify(tool.details) !== JSON.stringify(before.details)) change.details = tool.details === undefined ? null : tool.details;
			if ((tool.detailsOmitted === true) !== (before.detailsOmitted === true)) change.detailsOmitted = tool.detailsOmitted === true;
			if (tool.output !== before.output) {
				if (tool.output === undefined) change.output = null;
				else if (before.output !== undefined && tool.output.startsWith(before.output)) change.outputAppended = tool.output.slice(before.output.length);
				else change.output = tool.output;
			}
			if (Object.keys(change).length === 0) continue;
			out.push({ type: change.status === "done" ? "tool.finished" : "tool.updated", callId: tool.callId, ...change, ...timing });
		}
		changes.push(...out);
		return true;
	}

	/** When a call started: the transcript's time for the call (its reply's end), else when this stream first saw it. */
	private timing(tool: ChatTool, messages: readonly ChatMessage[]): ToolTiming {
		let startedAt: number | undefined;
		// The call is in one of the last replies.
		for (let i = messages.length - 1; i >= Math.max(0, messages.length - 20) && startedAt === undefined; i--) {
			const m = messages[i]!;
			if (m.role === "assistant") startedAt = m.toolCalls?.find((c) => c.id === tool.callId)?.startedAt;
		}
		if (startedAt === undefined) {
			if (!this.seen.has(tool.callId)) this.seen.set(tool.callId, this.now());
			startedAt = this.seen.get(tool.callId)!;
		}
		return { startedAt, elapsedMs: Math.max(0, this.now() - startedAt) };
	}
}
