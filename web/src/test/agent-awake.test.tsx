/*
 * The agent page says what keeps an awake agent from hibernating (docs/hibernation.md): the agent's `awake`, from its
 * runtime's report. Work waiting for someone's answer is shown, and said not to keep it awake.
 */
import { screen } from "@testing-library/react";
import type { Schemas } from "../api/client";
import { agent, agentId, team } from "./fixtures";
import { mockApi, renderApp, shellRoutes } from "./harness";

beforeEach(() => {
  vi.stubGlobal(
    "EventSource",
    class {
      onerror = null;
      addEventListener() {}
      close() {}
    },
  );
});

function show(extra: Partial<Schemas["Agent"]>) {
  mockApi({
    ...shellRoutes("none", "owner"),
    [`GET /v1/agents/${agentId}`]: () => agent(extra),
    [`GET /v1/agents/${agentId}/threads`]: () => [],
  });
  renderApp(`/teams/${team.slug}/agents/${agentId}`);
}

describe("why an agent is awake", () => {
  it("names a reply, background work and its start, and tasks waiting for someone", async () => {
    show({
      running: true,
      awake: {
        reasons: ["reply", "background"],
        backgroundTasks: 2,
        backgroundSince: new Date(Date.now() - 5 * 60_000).toISOString(),
        waitingForPerson: 1,
        overCeiling: false,
      },
    });
    expect(await screen.findByText(/Kept awake by a reply is in progress, background work \(2 tasks\)\./)).toBeInTheDocument();
    expect(screen.getByText(/The oldest started/)).toBeInTheDocument();
    expect(screen.getByText(/1 task waits for someone's answer\. That doesn't keep it awake/)).toBeInTheDocument();
  });

  it("says when nothing keeps it awake, and when background work ran past the limit", async () => {
    show({ running: true, awake: { reasons: [], backgroundTasks: 0, backgroundSince: null, waitingForPerson: 0, overCeiling: true } });
    expect(await screen.findByText("Nothing keeps it awake: it hibernates shortly.")).toBeInTheDocument();
    expect(screen.getByText(/run past the platform's time limit/)).toBeInTheDocument();
  });

  it("says nothing more for a hibernating agent", async () => {
    show({ running: false });
    expect((await screen.findAllByText("Hibernating")).length).toBeGreaterThan(0);
    expect(screen.queryByText(/Kept awake/)).not.toBeInTheDocument();
  });
});
