import { BACKGROUND_CONTEXT, withAbortSignal } from "@earendil-works/chord/context";
import { type ConversationId, type Harness, LiveDoc, type TaskId, type TaskInspection } from "@earendil-works/pi-durable";

const ctx = BACKGROUND_CONTEXT;

/** `/status` reports counts up to this (a count at the cap means "this many or more"). */
export const MAX_REPORTED_TASKS = 1000;

/**
 * Task kinds the platform's own runtime treats as a wait for a person's answer (an approval, #16). Empty until the
 * approvals land; an agent declares its own with `AgentDefinition.waitsForPerson`. See docs/hibernation.md.
 */
export const PLATFORM_PERSON_WAIT_KINDS: readonly string[] = [];

/** What `/status` reports as `backgroundWork` (docs/runtime-contract.md, Status). */
export type BackgroundWork = {
	/** Units of live background work that keep the agent awake (at most MAX_REPORTED_TASKS). */
	tasks: number;
	/** Unix ms when the oldest of them started; null when there are none. */
	oldestStartedAt: number | null;
	/** Live tasks waiting for a person's answer, anywhere (at most MAX_REPORTED_TASKS); they keep nothing awake. */
	waitingForPerson: number;
};

/**
 * One unit of background work: a task that is not part of a thread's ordinary run, with everything it owns. A
 * background task (`background: true`), a task at the top of a conversation that is not a thread (a background
 * subagent's run, once the task that created its conversation is gone), or a task left in a thread whose run has ended
 * (a tool call still running).
 */
export type BackgroundUnit = {
	readonly taskId: TaskId;
	readonly kind: string;
	readonly conversationId: ConversationId;
	/** When it started (pi-durable's `startedAt`), or when this process first saw it if it hasn't run yet. */
	readonly startedAt: number;
	/** Only waiting for a person (or blocked: no definition can run it), so it keeps nothing awake. */
	readonly idle: boolean;
};

export type LiveWork = {
	/** Threads with a run in progress that is not only waiting for a person. */
	busyThreads: number;
	units: BackgroundUnit[];
	/** Live tasks of a kind that waits for a person. */
	waitingForPerson: number;
};

export type WorkOptions = {
	/** Whether a task of this kind stands for a wait on a person's answer. */
	waitsForPerson: (kind: string) => boolean;
	/** When this process first saw each live task (for tasks that haven't run yet); pruned to live tasks. */
	firstSeen: Map<number, number>;
	now?: () => number;
};

type Node = {
	readonly id: number;
	readonly inspection: TaskInspection;
	readonly conversationId: number;
	readonly owner?: number;
	readonly background: boolean;
	readonly kind: string;
};

/**
 * Reads the agent's live work from pi-durable: `harness.inspect()` (every live task with its record and what the
 * scheduler would do with it) and `harness.taskGraph()` (which conversations each task owns), and the threads' live
 * runs. Writes nothing and runs no task code.
 *
 * A task belongs to a thread's ordinary run when, following its owners up (an owner task, or the task that owns its
 * conversation), no task on the way is a background task and the top one is in a thread whose run is in progress.
 * Every other live task is background work, grouped by its nearest background task, or by the top task otherwise.
 *
 * A task is idle when it is blocked (no definition can run it), when its kind waits for a person, or when everything
 * it waits for and everything it owns is idle (at least one thing): a tool call awaiting an approval task, or the
 * reply waiting on that tool call. Idle work keeps nothing awake: see docs/hibernation.md.
 */
