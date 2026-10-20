import { createHash } from "node:crypto";
import { BACKGROUND_CONTEXT, type Context } from "@earendil-works/chord/context";
import {
	defineDoc,
	defineExtension,
	type EntryRecord,
	type Extension,
	type Harness,
	type HookApi,
	hook,
	type JsonObject,
	LiveDoc,
	type ToolHookCall,
	ToolTask,
} from "@earendil-works/pi-durable";
import { redactArguments } from "./redact.ts";
import type { ThreadStore } from "./threads.ts";
import { type Author, type ChatApproval, USER_ENTRY, type ViewApprovals } from "./view.ts";

/**
 * Tool approvals (docs/approvals.md): a call to a tool the template marks, or one an author's hook asks about, waits in
 * its tool task's `beforeTool` hook until a person decides. The hook runs before pi-durable records the call's intent,
 * so a process that stops while it waits (hibernation, a crash) leaves the task to rerun from the start: its hook asks
 * again, finds the request here (its ID comes from the task's ID) and the decision if one arrived meanwhile.
 */

export type ApprovalStatus = "pending" | "approved" | "denied" | "timed_out" | "cancelled";

/** Who decided: a person or service account (as the platform says), or the system (a time-out, a cancellation). */
export type Decider = { kind: "user" | "service_account" | "system"; id?: string; name: string };

/** One request, as the agent's storage keeps it (`pdp.approvals`). */
export type ApprovalRecord = {
	id: string;
	/** The thread whose run made the call (absent: a conversation no thread owns). */
	threadId?: string;
	/** The conversation the call is in (the thread's, or a subagent's under it). */
	conversationId: number;
	taskId: number;
	callId: string;
	tool: string;
	/** A bounded, redacted view of the arguments (redact.ts). */
	arguments: unknown;
	/** Why approval is asked, from an author's hook. */
	summary?: string;
	/** Who the run is for: the author of the message that started it (absent: unattended). */
	requestedFor?: Author;
	requestedAt: number;
	expiresAt?: number;
	status: ApprovalStatus;
	decidedAt?: number;
	decidedBy?: Decider;
	/** The decider's reason, given to the model on denial. */
	reason?: string;
};

type ApprovalsState = { items: Record<string, ApprovalRecord> };

/** The agent's approval requests, pending and recently resolved. The library's: `pdp.` is reserved for the platform. */
export const ApprovalsDoc = defineDoc<ApprovalsState & JsonObject>({
	kind: "pdp.approvals",
	version: 1,
	scope: "session",
	initial: () => ({ items: {} }),
});

/** What a request asks besides the call itself. */
export type ApprovalOptions = {
	/** Shown to the people who decide: why it needs approval, what the agent knows. At most 2,000 characters. */
	summary?: string;
	/** Two requests for one call with the same key are one request. Default "" (the template's and an author's share it). */
	key?: string;
	/** Without a platform only (the test kit): how long until the request is denied. Default the template's. */
	timeoutMs?: number;
};

export type ApprovalDecision = {
	approved: boolean;
	status: ApprovalStatus;
	reason?: string;
	decidedBy?: Decider;
	/** The block message pi-durable gives the model ("Tool call blocked: <message>") when not approved. */
	message: string;
};

/** What an author's hook uses (`AgentContext.approvals`). */
export interface ApprovalRequester {
	/**
	 * Asks a person to approve this call and waits for the decision (durably: across restarts and hibernation). Use it
	 * in a `ToolTask` `beforeTool` hook, with the hook's `api` and `context`, and block the call when not approved.
	 */
	request(call: ToolHookCall, api: HookApi, context: Context, options?: ApprovalOptions): Promise<ApprovalDecision>;
}

/** The template's approval policy: tool names or `*` patterns, and how long a request waits. */
export type ApprovalPolicy = { tools: readonly string[]; timeoutMs: number };

export const DEFAULT_APPROVAL_TIMEOUT_MS = 24 * 60 * 60 * 1000;
const MAX_RESOLVED = 500;
const MAX_TIMER = 2 ** 31 - 1;

