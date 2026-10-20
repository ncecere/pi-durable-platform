import { Type } from "@earendil-works/pi-ai";
import { defineExtension, defineTool, section } from "@earendil-works/pi-durable";

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

/** The built-in "assistant" template: a prompt plus side-effect-free tools. */
export function assistantExtension(instructions: string) {
	return defineExtension({
		name: "assistant",
		tools: [getTime],
		sections: [section("preamble", () => instructions, { tag: false })],
	});
}
