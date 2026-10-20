import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { type AgentChange, type Conversation, defineDoc, type Harness } from "@earendil-works/pi-durable";

export type ThreadRecord = { threadId: string; conversationId: number; title: string; createdAt: number; updatedAt: number };
type ThreadsState = { threads: Record<string, Omit<ThreadRecord, "threadId">> };

/** Maps client thread IDs to Pi conversation IDs, committed atomically with the conversation it points at. */
export const ThreadsDoc = defineDoc<ThreadsState>({
	kind: "pdp.threads",
	version: 1,
	scope: "session",
	initial: () => ({ threads: {} }),
});

const ctx = BACKGROUND_CONTEXT;

export class ThreadStore {
	private readonly creating = new Map<string, Promise<Conversation>>();

	constructor(
		private readonly harness: Harness,
		private readonly agent: () => AgentChange,
	) {}

	async get(threadId: string): Promise<Conversation | undefined> {
		const record = (await this.harness.snapshot(ThreadsDoc, ctx))?.threads[threadId];
		if (record === undefined) return undefined;
		return this.harness.conversation(record.conversationId as Conversation["id"], ctx);
	}

	/** Get or create; the process owns the storage, so an in-process guard is enough against double creation. */
	async ensure(threadId: string, firstMessage: string): Promise<Conversation> {
		const existing = await this.get(threadId);
		if (existing !== undefined) return existing;
		const pending = this.creating.get(threadId);
		if (pending !== undefined) return pending;
		const created = this.harness
			.createConversation(
				{
					ownership: { kind: "ownerless" },
					agent: this.agent(),
					init: async (tx, conversationId) => {
						const now = Date.now();
						(await tx.doc(ThreadsDoc)).threads[threadId] = { conversationId, title: titleFrom(firstMessage), createdAt: now, updatedAt: now };
					},
				},
				ctx,
			)
			.finally(() => this.creating.delete(threadId));
		this.creating.set(threadId, created);
		return created;
	}

	async touch(threadId: string): Promise<void> {
		await this.harness.commit(async (tx) => {
			const record = (await tx.doc(ThreadsDoc)).threads[threadId];
			if (record !== undefined) record.updatedAt = Date.now();
		}, ctx);
	}

	async remove(threadId: string): Promise<boolean> {
		return this.harness.commit(async (tx) => {
			const threads = (await tx.doc(ThreadsDoc)).threads;
			if (threads[threadId] === undefined) return false;
			delete threads[threadId];
			return true;
		}, ctx);
	}

	/** The thread a conversation belongs to (undefined: none, such as a task's own conversation). */
	async threadOf(conversationId: number): Promise<string | undefined> {
		const state = await this.harness.snapshot(ThreadsDoc, ctx);
		return Object.entries(state?.threads ?? {}).find(([, record]) => record.conversationId === conversationId)?.[0];
	}

	async list(): Promise<ThreadRecord[]> {
		const state = await this.harness.snapshot(ThreadsDoc, ctx);
		return Object.entries(state?.threads ?? {})
			.map(([threadId, record]) => ({ threadId, ...record }))
			.sort((a, b) => b.updatedAt - a.updatedAt);
	}
}

function titleFrom(text: string): string {
	const line = text.trim().split("\n")[0] ?? "";
	if (line.length === 0) return "New chat";
	return line.length > 60 ? `${line.slice(0, 59)}…` : line;
}
