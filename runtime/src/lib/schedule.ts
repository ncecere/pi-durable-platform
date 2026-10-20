import type { Context } from "@earendil-works/chord/context";
import { defineExtension, defineTool, type Extension, Type } from "./facade.ts";

/**
 * Agent-made schedules (docs/triggers.md): the `schedule`, `list_schedules` and `cancel_schedule` tools let the agent
 * set its own reminders and recurring work. They call the platform's gateway with the agent's own credential
 * (PLATFORM_KEY, as thread status reports do), never a person's; the platform makes a trigger for this agent only,
 * marked as made by it, within its limits, and wakes the agent when it fires. The library adds them to every thread
 * when the platform says the agent's template version allows it (PLATFORM_SCHEDULES=true); the gateway checks again.
 */

/** The extension's name: `pdp` names are the platform's, so no author's extension can take it. */
export const SCHEDULE_EXTENSION = "pdp-schedules";

/** A schedule as the gateway returns it. */
export type AgentSchedule = {
	id: string;
	name: string;
	once: boolean;
	cron?: string;
	timezone: string;
	message: string;
	threadId: string | null;
	originThreadId: string | null;
	nextFireAt: string | null;
	paused: boolean;
	pausedReason?: string;
};

export type ScheduleOptions = {
	/** The platform's gateway (PLATFORM_GATEWAY_URL) and the agent's credential (PLATFORM_KEY). */
	gatewayUrl: string;
	key: string;
	/** The platform's thread ID of a conversation (undefined for one that isn't a thread: a subagent's). */
	threadOf: (conversationId: number) => Promise<string | undefined>;
	fetch?: typeof fetch;
};

/** What a gateway call came to: the answer's data, or the platform's refusal (its message is for the model). */
type Answer<T> = { ok: true; data: T } | { ok: false; message: string };

/** An ISO 8601 date and time (a one-time reminder); anything else is a cron expression. */
const ISO_TIME = /^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}/;

function text(value: string, isError = false) {
	return { output: [{ type: "text" as const, text: value }], ...(isError ? { isError: true } : {}) };
}

/** A schedule in one line, for the model. */
function describe(s: AgentSchedule): string {
	const when = s.once ? "once" : `cron "${s.cron}" (${s.timezone})`;
	const next = s.paused ? `paused${s.pausedReason === "failures" ? " after failed runs" : ""}` : s.nextFireAt ? `next ${s.nextFireAt}` : "firing now";
	const where = s.threadId ? `in conversation ${s.threadId}` : "in a new conversation each time";
	return `- ${s.id} "${s.name}": ${when}, ${next}, ${where}. Message: ${JSON.stringify(s.message.length > 200 ? `${s.message.slice(0, 199)}…` : s.message)}`;
}

