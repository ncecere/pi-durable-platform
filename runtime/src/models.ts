import type { Api, AssistantMessage, Message, Model } from "@earendil-works/pi-ai";
import { anthropicMessagesApi } from "@earendil-works/pi-ai/api/anthropic-messages.lazy";
import { openAICompletionsApi } from "@earendil-works/pi-ai/api/openai-completions.lazy";
import { openAIResponsesApi } from "@earendil-works/pi-ai/api/openai-responses.lazy";
import { createModels, createProvider, type MutableModels } from "@earendil-works/pi-ai/models";
import {
	type FauxResponseFactory,
	fauxAssistantMessage,
	fauxProvider,
	fauxText,
	fauxThinking,
	fauxToolCall,
} from "@earendil-works/pi-ai/providers/faux";
import type { ModelRef } from "@earendil-works/pi-durable";
import { countingUsage, UsageReporter } from "./usage.ts";

/** usage reports the model's usage to the platform, when the platform's proxy does not count it. */
export type AgentModels = { models: MutableModels; model: ModelRef; source: string; usage?: UsageReporter };
type Env = Record<string, string | undefined>;

/**
 * The agent's model, from environment variables the platform injects. The browser never sees them.
 *
 * - `MODEL_PROVIDER=demo` (default): a scripted model, so nothing needs a key.
 * - `MODEL_PROVIDER=catalog`: a model from the platform's catalog. `MODEL_API` (openai-completions,
 *   openai-responses or anthropic-messages), `MODEL_BASE_URL`, `MODEL_API_KEY`, `MODEL_ID`, and optionally
 *   `MODEL_NAME`, `MODEL_PROVIDER_NAME`, `MODEL_CONTEXT_WINDOW`, `MODEL_MAX_TOKENS`, `MODEL_REASONING`, `MODEL_IMAGES`
 *   and `MODEL_HEADERS` (a JSON object). Through the platform's model proxy, the URL is the platform's and the key is
 *   the agent's own credential; in direct mode they are the provider's.
 * - `MODEL_PROVIDER=openai-compatible`: chat completions at `MODEL_BASE_URL` (the platform-wide default model).
 */
export function createAgentModels(env: Env = process.env): AgentModels {
	const models = createModels();
	const provider = (env.MODEL_PROVIDER ?? "demo").trim();
	if (provider === "demo") return { models, model: registerDemoModel(models, env), source: "scripted demo" };
	if (provider === "catalog") return registerModel(models, env, required(env, "MODEL_API"));
	if (provider === "openai-compatible") return registerModel(models, env, "openai-completions");
	throw new Error(`Unknown MODEL_PROVIDER "${provider}". Use demo, catalog or openai-compatible.`);
}

const APIS = {
	"openai-completions": openAICompletionsApi,
	"openai-responses": openAIResponsesApi,
	"anthropic-messages": anthropicMessagesApi,
} as const;
type SupportedApi = keyof typeof APIS;

function registerModel(models: MutableModels, env: Env, api: string): AgentModels {
	if (!(api in APIS)) throw new Error(`Unsupported MODEL_API "${api}". Use ${Object.keys(APIS).join(", ")}.`);
	const baseUrl = required(env, "MODEL_BASE_URL").replace(/\/$/, "");
	const apiKey = required(env, "MODEL_API_KEY");
	const modelId = required(env, "MODEL_ID");
	const host = new URL(baseUrl).host;
	const headers = parseHeaders(env.MODEL_HEADERS);
	const model = {
		id: modelId,
		name: env.MODEL_NAME?.trim() || modelId,
		api: api as SupportedApi,
		provider: "agent-model",
		baseUrl,
		reasoning: env.MODEL_REASONING === "true",
		input: env.MODEL_IMAGES === "true" ? ["text", "image"] : ["text"],
		cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
		contextWindow: positive(env.MODEL_CONTEXT_WINDOW, 128_000),
		maxTokens: positive(env.MODEL_MAX_TOKENS, 16_000),
		...(headers ? { headers } : {}),
		// Compatible servers implement the core of chat completions; some (Open Model Gateway) refuse fields they
		// don't know, such as store.
		...(api === "openai-completions" ? { compat: { supportsDeveloperRole: false, supportsStore: false } } : {}),
	} as Model<Api>;
	const label = env.MODEL_PROVIDER_NAME?.trim() || host;
	const usage = env.MODEL_USAGE_URL && env.MODEL_USAGE_KEY ? new UsageReporter(env.MODEL_USAGE_URL, env.MODEL_USAGE_KEY) : undefined;
	const streams = APIS[api as SupportedApi]();
	models.setProvider(
		createProvider({
			id: "agent-model",
			name: label,
			baseUrl,
			auth: { apiKey: { name: "MODEL_API_KEY", resolve: async () => ({ auth: { apiKey }, source: "MODEL_API_KEY" }) } },
			models: [model],
			api: usage ? countingUsage(streams, usage) : streams,
		}),
	);
	return { models, model: { provider: "agent-model", modelId }, source: `${api} (${label})`, usage };
}

function positive(value: string | undefined, fallback: number): number {
	const n = Number(value);
	return Number.isInteger(n) && n > 0 ? n : fallback;
}

function parseHeaders(raw: string | undefined): Record<string, string> | undefined {
	if (!raw?.trim()) return undefined;
	const parsed: unknown = JSON.parse(raw);
	if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) throw new Error("MODEL_HEADERS must be a JSON object");
	const out: Record<string, string> = {};
	for (const [k, v] of Object.entries(parsed)) if (typeof v === "string") out[k] = v;
	return out;
}

function required(env: Env, name: string): string {
	const value = env[name]?.trim();
	if (!value) throw new Error(`${name} is required for MODEL_PROVIDER=${env.MODEL_PROVIDER}`);
	return value;
}

function registerDemoModel(models: MutableModels, env: Env): ModelRef {
	const faux = fauxProvider({
		provider: "demo",
		models: [{ id: "demo-1", name: "Demo (scripted)", reasoning: true }],
		tokensPerSecond: Number(env.DEMO_TOKENS_PER_SECOND ?? 40),
	});
	const name = env.AGENT_NAME ?? "your assistant";
	const respond: FauxResponseFactory = (context) => {
		faux.appendResponses([respond]);
		return demoReply(context.messages, name);
	};
	faux.setResponses([respond]);
	models.setProvider(faux.provider);
	return { provider: "demo", modelId: "demo-1" };
}

function demoReply(messages: readonly Message[], name: string): AssistantMessage {
	const last = messages.at(-1);
	if (last?.role === "toolResult") {
		const output = last.content.map((part) => (part.type === "text" ? part.text : "")).join("\n").trim();
		return fauxAssistantMessage([fauxText(`The \`${last.toolName}\` tool says: ${output}`)]);
	}
	const text = last?.role === "user" ? (typeof last.content === "string" ? last.content : last.content.map((p) => (p.type === "text" ? p.text : "")).join(" ")) : "";
	if (/\b(time|date|clock)\b/i.test(text)) {
		return fauxAssistantMessage([fauxThinking("The user asked about the time."), fauxToolCall("get_time", {})], { stopReason: "toolUse" });
	}
	const turns = messages.filter((message) => message.role === "user").length;
	return fauxAssistantMessage([
		fauxThinking("No tool needed."),
		fauxText(
			`Hi, I'm ${name}. I'm running on a **scripted demo model**, so this reply is canned.\n\n` +
				`You said: "${text}". This is message ${turns} in this chat. It's stored durably in PostgreSQL, so it survives my pod being restarted or hibernated. Ask me the time to see a tool call.`,
		),
	]);
}
