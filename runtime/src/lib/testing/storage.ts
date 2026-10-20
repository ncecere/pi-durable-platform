import type { Context } from "@earendil-works/chord/context";
import { MemoryStorage, type Storage } from "@earendil-works/pi-durable";

/** The process holding this storage died: nothing it does reaches storage any more. */
export class StorageCrashed extends Error {
	constructor() {
		super("the process holding this storage crashed (test kit)");
		this.name = "StorageCrashed";
	}
}

/**
 * In-memory storage that survives a simulated crash: what was committed stays; the crashed process's handle rejects
 * everything from then on, as a dead process's connection would, and a new handle (a restarted process) reads what was
 * committed. One handle at a time, like the ownership rule of the platform's PostgreSQL storage.
 */
export class TestStorage {
	private readonly inner = new MemoryStorage();
	private current: CrashableStorage | undefined;

	/** A handle for a (re)started process. */
	open(): Storage {
		if (this.current !== undefined && !this.current.dead) throw new Error("the storage is still open: crash or close the agent first");
		this.current = new CrashableStorage(this.inner);
		return this.current;
	}

	/** The current handle's process dies now. */
	crash(): void {
		this.current?.kill();
	}
}

export function memoryStorage(): TestStorage {
	return new TestStorage();
}

/** Delegates to the shared storage until killed or closed; never closes the shared storage. */
class CrashableStorage implements Storage {
	dead = false;
	constructor(private readonly inner: MemoryStorage) {}

	kill(): void {
		this.dead = true;
	}

	private live<T>(call: () => Promise<T>): Promise<T> {
		if (this.dead) return Promise.reject(new StorageCrashed());
		return call();
	}

	commit: Storage["commit"] = (writes, context) => this.live(() => this.inner.commit(writes, context));
	mintId: Storage["mintId"] = () => this.live(() => this.inner.mintId());
	conversation: Storage["conversation"] = (id, context) => this.live(() => this.inner.conversation(id, context));
	scanConversations: Storage["scanConversations"] = (query, limit, cursor, context) =>
		this.live(() => this.inner.scanConversations(query, limit, cursor, context));
	entry = ((...args: Parameters<MemoryStorage["entry"]>) => this.live(() => (this.inner.entry as (...a: unknown[]) => Promise<unknown>)(...args))) as Storage["entry"];
	findLatestHeadMarker: Storage["findLatestHeadMarker"] = (conversationId, at, context) =>
		this.live(() => this.inner.findLatestHeadMarker(conversationId, at, context));
	scanEntries: Storage["scanEntries"] = (query, limit, cursor, context) => this.live(() => this.inner.scanEntries(query, limit, cursor, context));
	task: Storage["task"] = (id, context) => this.live(() => this.inner.task(id, context));
	scanTasks: Storage["scanTasks"] = (query, limit, cursor, context) => this.live(() => this.inner.scanTasks(query, limit, cursor, context));
	submission: Storage["submission"] = (id, context) => this.live(() => this.inner.submission(id, context));
	scanSubmissions: Storage["scanSubmissions"] = (query, limit, cursor, context) =>
		this.live(() => this.inner.scanSubmissions(query, limit, cursor, context));
	submissionByRequest: Storage["submissionByRequest"] = (conversationId, requestId, context) =>
		this.live(() => this.inner.submissionByRequest(conversationId, requestId, context));
	findDocument: Storage["findDocument"] = (address, at, context) => this.live(() => this.inner.findDocument(address, at, context));
	document: Storage["document"] = (id, at, context) => this.live(() => this.inner.document(id, at, context));
	scanDocuments: Storage["scanDocuments"] = (query, limit, cursor, context) => this.live(() => this.inner.scanDocuments(query, limit, cursor, context));

	async close(_context: Context): Promise<void> {
		this.dead = true;
	}
}