export async function liveWork(harness: Harness, threadConversations: readonly number[], options: WorkOptions): Promise<LiveWork> {
	const now = options.now?.() ?? Date.now();
	const inspection = await harness.inspect(ctx);
	const graph = await harness.taskGraph(ctx);
	const ownedBy = new Map<number, number>(); // conversation -> the live task that owns it
	try {
		for (const node of Object.values(graph.value.tasks)) for (const conversation of node.conversations) ownedBy.set(conversation as number, node.id as number);
	} finally {
		graph.dispose();
	}

	const nodes = new Map<number, Node>();
	for (const task of inspection.tasks) {
		const record = task.record;
		nodes.set(record.id as number, {
			id: record.id as number,
			inspection: task,
			conversationId: record.conversationId as number,
			...(record.owner === undefined ? {} : { owner: record.owner as number }),
			background: record.background,
			kind: record.kind,
		});
	}
	for (const id of options.firstSeen.keys()) if (!nodes.has(id)) options.firstSeen.delete(id);
	for (const id of nodes.keys()) if (!options.firstSeen.has(id)) options.firstSeen.set(id, now);

	// Owner edges: a task's owner task, or (for a task at the top of its conversation) the live task owning that
	// conversation. A background task is a boundary: it has no owner edge (its work doesn't keep anything above busy).
	const parent = (node: Node): Node | undefined => {
		const id = node.owner ?? (node.background ? undefined : ownedBy.get(node.conversationId));
		return id === undefined ? undefined : nodes.get(id);
	};
	const children = new Map<number, Node[]>();
	for (const node of nodes.values()) {
		const owner = parent(node);
		if (owner === undefined || owner.id === node.id) continue;
		const list = children.get(owner.id) ?? [];
		list.push(node);
		children.set(owner.id, list);
	}

	const idleMemo = new Map<number, boolean>();
	const idle = (node: Node, depth = 0): boolean => {
		const known = idleMemo.get(node.id);
		if (known !== undefined) return known;
		if (depth > 256) return false;
		idleMemo.set(node.id, false); // a cycle is not idle
		let result: boolean;
		if (node.inspection.state.kind === "blocked" || options.waitsForPerson(node.kind)) {
			result = true;
		} else {
			const deps = [...(children.get(node.id) ?? [])];
			const state = node.inspection.record.state;
			if (state.status === "waiting") for (const id of state.on) deps.push(...[nodes.get(id as number)].filter((n) => n !== undefined));
			result = deps.length > 0 && deps.every((dep) => idle(dep, depth + 1));
		}
		idleMemo.set(node.id, result);
		return result;
	};

	// Threads with a run in progress.
	const running = new Set<number>();
	for (const conversation of threadConversations) {
		const live = await harness.snapshot(LiveDoc, conversation as never, ctx);
		if (live?.run !== undefined) running.add(conversation);
	}

	const units = new Map<number, Node>();
	const ordinary = new Map<number, Node[]>(); // thread conversation -> the top tasks of its run
	for (const node of nodes.values()) {
		let top = node;
		let boundary: Node | undefined = node.background ? node : undefined;
		for (let depth = 0; depth < 256; depth++) {
			const up = parent(top);
			if (up === undefined || up.id === node.id) break;
			top = up;
			if (boundary === undefined && top.background) boundary = top;
		}
		if (boundary !== undefined) {
			units.set(boundary.id, boundary);
		} else if (running.has(top.conversationId)) {
			if (top.id === node.id) ordinary.set(top.conversationId, [...(ordinary.get(top.conversationId) ?? []), top]);
		} else {
			units.set(top.id, top);
		}
	}

	let busyThreads = 0;
	for (const conversation of running) {
		const tops = ordinary.get(conversation) ?? [];
		// A run with no task (between two) counts as busy, as before; one whose every task waits for a person doesn't.
		if (tops.length === 0 || !tops.every((top) => idle(top))) busyThreads++;
	}
	let waitingForPerson = 0;
	for (const node of nodes.values()) if (options.waitsForPerson(node.kind)) waitingForPerson++;
	return {
		busyThreads,
		waitingForPerson,
		units: [...units.values()].map((unit) => ({
			taskId: unit.id as TaskId,
			kind: unit.kind,
			conversationId: unit.conversationId as ConversationId,
			startedAt: unit.inspection.record.startedAt ?? options.firstSeen.get(unit.id) ?? now,
			idle: idle(unit),
		})),
	};
}

/** The `backgroundWork` field of `/status`. */
export function reportBackground(work: LiveWork): BackgroundWork {
	const active = work.units.filter((unit) => !unit.idle);
	const oldest = active.reduce<number | null>((min, unit) => (min === null || unit.startedAt < min ? unit.startedAt : min), null);
	return {
		tasks: Math.min(active.length, MAX_REPORTED_TASKS),
		oldestStartedAt: oldest,
		waitingForPerson: Math.min(work.waitingForPerson, MAX_REPORTED_TASKS),
	};
}

/**
 * The background work that started at or before `cutoff` (Unix ms) and keeps the agent awake: never a thread's run in
 * progress, and not work that only waits for a person.
 */
export function olderThan(work: LiveWork, cutoff: number): BackgroundUnit[] {
	return work.units.filter((unit) => !unit.idle && unit.startedAt <= cutoff);
}

/** Aborts these units (with everything they own) and waits up to `waitMs` for them to end (abort handlers run first). */
export async function abortBackground(harness: Harness, units: readonly BackgroundUnit[], waitMs: number): Promise<{ aborted: TaskId[]; settled: boolean }> {
	const aborted: TaskId[] = [];
	for (const unit of units) {
		if ((await harness.abortTask(unit.taskId, ctx)) === "marked") aborted.push(unit.taskId);
	}
	if (aborted.length === 0) return { aborted, settled: true };
	const wait = withAbortSignal(AbortSignal.timeout(waitMs), ctx);
	const settled = await Promise.all(aborted.map((id) => harness.waitForTask(id, wait))).then(
		() => true,
		() => false,
	);
	return { aborted, settled };
}
