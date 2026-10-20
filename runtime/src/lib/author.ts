import type { Context } from "@earendil-works/chord/context";
import type { EntryRecord, ToolExecutionApi } from "@earendil-works/pi-durable";
import { type Author, AUTHOR_KINDS, USER_ENTRY } from "./view.ts";

export type { Author };

/** How far back `runAuthor` looks for the message that started the run. */
const LOOK_BACK = 200;

/**
 * Who sent the message that started the run a tool call belongs to, as the platform recorded it (never as a client
 * said): a person (`user`), a service account, or a `trigger` (a schedule or webhook: an unattended run,
 * docs/triggers.md). Undefined when nobody is recorded: a message the platform didn't author, a background subagent's
 * report, a subagent's own conversation (its task comes from its caller), or a run that started more than 200 entries
 * back. A steer that joined the run doesn't change it. Reads the transcript in a commit that writes nothing.
 */
export async function runAuthor(api: Pick<ToolExecutionApi, "conversationId" | "commit">, context: Context): Promise<Author | undefined> {
	let entries: readonly EntryRecord[] = [];
	await api.commit(async (tx) => {
		entries = (await tx.scanEntries({ conversationId: api.conversationId, order: "descending" }, LOOK_BACK)).items;
	}, context);
	let starter: Author | undefined;
	let found = false;
	for (const entry of entries) {
		if (entry.kind === "pi.system" || entry.kind === "pi.compaction") continue;
		const model = entry.model ?? [];
		if (model.some((m) => m.role === "user")) {
			found = true;
			starter = entry.kind === USER_ENTRY ? authorOf(entry.data) : undefined;
			continue;
		}
		const final = model.some((m) => m.role === "assistant" && (m as { stopReason?: string }).stopReason !== "toolUse");
		if (final && found) break;
	}
	return starter;
}

function authorOf(data: unknown): Author | undefined {
	if (typeof data !== "object" || data === null) return undefined;
	const author = (data as { author?: unknown }).author;
	if (typeof author !== "object" || author === null) return undefined;
	const { kind, id, name, via } = author as Record<string, unknown>;
	if (typeof kind !== "string" || !AUTHOR_KINDS.has(kind) || typeof id !== "string" || typeof name !== "string") return undefined;
	return { kind: kind as Author["kind"], id, name, ...(typeof via === "string" ? { via } : {}) };
}
