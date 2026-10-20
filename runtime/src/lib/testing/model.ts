import type { AssistantMessage, ToolCall } from "@earendil-works/pi-ai";
import { createModels, type MutableModels } from "@earendil-works/pi-ai/models";
import {
	type FauxResponseFactory,
	fauxAssistantMessage,
	fauxProvider,
	fauxText,
	fauxToolCall,
} from "@earendil-works/pi-ai/providers/faux";
import type { ModelRef } from "@earendil-works/pi-durable";

/** One scripted model response: a text answer, a whole message, or a function of the request (it may wait). */
export type FakeStep = string | AssistantMessage | FauxResponseFactory;

export type FakeModel = {
	/** Pass to `startAgent(..., { models })`, or to `startTestAgent(..., { model })`. */
	readonly models: MutableModels;
	readonly model: ModelRef;
	/** Queues responses, used in order by every conversation (a subagent's too). */
	reply(...steps: FakeStep[]): void;
	/** Model requests made so far (a request a crash interrupted counts). */
	readonly calls: number;
	/** Responses still queued. */
	readonly pending: number;
};

/**
 * A scripted model on pi-ai's faux provider. Responses are taken from the queue in order; with the queue empty, the
 * model answers `fallback` (default "ok"), so a test never hangs on a missing response. Streams instantly unless
 * `tokensPerSecond` is given.
 */
export function fakeModel(options: { provider?: string; modelId?: string; fallback?: string; tokensPerSecond?: number } = {}): FakeModel {
	const provider = options.provider ?? "fake";
	const modelId = options.modelId ?? "fake-1";
	const faux = fauxProvider({ provider, models: [{ id: modelId, name: modelId }], tokensPerSecond: options.tokensPerSecond ?? 0 });
	const queue: FakeStep[] = [];
	let calls = 0;
	const respond: FauxResponseFactory = async (context, streamOptions, state, model) => {
		faux.appendResponses([respond]);
		calls++;
		const step = queue.shift() ?? options.fallback ?? "ok";
		if (typeof step === "string") return answer(step);
		if (typeof step === "function") return step(context, streamOptions, state, model);
		return step;
	};
	faux.setResponses([respond]);
	const models = createModels();
	models.setProvider(faux.provider);
	return {
		models,
		model: { provider, modelId },
		reply: (...steps) => void queue.push(...steps),
		get calls() {
			return calls;
		},
		get pending() {
			return queue.length;
		},
	};
}

/** A final answer. */
export function answer(text: string): AssistantMessage {
	return fauxAssistantMessage([fauxText(text)]);
}

/** A response that calls one tool (give `id` to recognise the call in the transcript). */
export function toolCall(name: string, args: ToolCall["arguments"] = {}, options: { id?: string } = {}): AssistantMessage {
	return fauxAssistantMessage([fauxToolCall(name, args, options)], { stopReason: "toolUse" });
}

/** A response that waits for the gate before answering with `then` (a crash in the middle of a model request). */
export function afterGate(gate: Gate, then: AssistantMessage | string): FauxResponseFactory {
	return async (_context, streamOptions) => {
		await gate.wait(streamOptions?.signal);
		return typeof then === "string" ? answer(then) : then;
	};
}

/**
 * A point a test controls: code awaits `wait()` until the test calls `open()`. `reached` resolves the first time
 * something waits, so a test knows the agent got there (into a tool, a model request, a subagent) before crashing it.
 * A wait given an abort signal (a tool's `context.abortSignal`) rejects when it is aborted, as when its process dies.
 */
export type Gate = {
	wait(signal?: AbortSignal | { abortSignal?: AbortSignal }): Promise<void>;
	open(): void;
	readonly reached: Promise<void>;
	readonly isOpen: boolean;
};

export function gate(): Gate {
	let opened = false;
	let release!: () => void;
	const released = new Promise<void>((resolve) => (release = resolve));
	let reach!: () => void;
	const reached = new Promise<void>((resolve) => (reach = resolve));
	return {
		wait: (signal) => {
			reach();
			if (opened) return Promise.resolve();
			const abort = signal instanceof AbortSignal ? signal : signal?.abortSignal;
			if (abort === undefined) return released;
			return new Promise<void>((resolve, reject) => {
				if (abort.aborted) return reject(abort.reason ?? new Error("aborted"));
				abort.addEventListener("abort", () => reject(abort.reason ?? new Error("aborted")), { once: true });
				void released.then(resolve);
			});
		},
		open: () => {
			opened = true;
			release();
		},
		reached,
		get isOpen() {
			return opened;
		},
	};
}