/** The schedule tools as an extension (see the module's note). */
export function scheduleExtension(options: ScheduleOptions): Extension {
	const doFetch = options.fetch ?? fetch;
	const base = options.gatewayUrl.replace(/\/$/, "");
	async function call<T>(method: string, path: string, body: unknown, context: Context): Promise<Answer<T>> {
		const signals = [AbortSignal.timeout(15_000)];
		if (context.abortSignal) signals.push(context.abortSignal);
		let res: Response;
		try {
			res = await doFetch(base + path, {
				method,
				headers: { authorization: `Bearer ${options.key}`, "content-type": "application/json" },
				body: body === undefined ? undefined : JSON.stringify(body),
				signal: AbortSignal.any(signals),
			});
		} catch {
			if (context.abortSignal?.aborted) throw context.abortSignal.reason;
			// Never the error's text: it may name the gateway's address.
			return { ok: false, message: "The platform could not be reached. Try again later." };
		}
		const answer = (await res.json().catch(() => undefined)) as { data?: T; error?: { message?: string } } | undefined;
		if (res.ok && answer && "data" in answer) return { ok: true, data: answer.data as T };
		return { ok: false, message: answer?.error?.message ?? `The platform refused (status ${res.status}).` };
	}

	const schedule = defineTool({
		name: "schedule",
		description:
			"Set a reminder or recurring work for yourself: when it fires, the platform wakes you with the message. " +
			"`when` is an ISO 8601 date and time for a one-time reminder (2026-10-21T09:00, in `timezone`, or with an offset) " +
			"or a cron expression for recurring work (five fields: minute hour day month weekday, such as `0 9 * * 1-5`; or @daily, @weekly). " +
			"Write the message to yourself: what to do, with what you will need, since it may start a new conversation. " +
			'`thread`: "this" (default) fires in this conversation when it may take a scheduled message, otherwise in a new one; "new" starts a new conversation each time. ' +
			"The answer says where it will fire. Use get_time first if you need the current time.",
		parameters: Type.Object({
			when: Type.String({ description: "ISO 8601 date and time (one-time) or a cron expression (recurring)" }),
			timezone: Type.Optional(Type.String({ description: "IANA time zone, such as Europe/Paris (default UTC)" })),
			message: Type.String({ description: "What you will be told when it fires" }),
			thread: Type.Optional(Type.Union([Type.Literal("this"), Type.Literal("new")])),
			name: Type.Optional(Type.String({ description: "A short name people will see (optional)" })),
		}),
		execute: async (args, api, context) => {
			const when = args.when.trim();
			const threadId = await options.threadOf(api.conversationId as number);
			const body = {
				...(ISO_TIME.test(when) ? { at: when } : { cron: when }),
				timezone: args.timezone ?? "UTC",
				message: args.message,
				thread: args.thread ?? "this",
				...(args.name ? { name: args.name } : {}),
				...(threadId ? { threadId } : {}),
			};
			const res = await call<{ schedule: AgentSchedule; where: "this" | "new"; whyNotHere?: string }>("POST", "/schedules", body, context);
			if (!res.ok) return text(`Not scheduled: ${res.message}`, true);
			const { schedule: s, where, whyNotHere } = res.data;
			const what = s.once ? `Reminder set for ${s.nextFireAt} (UTC)` : `Recurring schedule set: cron "${s.cron}" (${s.timezone}), first at ${s.nextFireAt} (UTC)`;
			const place =
				where === "this"
					? "It will fire in this conversation."
					: `It will fire in a new conversation${s.once ? "" : " each time"}${whyNotHere ? `, not this one: ${whyNotHere}` : ""}.`;
			return text(`${what}. ${place} ID ${s.id} ("${s.name}"); cancel it with cancel_schedule.`);
		},
	});

	const list = defineTool({
		name: "list_schedules",
		description: "List the reminders and recurring schedules you set yourself, with their IDs.",
		parameters: Type.Object({}),
		replay: "safe",
		execute: async (_args, _api, context) => {
			const res = await call<AgentSchedule[]>("GET", "/schedules", undefined, context);
			if (!res.ok) return text(`Couldn't list schedules: ${res.message}`, true);
			if (res.data.length === 0) return text("You have no schedules.");
			return text(`Your schedules (times in UTC):\n${res.data.map(describe).join("\n")}`);
		},
	});

	const cancel = defineTool({
		name: "cancel_schedule",
		description: "Cancel one of your own reminders or recurring schedules by its ID (from list_schedules).",
		parameters: Type.Object({ id: Type.String({ description: "The schedule's ID" }) }),
		execute: async (args, _api, context) => {
			const res = await call<AgentSchedule>("DELETE", `/schedules/${encodeURIComponent(args.id.trim())}`, undefined, context);
			if (!res.ok) return text(`Not cancelled: ${res.message}`, true);
			return text(`Cancelled "${res.data.name}".`);
		},
	});

	return defineExtension({ name: SCHEDULE_EXTENSION, tools: [schedule, list, cancel] });
}
