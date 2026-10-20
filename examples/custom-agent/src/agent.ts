// A starter agent built with @pdp/runtime (docs/custom-agents.md): the platform's assistant (the template's
// instructions and get_time) plus a task board, which is
//
//   - one tool, `board`: add a task, mark one done, or clear the board;
//   - one document, `example.board`: the conversation's tasks, kept next to the transcript and exposed to apps
//     (GET /v1/agents/{agent}/threads/{thread}/documents/example.board, with the access reading the thread needs);
//   - one guard: a `beforeTool` hook that blocks clearing the board (and any MCP tool that deletes), so the model
//     can't do it on its own. Asking a person first (approvals) comes with the platform's approvals (#16).
import { assistantExtension, defineAgent, defineDoc, defineExtension, defineTool, hook, ToolTask, Type } from "@pdp/runtime";

export type Task = { id: string; text: string; done: boolean };
export type Board = { tasks: Task[] };

/**
 * The board. Its kind is persisted protocol: never rename it; change its shape by raising `version` with a `migrate`.
 * `fork: "current"`: a forked conversation starts with the board as it is.
 */
export const BoardDoc = defineDoc<Board>({
	kind: "example.board",
	version: 1,
	scope: "conversation",
	history: "latest",
	fork: "current",
	initial: () => ({ tasks: [] }),
});

/** What the tool returns for apps (`details`: on the tool call in the thread view, never shown to the model). */
export type BoardDetails = { action: "add" | "done" | "clear"; open: number; done: number };

export const boardTool = defineTool({
	name: "board",
	description:
		"Manage this conversation's task board. action add: add the task `text`. action done: mark the task whose id or text is `text` done. action clear: remove every task.",
	parameters: Type.Object({
		action: Type.Union([Type.Literal("add"), Type.Literal("done"), Type.Literal("clear")]),
		text: Type.Optional(Type.String({ description: "The task to add, or the id or text of the task that is done" })),
	}),
	// Replay-safe: after a crash the call runs again, so its effect is keyed by the call (api.callId) and finds itself.
	replay: "safe",
	execute: async ({ action, text }, api, context) => {
		let board: Board = { tasks: [] };
		await api.commit(async (tx) => {
			const doc = await tx.doc(BoardDoc, api.conversationId);
			if (action === "add" && text && !doc.tasks.some((t) => t.id === api.callId)) doc.tasks.push({ id: api.callId, text, done: false });
			if (action === "done") for (const task of doc.tasks) if (task.id === text || task.text === text) task.done = true;
			if (action === "clear") doc.tasks.splice(0);
			board = { tasks: doc.tasks.map((t) => ({ ...t })) };
		}, context);
		const open = board.tasks.filter((t) => !t.done);
		const details: BoardDetails = { action, open: open.length, done: board.tasks.length - open.length };
		const list = open.map((t) => `- ${t.text} (id ${t.id})`).join("\n");
		return { output: [{ type: "text", text: open.length === 0 ? "The board has no open tasks." : `Open tasks:\n${list}` }], details };
	},
});

/** The guard: the model may not clear the board, nor call an MCP tool that deletes. */
export const guard = hook(ToolTask, {
	beforeTool: (call) => {
		if (call.name === "board" && (call.arguments as { action?: unknown }).action === "clear") {
			return { block: "Clearing the board needs a person: ask the user to confirm, and let them clear it." };
		}
		if (call.name.startsWith("mcp") && /delete|remove|drop/i.test(call.name)) return { block: "Deleting needs a person's approval." };
		return undefined;
	},
});

/** The board as one extension: its tool and its guard. The library selects it in every thread, on every message. */
export const boardExtension = defineExtension({ name: "task-board", tools: [boardTool], hooks: [guard] });

export const taskBoardAgent = defineAgent({
	name: "task-board",
	documents: [BoardDoc],
	expose: [{ document: BoardDoc, maxBytes: 32 * 1024 }],
	extensions: ({ instructions }) => [assistantExtension(instructions), boardExtension],
});