/** The policy in `TOOL_APPROVALS` (JSON `{tools, timeoutSeconds}`); none when absent or malformed. */
export function parseApprovalPolicy(raw: string | undefined): ApprovalPolicy {
	const none: ApprovalPolicy = { tools: [], timeoutMs: DEFAULT_APPROVAL_TIMEOUT_MS };
	if (raw === undefined || raw.trim() === "") return none;
	try {
		const value = JSON.parse(raw) as { tools?: unknown; timeoutSeconds?: unknown };
		const tools = Array.isArray(value.tools) ? value.tools.filter((t): t is string => typeof t === "string" && t.length > 0 && t.length <= 200).slice(0, 100) : [];
		const seconds = typeof value.timeoutSeconds === "number" && value.timeoutSeconds > 0 ? value.timeoutSeconds : undefined;
		return { tools, timeoutMs: seconds === undefined ? DEFAULT_APPROVAL_TIMEOUT_MS : seconds * 1000 };
	} catch {
		console.error("[runtime] TOOL_APPROVALS is not valid JSON; no tool needs approval");
		return none;
	}
}

/** Whether a tool name matches a pattern (`*`: any characters). */
export function toolMatches(pattern: string, name: string): boolean {
	const re = new RegExp(`^${pattern.split("*").map((part) => part.replace(/[.+?^${}()|[\]\\]/g, "\\$&")).join(".*")}$`);
	return re.test(name);
}

/** A request's ID: from its tool task (durable) and key, so a rerun of the hook finds the same request. */
export function approvalId(taskId: string | number, key = ""): string {
	return `ap_${createHash("sha256").update(`${taskId}\u0000${key}`).digest("base64url").slice(0, 22)}`;
}

/** Talks to the platform's gateway: POST JSON, returning the reply's `data`. */
export type GatewayCall = (path: string, body: unknown) => Promise<unknown>;

/** A decision as the platform (or a test) delivers it. */
export type DecisionInput = { decision: "approve" | "deny" | "timeout" | "cancel"; reason?: string; decidedBy?: Decider };

const ctx = BACKGROUND_CONTEXT;

/**
 * The agent's approvals: requests, decisions, and what the platform is told. One per running agent; bound to its
 * harness once open. The cache mirrors `pdp.approvals` (this process owns the storage, so every change goes through
 * here) so the chat view can show approvals without reading storage on every frame.
 */
export class Approvals implements ApprovalRequester {
	private harness: Harness | undefined;
	private threads: ThreadStore | undefined;
	private gateway: GatewayCall | undefined;
	private cache = new Map<string, ApprovalRecord>();
	private readonly waiters = new Map<string, Set<() => void>>();
	private readonly listeners = new Set<(threadId: string | undefined) => void>();
	private readonly timers = new Map<string, ReturnType<typeof setTimeout>>();
	private reports: Promise<void> = Promise.resolve();
	private closed = false;

	constructor(readonly policy: ApprovalPolicy) {}

	/** Connects the requester to the open agent; `gateway` is the platform (absent: decide time-outs here). */
	async bind(harness: Harness, threads: ThreadStore, gateway?: GatewayCall): Promise<void> {
		this.harness = harness;
		this.threads = threads;
		this.gateway = gateway;
		const state = await harness.snapshot(ApprovalsDoc, ctx);
		this.cache = new Map(Object.entries((state as ApprovalsState | undefined)?.items ?? {}).map(([id, r]) => [id, clone(r)]));
	}

	/** Whether the template marks this tool. */
	marks(name: string): boolean {
		return this.policy.tools.some((pattern) => toolMatches(pattern, name));
	}

	/** The library's extension: asks before every call of a tool the template marks. Selected last in every thread. */
	extension(): Extension {
		return defineExtension({
			name: "pdp-approvals",
			hooks: [
				hook(ToolTask, {
					beforeTool: async (call, api, context) => {
						// A request already asked stays asked, also after a new version stopped marking the tool.
						const asked = this.cache.get(approvalId(api.taskId as unknown as number))?.status === "pending";
						if (!asked && !this.marks(call.name)) return undefined;
						const decision = await this.request(call, api, context);
						return decision.approved ? undefined : { block: decision.message };
					},
				}),
			],
		});
	}

	async request(call: ToolHookCall, api: HookApi, context: Context, options: ApprovalOptions = {}): Promise<ApprovalDecision> {
		const harness = this.bound();
		const id = approvalId(api.taskId as unknown as number, options.key ?? "");
		let record = this.cache.get(id);
		if (record === undefined) {
			const fresh = await this.describe(id, call, api, options);
			record = await harness.commit(async (tx) => {
				const doc = (await tx.doc(ApprovalsDoc)) as unknown as ApprovalsState;
				const existing = doc.items[id];
				if (existing !== undefined) return clone(existing);
				prune(doc);
				doc.items[id] = fresh;
				return clone(fresh);
			}, context);
			this.cache.set(id, record);
			this.changed(record.threadId);
		}
		if (record.status === "pending") {
			await this.report(record);
			record = await this.wait(id, context);
		}
		return decisionOf(record);
	}

