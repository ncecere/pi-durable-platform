// The agent's process: what the local launcher (AGENTS_RUNTIME_DIR) and the image (Dockerfile) run. Its environment
// and exit codes are the runtime contract's (docs/runtime-contract.md).
import { runAgentProcess } from "@pdp/runtime";
import { taskBoardAgent } from "./agent.ts";

await runAgentProcess(taskBoardAgent);
