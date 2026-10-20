import type { AssistantMessage } from "@earendil-works/pi-ai";
import { AssistantEntry, type ConversationId } from "@earendil-works/pi-durable";
import { configure, defineExtension, defineTool, type Extension, type ModelRef, Type } from "./facade.ts";

export type SubagentOptions = {
	/** The tool's name (the model calls it) and its extension's. */
	name: string;
	/** What the model is told the subagent does. */
	description: string;
	/** The child's model; absent: the calling conversation's. */
	model?: ModelRef;
	/** The child's instructions, rendered after its sections. */
	instructions?: string;
	/**
	 * The child's extensions, from the calling conversation's resolved selection (this subagent's own extension already
	 * left out, so a child can't start subagents of its own). Absent: all of them, guards included. Leaving out a guard
	 * here runs the child without it.
	 */
	extensions?: (parent: readonly Extension[]) => readonly Extension[];
};

/** What the subagent tool's result carries for apps (`details`), and its running details while the child works. */
export type SubagentDetails = { conversationId: number };

/**
 * A foreground subagent as an extension: a replay-safe tool whose call creates a conversation it owns, gives it the
 * task and returns its answer. Aborting the call aborts the child. After a restart the call reruns, finds the child it
 * created (the ownership index) and the submission it made (the request ID is the call's task), so the child's work is
 * neither repeated nor lost. The child's spend counts in its own conversation's usage, never in the tool's result.
 */
export function defineSubagent(options: SubagentOptions): Extension {
	const own = options.name;
	const tool = defineTool({
		name: options.name,
		description: options.description,
		parameters: Type.Object({ task: Type.String({ description: "What the subagent should do, with everything it needs to know" }) }),
		replay: "safe",
		execute: async (args, api, context) => {
			// Read the caller's selection before the commit: reads inside a commit callback can wait on the commit itself.
			const parent = (await api.agent(context)).extensions.filter((extension) => extension.name !== own);
			const selection = options.extensions ? options.extensions(parent) : parent;
			const child: ConversationId = await api.commit(async (tx) => {
				const existing = (await tx.scanConversations({ ownerTaskId: api.taskId }, 1)).items[0];
				if (existing !== undefined) return existing.id;
				const created = await tx.createConversation({ ownership: { kind: "task", taskId: api.taskId } });
				// An array computed from the caller's resolved selection: an { add, remove } edit would start from the
				// host default instead and could drop what the caller had.
				await configure(tx, created.id, {
					extensions: [...selection],
					...(options.model ? { model: options.model } : {}),
					...(options.instructions !== undefined ? { instructions: options.instructions } : {}),
				});
				return created.id;
			}, context);
			const details: SubagentDetails = { conversationId: child };
			await api.details(details, context);
			const handle = await api.conversation(child, context);
			if (handle === undefined) throw new Error("The subagent's conversation is gone");
			const submission = await handle.submit({ type: "input", content: args.task, requestId: `subagent:${api.taskId}` }, context);
			const settled = await submission.wait(context);
			if (settled.status !== "done" || settled.type !== "input") throw new Error(`The subagent did not answer (${settled.status})`);
			const entry = await api.commit((tx) => tx.entry(AssistantEntry, settled.answer), context);
			const message = entry?.model?.[0] as AssistantMessage | undefined;
			const text = message?.content.flatMap((c) => (c.type === "text" ? [c.text] : [])).join("") ?? "";
			return { output: [{ type: "text", text }], details };
		},
	});
	return defineExtension({ name: own, tools: [tool] });
}