	/** Applies a decision; undefined when there is no such request, `applied: false` when it was already resolved. */
	async decide(id: string, input: DecisionInput): Promise<{ status: ApprovalStatus; applied: boolean } | undefined> {
		const harness = this.bound();
		const status: ApprovalStatus =
			input.decision === "approve" ? "approved" : input.decision === "deny" ? "denied" : input.decision === "timeout" ? "timed_out" : "cancelled";
		const reason = cleanReason(input.reason);
		const result = await harness.commit(async (tx) => {
			const doc = (await tx.doc(ApprovalsDoc)) as unknown as ApprovalsState;
			const record = doc.items[id];
			if (record === undefined) return undefined;
			if (record.status !== "pending") return { record: clone(record), applied: false };
			record.status = status;
			record.decidedAt = Date.now();
			const by = input.decidedBy ?? (status === "timed_out" || status === "cancelled" ? { kind: "system" as const, name: "System" } : undefined);
			if (by !== undefined) record.decidedBy = cleanDecider(by);
			if (reason !== undefined) record.reason = reason;
			return { record: clone(record), applied: true };
		}, ctx);
		if (result === undefined) return undefined;
		this.cache.set(id, result.record);
		if (result.applied) {
			this.clearTimer(id);
			for (const wake of this.waiters.get(id) ?? []) wake();
			this.changed(result.record.threadId);
		}
		return { status: result.record.status, applied: result.applied };
	}

	/** Every request (pending first, then newest), or one thread's. */
	list(threadId?: string): ApprovalRecord[] {
		return [...this.cache.values()]
			.filter((r) => threadId === undefined || r.threadId === threadId)
			.sort((a, b) => Number(b.status === "pending") - Number(a.status === "pending") || b.requestedAt - a.requestedAt);
	}

	/** What a thread's chat view shows: the approvals of its own conversation's calls, and its pending requests. */
	viewOf(threadId: string, conversationId: number): ViewApprovals {
		const records = this.list(threadId);
		const byCall = new Map<string, ChatApproval>();
		for (const r of records) if (r.conversationId === conversationId) byCall.set(r.callId, chatApproval(r, false));
		const pending = records
			.filter((r) => r.status === "pending")
			.sort((a, b) => a.requestedAt - b.requestedAt)
			.map((r) => chatApproval(r, true));
		return { byCall, pending };
	}

	pending(): ApprovalRecord[] {
		return this.list().filter((r) => r.status === "pending");
	}

	/** Called with a thread's ID (undefined: unknown) whenever one of its requests changes. Returns the unsubscribe. */
	onChange(listener: (threadId: string | undefined) => void): () => void {
		this.listeners.add(listener);
		return () => this.listeners.delete(listener);
	}

	/**
	 * Cancels pending requests whose call is gone (the run was stopped, the thread deleted, the call finished some other
	 * way) and tells the platform. Run at start and after a thread's run is stopped.
	 */
	async reconcile(threadId?: string): Promise<void> {
		const harness = this.bound();
		for (const record of this.pending()) {
			if (threadId !== undefined && record.threadId !== threadId) continue;
			const task = await harness.getTask(record.taskId as never, ctx).catch(() => undefined);
			const live = task !== undefined && (task.state.status === "pending" || task.state.status === "running" || task.state.status === "waiting");
			if (live) continue;
			const done = await this.decide(record.id, { decision: "cancel" });
			if (done?.applied) void this.report({ ...record, status: "cancelled" });
		}
	}

	/**
	 * At start: cancels what is gone, then sends the platform every pending request and applies the decisions it already
	 * has (made while the agent was hibernated, if the delivery didn't wake it).
	 */
	async sync(): Promise<void> {
		await this.reconcile();
		if (this.gateway === undefined) return;
		const at = Date.now();
		const data = await this.gateway("/approvals/sync", { approvals: this.pending().map(reportOf), at });
		for (const item of Array.isArray(data) ? data : []) await this.applyAnswer(item);
	}

