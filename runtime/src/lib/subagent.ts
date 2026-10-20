import type { AssistantMessage } from "@earendil-works/pi-ai";
import type { JsonValue } from "@earendil-works/chord";
import type { Context } from "@earendil-works/chord/context";
import { AssistantEntry, type ConversationHandle, type ConversationId, type EntryId, type TaskId, type Tx } from "@earendil-works/pi-durable";

type ConversationHandleSubmit = ConversationHandle["submit"];
import { configure, defineExtension, defineTask, defineTool, type Extension, type ModelRef, type ToolExecutionApi, Type } from "./facade.ts";
import { SCHEDULE_EXTENSION } from "./schedule.ts";

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
	 * left out, so a child can't start subagents of its own, and the schedule tools, so only the conversation a person
	 * or a trigger talks to sets schedules). Absent: all of them, guards included. Leaving out a guard here runs the
	 * child without it.
	 */
	extensions?: (parent: readonly Extension[]) => readonly Extension[];
	/**
	 * Run the child in the background: the call returns at once, the child works on in a background task (it doesn't
	 * keep the caller's reply busy and survives the caller's stop), and its answer comes back to the calling conversation
	 * as a new message (an entry of kind `subagent.report`, which the model reads like a user message and which starts
	 * a reply when the conversation is idle). A function decides per call (its first answer is kept, so a rerun after a
	 * restart decides the same way): `false` runs that call in the foreground. Default `false`.
	 *
	 * Background work keeps the agent awake up to the platform's ceiling (`AGENTS_BACKGROUND_MAX`), and it runs outside
	 * the reply that started it, so a trigger's run time and budget don't cover it (docs/first-party-agents.md).
	 */
	background?: boolean | ((api: ToolExecutionApi, context: Context) => boolean | Promise<boolean>);
};

/**
 * What the subagent tool's result carries for apps (`details`), and its running details while the child works:
 * the child's conversation, and for a background call `background: true` and the background task that runs it.
 */
export type SubagentDetails = { conversationId: number; background?: true; taskId?: number };

/** The entry kind a background subagent's answer is placed as in the calling conversation. */
export const SUBAGENT_REPORT = "subagent.report";
/** What a `subagent.report` entry's data says: which tool's background task, its child, and whether it answered. */
export type SubagentReport = { tool: string; taskId: number; conversationId: number; answered: boolean };

/** A background call's task: which tool, the calling conversation, the task, the call. Its child is the conversation it owns. */
type RunInput = { tool: string; parent: number; task: string; callId: string };
type RunState = { phase: "run" };
type RunResult = { answered: boolean };

/**
 * A subagent as an extension: a replay-safe tool whose call creates a conversation, gives it the task and returns its
 * answer (foreground), or starts it in a background task whose answer comes back later as a message (`background`).
 * Aborting a foreground call aborts the child. After a restart the call reruns, finds the child it created (the
 * ownership index, or its background task) and the submission it made (the request ID is the call's task), so the
 * child's work is neither repeated nor lost. The child's spend counts in its own conversation's usage, never in the
 * tool's result.
 */
