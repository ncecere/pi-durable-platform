import type { ConversationDocToken, ConversationRecord, Extension, HarnessSettings, JsonObject, SessionDocToken, Tx } from "./facade.ts";

/** What the platform tells an agent about itself when it starts. */
export type AgentContext = {
	readonly agentId: string;
	readonly name: string;
	/** The template's instructions (or the default). Render them in a section, as `assistantExtension` does, or not. */
	readonly instructions: string;
	/**
	 * The variables the platform passed (MODEL_*, MCP_*, PLATFORM_*). They hold credentials: never log them, and never
	 * send them anywhere but where the platform meant them to go.
	 */
	readonly env: Readonly<Record<string, string | undefined>>;
};

/** A document an agent declares: created in every new conversation (conversation scope) or once (session scope). */
export type AgentDocument = SessionDocToken<JsonObject> | ConversationDocToken<JsonObject>;

/** Run policy for every conversation. The extension selection is the library's (see AgentDefinition.extensions). */
export type AgentSettings = Omit<HarnessSettings, "extensions">;

/**
 * A custom agent: what it adds to the platform's runtime. Everything is optional; `defineAgent({})` is an agent with
 * no tools or prompt of its own (MCP servers still reach it).
 */
export type AgentDefinition = {
	/** For logs. */
	readonly name?: string;
	/**
	 * The agent's extensions: tools, prompt sections, hooks (guards, approvals), wraps and tasks. The library selects
	 * all of them, in this order, in every thread on every message, before the MCP servers' extensions, so a guard can't
	 * be left out of a thread. A function receives the agent's context (its template's instructions, for example).
	 */
	readonly extensions?: readonly Extension[] | ((context: AgentContext) => readonly Extension[]);
	/** Documents to create in every conversation (conversation scope) or once at start (session scope). */
	readonly documents?: readonly AgentDocument[];
	/** Runs in every commit that creates or forks a conversation, after the declared documents are created. */
	readonly conversationCreated?: (tx: Tx, conversation: ConversationRecord) => void | Promise<void>;
	/** Run policy (retry, compaction, progress, tool execution, queue modes, ...); read at every use. */
	readonly settings?: AgentSettings | ((context: AgentContext) => AgentSettings);
};

/** Extension names the platform's runtime installs itself. */
const RESERVED_EXTENSIONS = new Set(["mcp", "tool-search"]);
const RESERVED_EXTENSION_PREFIXES = ["mcp-thread:", "pdp", "pi."];
/** Tool name prefixes of the platform's MCP tools (template servers and app servers). */
const RESERVED_TOOL_PREFIXES = ["mcp__", "mcpc__"];
/** Document, entry and task names that belong to pi-durable (`pi.`) and to the platform (`pdp.`). */
const RESERVED_KIND_PREFIXES = ["pi.", "pdp."];

/** An agent definition the library refuses, with what is wrong. */
export class AgentDefinitionError extends Error {
	constructor(message: string) {
		super(message);
		this.name = "AgentDefinitionError";
	}
}

/** Checks a definition's static parts and returns it. Extensions given as a function are checked when the agent starts. */
export function defineAgent(definition: AgentDefinition): AgentDefinition {
	if (typeof definition !== "object" || definition === null) throw new AgentDefinitionError("an agent definition is an object");
	for (const doc of definition.documents ?? []) checkDocument(doc);
	if (Array.isArray(definition.extensions)) checkExtensions(definition.extensions as readonly Extension[]);
	if (typeof definition.settings === "object" && definition.settings !== null && "extensions" in definition.settings) {
		throw new AgentDefinitionError("settings.extensions is the library's: list extensions in the definition's extensions");
	}
	return definition;
}

/** The extensions of a definition for one agent, checked. */
export function resolveExtensions(definition: AgentDefinition, context: AgentContext): readonly Extension[] {
	const extensions = typeof definition.extensions === "function" ? definition.extensions(context) : (definition.extensions ?? []);
	checkExtensions(extensions);
	return extensions;
}

/** The settings of a definition for one agent, without an extension selection. */
export function resolveSettings(definition: AgentDefinition, context: AgentContext): AgentSettings {
	const settings = typeof definition.settings === "function" ? definition.settings(context) : (definition.settings ?? {});
	if ("extensions" in settings) throw new AgentDefinitionError("settings.extensions is the library's: list extensions in the definition's extensions");
	return settings;
}

function checkExtensions(extensions: readonly Extension[]): void {
	const names = new Set<string>();
	for (const extension of extensions) {
		if (typeof extension?.name !== "string" || extension.name === "") throw new AgentDefinitionError("every extension needs a name");
		const name = extension.name;
		if (RESERVED_EXTENSIONS.has(name) || RESERVED_EXTENSION_PREFIXES.some((p) => name.startsWith(p))) {
			throw new AgentDefinitionError(`extension name ${JSON.stringify(name)} is reserved for the platform`);
		}
		// Installing a second extension under a name replaces the first: one of them would silently not run.
		if (names.has(name)) throw new AgentDefinitionError(`two extensions are named ${JSON.stringify(name)}`);
		names.add(name);
		for (const tool of extension.tools ?? []) {
			if (RESERVED_TOOL_PREFIXES.some((p) => tool.name.startsWith(p))) {
				throw new AgentDefinitionError(`tool name ${JSON.stringify(tool.name)} (extension ${name}) uses a prefix reserved for MCP tools`);
			}
		}
		for (const task of extension.tasks ?? []) {
			const kind = task.definition.name;
			if (RESERVED_KIND_PREFIXES.some((p) => kind.startsWith(p))) {
				throw new AgentDefinitionError(`task name ${JSON.stringify(kind)} (extension ${name}) is reserved (pi. and pdp. names belong to pi-durable and the platform)`);
			}
		}
	}
}

function checkDocument(doc: AgentDocument): void {
	const definition = doc?.definition;
	if (definition === undefined || typeof definition.kind !== "string") throw new AgentDefinitionError("documents must be defined with defineDoc");
	const kind = definition.kind;
	if (RESERVED_KIND_PREFIXES.some((p) => kind.startsWith(p))) {
		throw new AgentDefinitionError(`document kind ${JSON.stringify(kind)} is reserved (pi. and pdp. names belong to pi-durable and the platform)`);
	}
	const scope: string = definition.scope;
	if (scope !== "session" && scope !== "conversation") throw new AgentDefinitionError(`document ${kind}: only session and conversation documents can be declared`);
	if ((definition as { family?: true }).family === true) throw new AgentDefinitionError(`document ${kind}: families can't be declared`);
}

/** Creates the declared documents of one new conversation (conversation scope), inside its creating commit. */
export async function createConversationDocuments(tx: Tx, conversation: ConversationRecord, documents: readonly AgentDocument[]): Promise<void> {
	for (const doc of documents) {
		if (doc.definition.scope === "conversation") await tx.doc(doc as ConversationDocToken<JsonObject>, conversation.id);
	}
}

/** Creates the declared session documents (once; an existing one is kept). */
export async function createSessionDocuments(tx: Tx, documents: readonly AgentDocument[]): Promise<void> {
	for (const doc of documents) {
		if (doc.definition.scope === "session") await tx.doc(doc as SessionDocToken<JsonObject>);
	}
}