	/** Pending requests and the threads that wait only for a person (for /status). */
	waiting(): { pending: number; threads: Set<string> } {
		const threads = new Set<string>();
		const pending = this.pending();
		for (const r of pending) if (r.threadId !== undefined) threads.add(r.threadId);
		return { pending: pending.length, threads };
	}

	close(): void {
		this.closed = true;
		for (const timer of this.timers.values()) clearTimeout(timer);
		this.timers.clear();
	}

	private bound(): Harness {
		if (this.harness === undefined) throw new Error("approvals are not available before the agent has started");
		return this.harness;
	}

	private changed(threadId: string | undefined): void {
		for (const listener of this.listeners) {
			try {
				listener(threadId);
			} catch (error) {
				console.error("[runtime] approvals listener:", (error as Error).message);
			}
		}
	}

	/** Waits until the request is resolved, or the hook's context is aborted (a stop, or the process closing). */
	private wait(id: string, context: Context): Promise<ApprovalRecord> {
		return new Promise((resolve, reject) => {
			const signal = context.abortSignal;
			const set = this.waiters.get(id) ?? new Set();
			this.waiters.set(id, set);
			const done = () => {
				set.delete(wake);
				if (set.size === 0) this.waiters.delete(id);
				signal?.removeEventListener("abort", onAbort);
			};
			const wake = () => {
				const record = this.cache.get(id);
				if (record === undefined || record.status === "pending") return;
				done();
				resolve(record);
			};
			const onAbort = () => {
				done();
				reject(signal?.reason ?? new Error("aborted"));
			};
			if (signal?.aborted) return onAbort();
			set.add(wake);
			signal?.addEventListener("abort", onAbort, { once: true });
			this.armTimer(id);
			wake();
		});
	}

	/** Without a platform, the runtime denies a request itself when it expires (the platform does it otherwise). */
	private armTimer(id: string): void {
		const record = this.cache.get(id);
		if (this.gateway !== undefined || this.closed || this.timers.has(id) || record?.expiresAt === undefined) return;
		const fire = () => {
			this.timers.delete(id);
			const left = (this.cache.get(id)?.expiresAt ?? 0) - Date.now();
			if (left > 0) {
				this.timers.set(id, setTimeout(fire, Math.min(left, MAX_TIMER)));
				return;
			}
			void this.decide(id, { decision: "timeout" }).catch((error: unknown) => console.error("[runtime] approval time-out:", (error as Error).message));
		};
		this.timers.set(id, setTimeout(fire, Math.min(Math.max(0, record.expiresAt - Date.now()), MAX_TIMER)));
	}

	private clearTimer(id: string): void {
		const timer = this.timers.get(id);
		if (timer !== undefined) clearTimeout(timer);
		this.timers.delete(id);
	}

	/**
	 * Tells the platform about a request (idempotent: every rerun of the hook reports again). Its answer can carry a
	 * decision already made. Failures are retried in the background; the next start's sync covers what is still missed.
	 */
	private async report(record: ApprovalRecord): Promise<void> {
		const gateway = this.gateway;
		if (gateway === undefined) return;
		const send = async () => this.applyAnswer({ id: record.id, ...((await gateway("/approvals/report", reportOf(record))) as object) });
		try {
			await send();
		} catch {
			this.reports = this.reports.then(async () => {
				for (let attempt = 1; attempt <= 5 && !this.closed; attempt++) {
					await new Promise((r) => setTimeout(r, 1000 * 2 ** attempt));
					if (this.cache.get(record.id)?.status !== record.status) return;
					try {
						await send();
						return;
					} catch (error) {
						if (attempt === 5) console.error("[runtime] reporting an approval failed:", (error as Error).message);
					}
				}
			});
		}
	}

	/** The platform's word about a request: its expiry, and a decision if it has one. */
	private async applyAnswer(item: unknown): Promise<void> {
		if (typeof item !== "object" || item === null) return;
		const { id, status, reason, decidedBy, expiresAt } = item as Record<string, unknown>;
		if (typeof id !== "string") return;
		const record = this.cache.get(id);
		if (record === undefined || record.status !== "pending") return;
		if (typeof expiresAt === "number" && expiresAt > 0 && record.expiresAt !== expiresAt) {
			const updated = await this.bound().commit(async (tx) => {
				const r = ((await tx.doc(ApprovalsDoc)) as unknown as ApprovalsState).items[id];
				if (r !== undefined) r.expiresAt = expiresAt;
				return r === undefined ? undefined : clone(r);
			}, ctx);
			if (updated !== undefined) {
				this.cache.set(id, updated);
				this.changed(updated.threadId);
			}
		}
		const decision = status === "approved" ? "approve" : status === "denied" ? "deny" : status === "timed_out" ? "timeout" : status === "cancelled" ? "cancel" : undefined;
		if (decision === undefined) return;
		await this.decide(id, { decision, ...(typeof reason === "string" ? { reason } : {}), ...(parseDecider(decidedBy) ? { decidedBy: parseDecider(decidedBy)! } : {}) });
	}

