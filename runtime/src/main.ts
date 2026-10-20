// The platform's own agent runtime: the library's default composition (the assistant: the template's instructions and
// get_time), run as a process. A custom agent's entry point looks the same with its own definition; see
// docs/custom-agents.md. Environment variables and exit codes: docs/runtime-contract.md.
import { assistantAgent, runAgentProcess } from "@pdp/runtime";

await runAgentProcess(assistantAgent);
