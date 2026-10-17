import type { AssistantMessage, Message, Model } from "@earendil-works/pi-ai";
import { openAICompletionsApi } from "@earendil-works/pi-ai/api/openai-completions.lazy";
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

export type AgentModels = { models: MutableModels; model: ModelRef; source: string };
type Env = Record<string, string | undefined>;

/**
 * The agent's model, from environment variables the platform injects. The browser never sees them.
 *
 * - `MODEL_PROVIDER=demo` (default): a scripted model, so nothing needs a key.
 * - `MODEL_PROVIDER=openai-compatible`: `MODEL_BASE_URL`, `MODEL_API_KEY`, `MODEL_ID` (LiteLLM, vLLM, …).
 */
export function createAgentModels(env: Env = process.env): AgentModels {
	const models = createModels();
	const provider = (env.MODEL_PROVIDER ?? "demo").trim();
	if (provider === "demo") return { models, model: registerDemoModel(models, env), source: "scripted demo" };
	if (provider === "openai-compatible") {
		const baseUrl = required(env, "MODEL_BASE_URL").replace(/\/$/, "");
		const apiKey = required(env, "MODEL_API_KEY");
		const modelId = required(env, "MODEL_ID");
		const model: Model<"openai-completions"> = {
			id: modelId,
			name: modelId,
			api: "openai-completions",
			provider: "proxy",
			baseUrl,
			reasoning: env.MODEL_REASONING === "true",
			input: ["text"],
			cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
			contextWindow: Number(env.MODEL_CONTEXT_WINDOW ?? 128_000),
			maxTokens: Number(env.MODEL_MAX_TOKENS ?? 16_000),
			compat: { supportsDeveloperRole: false },
		};
		models.setProvider(
			createProvider({
				id: "proxy",
				name: `OpenAI-compatible (${new URL(baseUrl).host})`,
				baseUrl,
				auth: { apiKey: { name: "MODEL_API_KEY", resolve: async () => ({ auth: { apiKey }, source: "MODEL_API_KEY" }) } },
				models: [model],
				api: openAICompletionsApi(),
			}),
		);
		return { models, model: { provider: "proxy", modelId }, source: `openai-compatible (${new URL(baseUrl).host})` };
	}
	throw new Error(`Unknown MODEL_PROVIDER "${provider}". Use demo or openai-compatible.`);
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