	/** A new request: the call, its thread and who the run is for. */
	private async describe(id: string, call: ToolHookCall, api: HookApi, options: ApprovalOptions): Promise<ApprovalRecord> {
		const now = Date.now();
		const conversationId = api.conversationId as unknown as number;
		const place = await this.placeOf(conversationId).catch(() => undefined);
		const timeoutMs = options.timeoutMs ?? this.policy.timeoutMs;
		const summary = options.summary?.trim().slice(0, 2000);
		const record: ApprovalRecord = {
			id,
			conversationId,
			taskId: api.taskId as unknown as number,
			callId: call.id,
			tool: call.name,
			arguments: redactArguments(call.arguments ?? {}),
			requestedAt: now,
			expiresAt: now + timeoutMs,
			status: "pending",
		};
		if (place?.threadId !== undefined) record.threadId = place.threadId;
		if (place?.requestedFor !== undefined) record.requestedFor = place.requestedFor;
		if (summary) record.summary = summary;
		return record;
	}

	/**
	 * The thread a conversation belongs to (a subagent's conversation belongs to its owner's), and who its current run
	 * is for: the author of the message that started it.
	 */
	private async placeOf(conversationId: number): Promise<{ threadId?: string; requestedFor?: Author } | undefined> {
		const harness = this.bound();
		const byConversation = new Map((await this.threads!.list()).map((t) => [t.conversationId, t.threadId]));
		let id = conversationId;
		for (let depth = 0; depth < 8; depth++) {
			const threadId = byConversation.get(id);
			const conversation = await harness.conversation(id as never, ctx);
			if (conversation === undefined) return undefined;
			const watch = await conversation.watch(ctx);
			const { entries, conversation: record } = watch.value;
			await watch.stop();
			if (threadId !== undefined) {
				const author = runStarter(entries);
				return { threadId, ...(author === undefined ? {} : { requestedFor: author }) };
			}
			if (record.owner === undefined) return undefined;
			id = record.owner.conversationId as unknown as number;
		}
		return undefined;
	}
}

/** A ToolTask hook that asks for approval when `decide` says so (options, or true), and blocks a denied call. */
export function approvalHook(
	approvals: ApprovalRequester,
	decide: (call: ToolHookCall) => ApprovalOptions | boolean | undefined | Promise<ApprovalOptions | boolean | undefined>,
) {
	return hook(ToolTask, {
		beforeTool: async (call, api, context) => {
			const ask = await decide(call);
			if (ask === undefined || ask === false) return undefined;
			const decision = await approvals.request(call, api, context, ask === true ? {} : ask);
			return decision.approved ? undefined : { block: decision.message };
		},
	});
}

/**
 * The author of the message that started the latest run: walking back from the end, the earliest user message after
 * the previous final answer (a steer that joined the run is later than it). Undefined when it has no author.
 */
export function runStarter(entries: readonly EntryRecord[]): Author | undefined {
	let starter: Author | undefined;
	let found = false;
	for (let i = entries.length - 1; i >= 0; i--) {
		const entry = entries[i]!;
		if (entry.kind === USER_ENTRY || entry.kind === "pi.user") {
			found = true;
			starter = entry.kind === USER_ENTRY ? authorOf(entry.data) : undefined;
			continue;
		}
		const final = (entry.model ?? []).some((m) => m.role === "assistant" && (m as { stopReason?: string }).stopReason !== "toolUse");
		if (final && found) break;
	}
	return starter;
}

function authorOf(data: unknown): Author | undefined {
	if (typeof data !== "object" || data === null) return undefined;
	const author = (data as { author?: unknown }).author;
	if (typeof author !== "object" || author === null) return undefined;
	const { kind, id, name, via } = author as Record<string, unknown>;
	if ((kind !== "user" && kind !== "service_account") || typeof id !== "string" || typeof name !== "string") return undefined;
	return { kind, id, name, ...(typeof via === "string" ? { via } : {}) };
}

