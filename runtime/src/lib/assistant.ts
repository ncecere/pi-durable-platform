import { defineAgent } from "./define.ts";
import { defineExtension, defineTool, section, Type } from "./facade.ts";

/** What an agent says when its template gives no instructions. */
export const DEFAULT_INSTRUCTIONS = "You are a helpful, concise assistant. Use get_time for anything about the current date or time.";

const getTime = defineTool({
	name: "get_time",
	description: "Get the current date and time, optionally in an IANA time zone such as Europe/Paris.",
	parameters: Type.Object({ timeZone: Type.Optional(Type.String()) }),
	replay: "safe",
	execute: async (args) => {
		const timeZone = args.timeZone ?? "UTC";
		const text = new Intl.DateTimeFormat("en-US", { dateStyle: "full", timeStyle: "long", timeZone }).format(new Date());
		return { output: [{ type: "text", text: `${text} (${timeZone})` }] };
	},
});

/**
 * The built-in "assistant": the template's instructions as the prompt's preamble, plus side-effect-free tools. Its
 * name, section and tool names are stored in every existing thread's agent selection, so they never change.
 */
export function assistantExtension(instructions: string) {
	return defineExtension({
		name: "assistant",
		tools: [getTime],
		sections: [section("preamble", () => instructions, { tag: false })],
	});
}

/** The platform's own agent: the default composition, built with the same API a custom agent uses. */
export const assistantAgent = defineAgent({
	name: "assistant",
	extensions: ({ instructions }) => [assistantExtension(instructions)],
});