export function defineSubagent(options: SubagentOptions): Extension {
	const own = options.name;
	const runKind = `${own}.background`;

	// The child's selection, computed from the caller's resolved selection, and its configuration. An array: an
	// { add, remove } edit would start from the host default instead and could drop what the caller had.
	const selectionOf = async (api: ToolExecutionApi, context: Context) => {
		const parent = (await api.agent(context)).extensions.filter(
			(extension) => extension.name !== own && extension.name !== SCHEDULE_EXTENSION,
		);
		return options.extensions ? options.extensions(parent) : parent;
	};
	const configureChild = (tx: Tx, child: ConversationId, selection: readonly Extension[]) =>
		configure(tx, child, {
			extensions: [...selection],
			...(options.model ? { model: options.model } : {}),
			...(options.instructions !== undefined ? { instructions: options.instructions } : {}),
		});

	// A background call's child runs here: a background task of the calling conversation that owns the child, gives it
	// the task, waits for its answer and posts it back. Request IDs keep a restart from giving the task or posting the
	// answer twice.
	const Run = defineTask<RunInput, RunState, RunResult>({
		name: runKind,
		version: 1,
		initial: () => ({ phase: "run" }),
		phases: {
			run: async (task, runtime, context) => {
				const { parent, tool } = task.input;
				let answered = false;
				let text: string;
				let child = 0;
				await runtime.commit(async (tx) => {
					child = ((await tx.scanConversations({ ownerTaskId: task.id }, 1)).items[0]?.id as number | undefined) ?? 0;
					return undefined;
				}, context);
				const handle = child === 0 ? undefined : await runtime.conversation(child as ConversationId, context);
				if (handle === undefined) {
					text = "The subagent's conversation is gone.";
				} else {
					const submission = await handle.submit({ type: "input", content: task.input.task, requestId: `subagent:${task.id}` }, context);
					const settled = await submission.wait(context);
					if (settled.status === "done" && settled.type === "input") {
						answered = true;
						text = await answerText(settled.answer, (change) =>
							runtime.commit(async (tx) => {
								await change(tx);
								return undefined;
							}, context),
						);
					} else {
						text = `The subagent did not answer (${settled.status}).`;
					}
				}
				await postBack(runtime, task, child, answered, text, context);
				await runtime.commit(() => ({ status: "terminal", outcome: { status: "completed", result: { answered } } }), context);
			},
		},
		// The child's conversation is the task's: aborting the task aborts it first. The caller is told it ended.
		abort: async (task, runtime, context) => {
			let child = 0;
			await runtime.commit(async (tx) => {
				child = ((await tx.scanConversations({ ownerTaskId: task.id }, 1)).items[0]?.id as number | undefined) ?? 0;
				return undefined;
			}, context);
			await postBack(runtime, task, child, false, "It was stopped before it answered.", context).catch(() => {});
			await runtime.commit(() => ({ status: "terminal", outcome: { status: "aborted", reason: "aborted" } }), context);
		},
	});

	const tool = defineTool({
		name: options.name,
		description: options.description,
		parameters: Type.Object({ task: Type.String({ description: "What the subagent should do, with everything it needs to know" }) }),
		replay: "safe",
		execute: async (args, api, context) => {
			// Read the caller's selection before the commit: reads inside a commit callback can wait on the commit itself.
			const selection = await selectionOf(api, context);
			const wanted = typeof options.background === "function" ? await options.background(api, context) : options.background === true;
			// The first decision is kept: a rerun after a restart goes the same way.
			const background = await api.memo<boolean>("subagent.background", wanted, context);
			if (background) {
				const started = await api.commit(async (tx) => {
					// This call's background task, if an earlier attempt made it.
					for (const record of (await tx.scanTasks({ conversationId: api.conversationId, kind: runKind }, 1000)).items) {
						if ((record.input as RunInput | undefined)?.callId !== api.callId) continue;
						const owned = (await tx.scanConversations({ ownerTaskId: record.id }, 1)).items[0];
						return { taskId: record.id as number, child: (owned?.id as number | undefined) ?? 0 };
					}
					// The task, then its child conversation (owned by it, so aborting the task aborts the child), in one commit.
					const taskId = await tx.createTask(
						Run,
						{ tool: own, parent: api.conversationId as number, task: args.task, callId: api.callId },
						{ ownership: { kind: "conversation" }, conversationId: api.conversationId, background: true },
					);
					const created = await tx.createConversation({ ownership: { kind: "task", taskId: taskId as TaskId } });
					await configureChild(tx, created.id, selection);
					return { taskId: taskId as number, child: created.id as number };
				}, context);
				const details: SubagentDetails = { conversationId: started.child, background: true, taskId: started.taskId };
				return {
					output: [
						{
							type: "text",
							text: `Started in the background (task ${started.taskId}). Its answer will arrive in this conversation as a message starting "[Background subagent ${own}"; don't wait for it.`,
						},
					],
					details,
				};
			}
			const child: ConversationId = await api.commit(async (tx) => {
				const existing = (await tx.scanConversations({ ownerTaskId: api.taskId }, 1)).items[0];
				if (existing !== undefined) return existing.id;
				const created = await tx.createConversation({ ownership: { kind: "task", taskId: api.taskId } });
				await configureChild(tx, created.id, selection);
				return created.id;
			}, context);
			const details: SubagentDetails = { conversationId: child };
			await api.details(details, context);
			const handle = await api.conversation(child, context);
			if (handle === undefined) throw new Error("The subagent's conversation is gone");
			const submission = await handle.submit({ type: "input", content: args.task, requestId: `subagent:${api.taskId}` }, context);
			const settled = await submission.wait(context);
			if (settled.status !== "done" || settled.type !== "input") throw new Error(`The subagent did not answer (${settled.status})`);
			const text = await answerText(settled.answer, (change) => api.commit(change, context));
			return { output: [{ type: "text", text }], details };
		},
	});
	return defineExtension({ name: own, tools: [tool], tasks: [Run] });
}

/** Posts a background call's answer (or why there is none) to the calling conversation, once (its request ID). */
async function postBack(
	runtime: { conversation(id: ConversationId, context: Context): Promise<{ submit: ConversationHandleSubmit } | undefined> },
	task: { id: unknown; input: RunInput },
	child: number,
	answered: boolean,
	text: string,
	context: Context,
): Promise<void> {
	const { tool, parent, callId } = task.input;
	const report: SubagentReport = { tool, taskId: task.id as number, conversationId: child, answered };
	const back = await runtime.conversation(parent as ConversationId, context);
	await back?.submit(
		{
			type: "input",
			content: `[Background subagent ${tool} (call ${callId}) ${answered ? "answered" : "ended"}]\n\n${text}`,
			requestId: `subagent-report:${task.id as number}`,
			entry: { kind: SUBAGENT_REPORT, data: report as unknown as JsonValue },
		},
		context,
	);
}

/** The text of an answer entry, read in a commit (entries of another conversation are read through `tx`). */
async function answerText(entryId: EntryId, commit: (change: (tx: Tx) => Promise<void>) => Promise<unknown>): Promise<string> {
	let text = "";
	await commit(async (tx) => {
		const entry = await tx.entry(AssistantEntry, entryId);
		const message = entry?.model?.[0] as AssistantMessage | undefined;
		text = message?.content.flatMap((c) => (c.type === "text" ? [c.text] : [])).join("") ?? "";
	});
	return text;
}
