import type { ProviderStreams, Usage } from "@earendil-works/pi-ai";

type Counts = { requests: number; input: number; output: number; cacheRead: number; cacheWrite: number };
const empty = (): Counts => ({ requests: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0 });

/**
 * Reports model usage to the platform (MODEL_USAGE_URL, with MODEL_USAGE_KEY), for models the platform's proxy does
 * not see: a direct-mode provider, or the platform's default model. Counts are batched and sent a moment after a
 * response ends; a failed send is retried with the next batch, a few times at most. Reporting is best effort: it never
 * delays or fails the agent's work.
 */
export class UsageReporter {
	private pending = empty();
	private timer: ReturnType<typeof setTimeout> | undefined;
	private sending: Promise<void> = Promise.resolve();
	private failures = 0;

	constructor(
		private readonly url: string,
		private readonly key: string,
		private readonly delayMs = 2000,
		private readonly fetchImpl: typeof fetch = fetch,
	) {}

	add(usage: Usage | undefined): void {
		if (!usage) return;
		const p = this.pending;
		p.requests += 1;
		p.input += usage.input || 0;
		p.output += usage.output || 0;
		p.cacheRead += usage.cacheRead || 0;
		p.cacheWrite += usage.cacheWrite || 0;
		this.timer ??= setTimeout(() => void this.flush(), this.delayMs);
	}

	/** Sends what is pending now (also when the agent stops). */
	flush(): Promise<void> {
		if (this.timer) clearTimeout(this.timer);
		this.timer = undefined;
		this.sending = this.sending.then(() => this.send());
		return this.sending;
	}

	private async send(): Promise<void> {
		const batch = this.pending;
		if (batch.requests === 0) return;
		this.pending = empty();
		try {
			const res = await this.fetchImpl(this.url, {
				method: "POST",
				headers: { authorization: `Bearer ${this.key}`, "content-type": "application/json" },
				body: JSON.stringify(batch),
				signal: AbortSignal.timeout(10_000),
			});
			if (!res.ok && res.status >= 500) throw new Error(`status ${res.status}`);
			this.failures = 0;
		} catch (error) {
			// Keep the counts for the next attempt, unless the platform has been unreachable for a while.
			if (++this.failures <= 5) {
				for (const k of Object.keys(batch) as (keyof Counts)[]) this.pending[k] += batch[k];
				this.timer ??= setTimeout(() => void this.flush(), this.delayMs * 5);
			} else {
				console.error("[runtime] dropping unreported usage:", (error as Error).message);
				this.failures = 0;
			}
		}
	}
}

/** Wraps a model API so every finished response (also a failed one) is counted. */
export function countingUsage(streams: ProviderStreams, reporter: UsageReporter): ProviderStreams {
	const tap = <S extends { result(): Promise<{ usage?: Usage }> }>(stream: S): S => {
		stream.result().then(
			(message) => reporter.add(message.usage),
			() => {},
		);
		return stream;
	};
	return {
		stream: (model, context, options) => tap(streams.stream(model, context, options)),
		streamSimple: (model, context, options) => tap(streams.streamSimple(model, context, options)),
		...(streams.fetchDeferred
			? { fetchDeferred: (model, handle, options) => tap(streams.fetchDeferred!(model, handle, options)) }
			: {}),
		...(streams.cancelDeferred ? { cancelDeferred: streams.cancelDeferred.bind(streams) } : {}),
	};
}
