// The platform's own agent runtime, run as a process: the library's default composition (the assistant: the template's
// instructions and get_time), or the first-party kind the template version names (AGENT_KIND: triage, researcher;
// docs/first-party-agents.md). A custom agent's entry point looks the same with its own definition; see
// docs/custom-agents.md. Environment variables and exit codes: docs/runtime-contract.md.
import { runAgentProcess } from "@pdp/runtime";
import { agentFromEnv } from "@pdp/runtime/kinds";

await runAgentProcess(agentFromEnv);