function parseDecider(value: unknown): Decider | undefined {
	if (typeof value !== "object" || value === null) return undefined;
	const { kind, id, name } = value as Record<string, unknown>;
	if ((kind !== "user" && kind !== "service_account" && kind !== "system") || typeof name !== "string") return undefined;
	return cleanDecider({ kind, name, ...(typeof id === "string" ? { id } : {}) });
}

function cleanDecider(d: Decider): Decider {
	const name = oneLine(d.name).slice(0, 200) || (d.kind === "system" ? "System" : "someone");
	return { kind: d.kind, name, ...(d.id === undefined ? {} : { id: d.id.slice(0, 64) }) };
}

function cleanReason(reason: string | undefined): string | undefined {
	if (reason === undefined) return undefined;
	const clean = oneLine(reason).slice(0, 1000);
	return clean === "" ? undefined : clean;
}

const oneLine = (s: string) => s.replace(/[\u0000-\u001f\u007f]+/g, " ").trim();

function decisionOf(record: ApprovalRecord): ApprovalDecision {
	const approved = record.status === "approved";
	const base = {
		approved,
		status: record.status,
		...(record.reason === undefined ? {} : { reason: record.reason }),
		...(record.decidedBy === undefined ? {} : { decidedBy: record.decidedBy }),
	};
	return { ...base, message: blockMessage(record) };
}

/** What the model reads when a call isn't approved (pi-durable adds "Tool call blocked: "). */
function blockMessage(record: ApprovalRecord): string {
	const why = record.reason === undefined ? "" : `: ${record.reason}`;
	switch (record.status) {
		case "approved":
			return "";
		case "denied":
			return `denied by ${record.decidedBy?.name ?? "the person deciding"}${why}`;
		case "timed_out":
			return "nobody approved it in time";
		case "cancelled":
			return "the approval request was cancelled";
		default:
			return "not approved";
	}
}

/** A request as the chat view shows it. */
export function chatApproval(r: ApprovalRecord, withArguments: boolean): ChatApproval {
	return {
		id: r.id,
		callId: r.callId,
		tool: r.tool,
		status: r.status,
		requestedAt: r.requestedAt,
		...(r.expiresAt === undefined ? {} : { expiresAt: r.expiresAt }),
		...(r.summary === undefined ? {} : { summary: r.summary }),
		...(r.requestedFor === undefined ? {} : { requestedFor: r.requestedFor }),
		...(r.decidedAt === undefined ? {} : { decidedAt: r.decidedAt }),
		...(r.decidedBy === undefined ? {} : { decidedBy: r.decidedBy }),
		...(r.reason === undefined ? {} : { reason: r.reason }),
		...(withArguments ? { arguments: r.arguments } : {}),
	};
}

/** What the platform is told about a request. */
function reportOf(record: ApprovalRecord) {
	return {
		id: record.id,
		threadId: record.threadId,
		tool: record.tool,
		callId: record.callId,
		arguments: record.arguments,
		summary: record.summary,
		requestedFor: record.requestedFor,
		requestedAt: record.requestedAt,
		status: record.status,
	};
}

/** Keeps every pending request and the newest resolved ones. */
function prune(doc: ApprovalsState): void {
	const resolved = Object.values(doc.items).filter((r) => r.status !== "pending");
	if (resolved.length < MAX_RESOLVED) return;
	resolved.sort((a, b) => (a.decidedAt ?? a.requestedAt) - (b.decidedAt ?? b.requestedAt));
	for (const r of resolved.slice(0, resolved.length - MAX_RESOLVED + 1)) delete doc.items[r.id];
}

function clone<T>(value: T): T {
	return JSON.parse(JSON.stringify(value)) as T;
}

/** The statuses /status needs: threads whose reply waits only for a person. */
export async function waitingThreads(harness: Harness, threads: ThreadStore, candidates: Set<string>): Promise<number> {
	let n = 0;
	for (const threadId of candidates) {
		const conversation = await threads.get(threadId);
		if (conversation === undefined) continue;
		const live = await harness.snapshot(LiveDoc, conversation.id, ctx);
		if (live?.run === undefined) continue;
		if (live.generation !== undefined) continue;
		if ((live.tools ?? []).some((t) => t.status === "running")) continue;
		n++;
	}
	return n;
}
