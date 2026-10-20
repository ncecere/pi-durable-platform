/*
 * Private and shared threads: the visibility choice on a shared team's agent (first message only; none in a
 * personal workspace), the private badge and creator in the thread list, message authors and the reply meta
 * line, refused sends, and platform admins' Conversations tab (a reasoned read; auditors don't get the tab).
 */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Schemas } from "../api/client";
import { agent, agentId, auditEntry, meFor, member, myTeam, page, team } from "./fixtures";
import { type Call, type Handler, Reply, mockApi, renderApp, shellRoutes } from "./harness";

type Thread = Schemas["AgentThread"];
type View = Schemas["ChatView"];

const me = { kind: "user" as const, id: "22222222-2222-4222-8222-222222222222", name: "Una User" };
const blair = { kind: "user" as const, id: "33333333-3333-4333-8333-333333333333", name: "Blair Admin" };
const T = Date.now() - 5 * 60_000;

const thread = (extra: Partial<Thread> = {}): Thread => ({
  threadId: "t1",
  title: "Release notes",
  createdAt: T,
  updatedAt: T,
  busy: false,
  lastReplyAt: null,
  lastError: null,
  visibility: "shared",
  createdBy: me,
  participants: [{ ...me, lastSentAt: T }],
  ...extra,
});

/** EventSource stand-in: each thread stream gets a snapshot of the view its thread ID maps to. */
const views = new Map<string, View>();
class FakeEventSource {
  onerror: (() => void) | null = null;
  private listeners: { type: string; fn: (e: { data: string }) => void }[] = [];
  constructor(readonly url: string) {
    setTimeout(() => {
      const id = /threads\/([^/]+)\/events/.exec(url)?.[1] ?? "";
      const view = views.get(decodeURIComponent(id));
      if (!view) return;
      const delta = url.includes("format=delta");
      for (const { type, fn } of this.listeners) {
        if (delta && type === "snapshot") fn({ data: JSON.stringify({ type: "snapshot", seq: 1, v: 1, view }) });
        if (!delta && type === "view") fn({ data: JSON.stringify(view) });
      }
    }, 0);
  }
  addEventListener(type: string, fn: (e: { data: string }) => void) {
    this.listeners.push({ type, fn });
  }
  close() {}
}

beforeEach(() => {
  views.clear();
  vi.stubGlobal("EventSource", FakeEventSource);
});

const made = (calls: Call[], method: string, pattern: RegExp) => calls.filter((c) => c.method === method && pattern.test(c.url));
const messagePosts = (calls: Call[]) => made(calls, "POST", /\/threads\/[^/]+\/messages$/);

/** The agent's test chat for a member, with the threads `list()` returns. */
function chatRoutes(list: () => Thread[], extra: Record<string, Handler> = {}): Record<string, Handler> {
  return {
    ...shellRoutes("none", "owner"),
    [`GET /v1/agents/${agentId}`]: () => agent({ running: true }),
    [`GET /v1/teams/${team.slug}/agents`]: () => [agent({ running: true })],
    [`GET /v1/agents/${agentId}/threads`]: () => list(),
    ...extra,
  };
}

describe("test chat: who sees a new conversation", () => {
  it("sends the chosen visibility with the first message only", async () => {
    const u = userEvent.setup();
    let threads: Thread[] = [];
    const calls = mockApi(chatRoutes(() => threads));
    // Any thread's messages route accepts the send and records the thread as private.
    vi.stubGlobal("fetch", wrapMessages(globalThis.fetch, (threadId) => {
      if (!threads.length) threads = [thread({ threadId, title: "Hello", visibility: "private" })];
      views.set(threadId, { busy: false, tools: [], messages: [{ id: "m1", role: "user", text: "Hello", author: me, at: T }] });
    }));
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=test`);

    const group = await screen.findByRole("group", { name: "Who can see this conversation" });
    expect(within(group).getByRole("button", { name: /Shared/ })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("Everyone who can use this agent sees it")).toBeInTheDocument();
    await u.click(within(group).getByRole("button", { name: /Private/ }));
    expect(screen.getByText("Only you")).toBeInTheDocument();

    await u.type(screen.getByRole("textbox", { name: "Message" }), "Hello");
    await u.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() => expect(messagePosts(calls)).toHaveLength(1));
    expect(messagePosts(calls)[0]!.body).toMatchObject({ text: "Hello", visibility: "private" });

    // The thread exists now: no choice, and no visibility on the next message.
    await waitFor(() => expect(screen.queryByRole("group", { name: "Who can see this conversation" })).toBeNull());
    const header = await screen.findByRole("region", { name: "Helper" });
    expect(await within(header).findByText("Private")).toBeInTheDocument();
    await u.type(screen.getByRole("textbox", { name: "Message" }), "And more");
    await u.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() => expect(messagePosts(calls)).toHaveLength(2));
    expect(messagePosts(calls)[1]!.body).not.toHaveProperty("visibility");
    expect(messagePosts(calls)[1]!.url).toBe(messagePosts(calls)[0]!.url);
  });

  it("offers no choice in a personal workspace and sends none", async () => {
    const u = userEvent.setup();
    const personal: Schemas["Team"] = { ...team, id: "21212121-2121-4212-8212-212121212121", slug: "personal-222222222222", name: "Personal", kind: "personal" };
    const own = agent({ teamId: personal.id, teamSlug: personal.slug, teamName: "Personal", running: true });
    const calls = mockApi({
      ...shellRoutes("none", null),
      "GET /v1/me": () => ({ ...meFor(), teams: [myTeam("owner", { id: personal.id, slug: personal.slug, name: "Personal", kind: "personal" })] }),
      [`GET /v1/teams/${personal.slug}`]: () => ({ team: personal, role: "owner" }),
      [`GET /v1/teams/${personal.slug}/members`]: () => [member("owner")],
      [`GET /v1/teams/${personal.slug}/audit`]: () => page([]),
      [`GET /v1/agents/${agentId}`]: () => own,
      [`GET /v1/agents/${agentId}/threads`]: () => [],
    });
    vi.stubGlobal("fetch", wrapMessages(globalThis.fetch, () => {}));
    renderApp(`/teams/${personal.slug}/agents/${agentId}?tab=test`);
    await u.type(await screen.findByRole("textbox", { name: "Message" }), "Hi");
    expect(screen.queryByRole("group", { name: "Who can see this conversation" })).toBeNull();
    await u.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() => expect(messagePosts(calls)).toHaveLength(1));
    expect(messagePosts(calls)[0]!.body).not.toHaveProperty("visibility");
  });
});

describe("test chat: threads and messages", () => {
  it("marks private threads and says who started a shared one and who's in it", async () => {
    const shared = thread({
      threadId: "t2",
      title: "Deploy plan",
      createdBy: blair,
      participants: [
        { ...blair, lastSentAt: T },
        { ...me, lastSentAt: T },
      ],
    });
    mockApi(chatRoutes(() => [thread({ title: "My notes", visibility: "private" }), shared]));
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=test`);
    const nav = await screen.findByRole("navigation", { name: "Chats" });
    const mine = (await within(nav).findByRole("button", { name: /^My notes/ })).closest("li")!;
    expect(within(mine).getByText("Private")).toBeInTheDocument();
    const theirs = within(nav).getByRole("button", { name: /^Deploy plan/ });
    expect(theirs).toHaveTextContent("Started by Blair Admin");
    expect(theirs).toHaveTextContent("2 people");
    // The most recent thread opens, with its Private badge.
    expect(within(screen.getByRole("region", { name: "Helper" })).getByText("Private")).toBeInTheDocument();
  });

  it("names authors on a thread with several people and shows a quiet meta line under replies", async () => {
    views.set("t1", {
      busy: false,
      tools: [],
      messages: [
        { id: "u1", role: "user", text: "What changed?", author: blair, at: T },
        { id: "u2", role: "user", text: "And for me?", author: { ...me, via: "Slack" }, at: T + 500 },
        {
          id: "a1",
          role: "assistant",
          at: T + 1000,
          toolCalls: [{ id: "c1", name: "search", arguments: {}, startedAt: T + 1200, endedAt: T + 2700 }],
          usage: { input: 900, output: 100, cacheRead: 0, cacheWrite: 0, total: 1000 },
          model: { provider: "openai", id: "fake-one" },
          stopReason: "toolUse",
        },
        { id: "r1", role: "tool", toolCallId: "c1", name: "search", text: "3 results", isError: false },
        {
          id: "a2",
          role: "assistant",
          at: T + 2800,
          text: "Two things changed.",
          usage: { input: 200, output: 34, cacheRead: 0, cacheWrite: 0, total: 234 },
          model: { provider: "openai", id: "fake-one" },
          stopReason: "length",
        },
      ],
    });
    mockApi(
      chatRoutes(() => [
        thread({
          lastReplyAt: T + 4200,
          participants: [
            { ...blair, lastSentAt: T },
            { ...me, lastSentAt: T + 500 },
          ],
        }),
      ]),
    );
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=test`);
    const theirs = await screen.findByRole("article", { name: "Blair Admin said" });
    expect(within(theirs).getByText("Blair Admin")).toBeInTheDocument();
    const time = within(theirs).getByText("5 minutes ago");
    expect(time.tagName).toBe("TIME");
    expect(time).toHaveAttribute("title");
    const mine = screen.getByText("And for me?").closest("article")!;
    expect(within(mine).getByText("You")).toBeInTheDocument();
    expect(within(mine).getByText("via Slack")).toBeInTheDocument();
    expect(screen.getByText("fake-one · 1,234 tokens · 3.2s · Length limit")).toBeInTheDocument();
    expect(screen.getByText("1.5s")).toBeInTheDocument();
    // The header's Shared keeps its icon with the word (one unbreakable label), whatever follows it.
    const shared = within(screen.getByRole("region", { name: "Helper" })).getByText("Shared");
    expect(shared.querySelector("svg")).not.toBeNull();
    expect(shared.parentElement).toHaveTextContent(/^Shared · Started by you · 2 people$/);
  });

  it("leaves your own name out when you're the only one in the thread", async () => {
    views.set("t1", { busy: false, tools: [], messages: [{ id: "u1", role: "user", text: "Just me", author: me, at: T }] });
    mockApi(chatRoutes(() => [thread()]));
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=test`);
    const msg = (await screen.findByText("Just me")).closest("article")!;
    expect(within(msg).queryByText("You")).toBeNull();
    expect(within(msg).getByText("5 minutes ago")).toBeInTheDocument();
  });

  it.each([
    [Reply.error(409, "visibility_fixed"), "This conversation's visibility can't change"],
    [Reply.error(404, "thread_not_found"), "This conversation isn't available"],
  ])("says so and goes back to a new chat when the send is refused (%#)", async (reply, toast) => {
    const u = userEvent.setup();
    views.set("t1", { busy: false, tools: [], messages: [{ id: "u1", role: "user", text: "Earlier", author: me, at: T }] });
    mockApi(chatRoutes(() => [thread()], { [`POST /v1/agents/${agentId}/threads/t1/messages`]: () => reply }));
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=test`);
    await screen.findByText("Earlier");
    await u.type(screen.getByRole("textbox", { name: "Message" }), "More");
    await u.click(screen.getByRole("button", { name: "Send message" }));
    // The toast (and its announcement for screen readers).
    expect((await screen.findAllByText(toast))[0]).toBeInTheDocument();
    expect(await screen.findByText("New test conversation")).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Who can see this conversation" })).toBeInTheDocument();
  });
});

describe("test chat: paging, search and steering", () => {
  it("pages the thread list and searches titles", async () => {
    const u = userEvent.setup();
    const first = Array.from({ length: 3 }, (_, i) => thread({ threadId: `p${i}`, title: `Chat ${i}` }));
    const calls = mockApi(
      chatRoutes(() => [], {
        [`GET /v1/agents/${agentId}/threads`]: (_b, call) => {
          if (call.search.get("q") === "deploy") return [thread({ threadId: "d1", title: "Deploy plan" })];
          if (call.search.get("cursor") === "next") return [thread({ threadId: "p9", title: "Old chat" })];
          return new Reply(200, { data: first, nextCursor: "next" });
        },
      }),
    );
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=test`);
    const nav = await screen.findByRole("navigation", { name: "Chats" });
    await within(nav).findByRole("button", { name: /^Chat 2/ });
    expect(made(calls, "GET", /\/threads$/)[0]!.search.get("limit")).toBe("50");
    await u.click(within(nav).getByRole("button", { name: "Load more chats" }));
    expect(await within(nav).findByRole("button", { name: /^Old chat/ })).toBeInTheDocument();
    expect(within(nav).queryByRole("button", { name: "Load more chats" })).toBeNull();

    await u.type(within(nav).getByRole("searchbox", { name: "Search chats" }), "deploy");
    expect(await within(nav).findByRole("button", { name: /^Deploy plan/ })).toBeInTheDocument();
    expect(within(nav).queryByRole("button", { name: /^Chat 2/ })).toBeNull();
  });

  it("loads earlier messages of a long conversation", async () => {
    const u = userEvent.setup();
    views.set("t1", { busy: false, tools: [], earlierCursor: "cur1", messages: [{ id: "m3", role: "user", text: "Latest question", author: me, at: T }] });
    const calls = mockApi(
      chatRoutes(() => [thread()], {
        [`GET /v1/agents/${agentId}/threads/t1`]: (_b, call): View => {
          expect(call.search.get("before")).toBe("cur1");
          return { busy: false, tools: [], messages: [{ id: "m1", role: "user", text: "First question", author: me, at: T }] };
        },
      }),
    );
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=test`);
    await screen.findByText("Latest question");
    expect(screen.queryByText("First question")).toBeNull();
    await u.click(screen.getByRole("button", { name: "Load earlier messages" }));
    expect(await screen.findByText("First question")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load earlier messages" })).toBeNull();
    expect(made(calls, "GET", /\/threads\/t1$/)).toHaveLength(1);
  });

  it("steers a reply in progress", async () => {
    const u = userEvent.setup();
    views.set("t1", { busy: true, tools: [], partial: { text: "Working on it", thinking: "" }, messages: [{ id: "m1", role: "user", text: "Plan the release", author: me, at: T }] });
    const calls = mockApi(chatRoutes(() => [thread({ busy: true })], { [`POST /v1/agents/${agentId}/threads/t1/messages`]: () => new Reply(202, { data: { submissionId: 2 } }) }));
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=test`);
    const box = await screen.findByRole("textbox", { name: "Message" });
    await screen.findByText("Working on it");
    const steer = screen.getByRole("button", { name: "Steer" });
    expect(steer).toBeDisabled();
    await u.type(box, "Only the backend, please");
    await u.click(steer);
    await waitFor(() => expect(messagePosts(calls)).toHaveLength(1));
    expect(messagePosts(calls)[0]!.body).toMatchObject({ text: "Only the backend, please", whenBusy: "steer" });
    await waitFor(() => expect(box).toHaveValue(""));
  });

  it("queues a follow-up with Enter while a reply runs", async () => {
    const u = userEvent.setup();
    views.set("t1", { busy: true, tools: [], partial: { text: "Working on it", thinking: "" }, messages: [{ id: "m1", role: "user", text: "Plan the release", author: me, at: T }] });
    const calls = mockApi(chatRoutes(() => [thread({ busy: true })], { [`POST /v1/agents/${agentId}/threads/t1/messages`]: () => new Reply(202, { data: { submissionId: 2 } }) }));
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=test`);
    const box = await screen.findByRole("textbox", { name: "Message" });
    await screen.findByText("Working on it");
    await u.type(box, "Then write the notes{Enter}");
    await waitFor(() => expect(messagePosts(calls)).toHaveLength(1));
    // No whenBusy: the platform's default, a follow-up that waits for the reply to end.
    const body = messagePosts(calls)[0]!.body as Record<string, unknown>;
    expect(body).toMatchObject({ text: "Then write the notes" });
    expect(body).not.toHaveProperty("whenBusy");
    await waitFor(() => expect(box).toHaveValue(""));
    // The reply can still be stopped: the button stays Stop.
    expect(screen.getByRole("button", { name: /stop/i })).toBeInTheDocument();
  });
});

describe("Conversations tab for platform staff", () => {
  const personal: Schemas["Team"] = { ...team, id: "21212121-2121-4212-8212-212121212121", slug: "personal-222222222222", name: "Personal", kind: "personal" };
  const own = agent({ teamId: personal.id, teamSlug: personal.slug, teamName: "Personal", running: true });
  const listed: Schemas["AdminAgentThread"] = { ...thread({ createdBy: blair, participants: [{ ...blair, lastSentAt: T }] }), title: null, titleHidden: true, visibility: "private" };
  const staffRoutes = (role: Schemas["PlatformRole"], extra: Record<string, Handler> = {}): Record<string, Handler> => ({
    ...shellRoutes(role, null),
    [`GET /v1/teams/${personal.slug}`]: () => ({ team: personal }),
    [`GET /v1/teams/${personal.slug}/members`]: () => [member("owner")],
    [`GET /v1/teams/${personal.slug}/audit`]: () => page([]),
    [`GET /v1/teams/${personal.slug}/agents`]: () => [own],
    [`GET /v1/agents/${agentId}`]: () => own,
    [`GET /v1/admin/agents/${agentId}/threads`]: () => [listed],
    ...extra,
  });
  const readPath = `/v1/admin/agents/${agentId}/threads/t1/read`;

  it("lets a platform admin read a conversation only with a reason, and shows it read-only", async () => {
    const u = userEvent.setup();
    const calls = mockApi(
      staffRoutes("platform_admin", {
        [`POST ${readPath}`]: (): View => ({ busy: false, tools: [], messages: [{ id: "u1", role: "user", text: "My secret plan", author: blair, at: T }] }),
      }),
    );
    renderApp(`/teams/${personal.slug}/agents/${agentId}?tab=conversations`);
    const table = await screen.findByRole("table", { name: "Conversations with Helper" });
    const row = (await within(table).findByText("Title hidden")).closest("tr")!;
    expect(within(row).getByText("Private")).toBeInTheDocument();
    expect(within(row).getAllByText("Blair Admin").length).toBeGreaterThan(0);

    await u.click(within(row).getByRole("button", { name: "Read the conversation started by Blair Admin…" }));
    const dialog = await screen.findByRole("dialog", { name: "Read this conversation?" });
    expect(within(dialog).getByText(/Recorded in the team's audit log, which the owner can see/)).toBeInTheDocument();
    await u.click(within(dialog).getByRole("button", { name: "Read conversation" }));
    expect(await within(dialog).findByText("Give a reason of at least 10 characters.")).toBeInTheDocument();
    await u.type(within(dialog).getByRole("textbox", { name: "Reason" }), "short");
    await u.click(within(dialog).getByRole("button", { name: "Read conversation" }));
    expect(made(calls, "POST", new RegExp(`${readPath}$`))).toHaveLength(0);

    await u.type(within(dialog).getByRole("textbox", { name: "Reason" }), " of a support ticket 4521");
    await u.click(within(dialog).getByRole("button", { name: "Read conversation" }));
    await waitFor(() => expect(made(calls, "POST", new RegExp(`${readPath}$`))).toHaveLength(1));
    expect(made(calls, "POST", new RegExp(`${readPath}$`))[0]!.body).toEqual({ reason: "short of a support ticket 4521" });
    const snapshot = await screen.findByRole("dialog");
    expect(await within(snapshot).findByText("My secret plan")).toBeInTheDocument();
    expect(within(snapshot).getByText(/Read-only, as of/)).toBeInTheDocument();
    expect(within(snapshot).queryByRole("textbox", { name: "Message" })).toBeNull();
  });

  it("labels a shared thread's hidden title neutrally, leaving Shared to the Visibility column", async () => {
    const shared: Schemas["AdminAgentThread"] = { ...listed, threadId: "t2", visibility: "shared" };
    mockApi(staffRoutes("platform_admin", { [`GET /v1/admin/agents/${agentId}/threads`]: () => [shared] }));
    renderApp(`/teams/${personal.slug}/agents/${agentId}?tab=conversations`);
    const table = await screen.findByRole("table", { name: "Conversations with Helper" });
    const row = (await within(table).findByText("Title hidden")).closest("tr")!;
    expect(within(row).getByText("Shared")).toBeInTheDocument();
    expect(within(table).queryByText(/Private/)).toBeNull();
    expect(screen.getByText(/Titles in a personal workspace are hidden, shared or not/)).toBeInTheDocument();
  });

  it("doesn't offer auditors the Conversations tab (the list is platform admins')", async () => {
    mockApi(staffRoutes("platform_auditor"));
    renderApp(`/teams/${personal.slug}/agents/${agentId}?tab=conversations`);
    expect(await screen.findByText("You're viewing this agent as platform staff")).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: /Conversations/ })).toBeNull();
    expect(screen.queryByRole("table", { name: "Conversations with Helper" })).toBeNull();
  });
});

describe("the team's audit log", () => {
  it("shows a platform admin's read of a private conversation with its reason", async () => {
    const u = userEvent.setup();
    const read = auditEntry({
      id: 202,
      action: "thread.admin_read",
      actor: { kind: "user", userId: "77777777-7777-4777-8777-777777777777", displayName: "Ada Admin", email: "admin@example.com" },
      targetType: "agent_thread",
      targetId: `${agentId}/t1`,
      targetLabel: null,
      before: null,
      after: null,
      metadata: { reason: "Support ticket 4521", visibility: "private", personal: false },
    });
    mockApi({ ...shellRoutes("none", "owner"), [`GET /v1/teams/${team.slug}/audit`]: () => page([read]), [`GET /v1/teams/${team.slug}/audit/202`]: () => read });
    renderApp(`/teams/${team.slug}/settings?tab=audit`);
    const table = await screen.findByRole("table", { name: "Audit log" });
    const row = (await within(table).findByText("Read a private conversation")).closest("tr")!;
    expect(row).toHaveTextContent("Reason: \u201cSupport ticket 4521\u201d");
    expect(within(row).getByRole("link", { name: "Conversation" })).toBeInTheDocument();
    await u.click(within(row).getByText("Read a private conversation"));
    expect(await screen.findByRole("heading", { level: 1, name: "Read a private conversation" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Reason" })).toBeInTheDocument();
  });
});

/** Answers every POST …/threads/{id}/messages (the harness matches exact paths), calling `onSend` with the thread ID. */
function wrapMessages(inner: typeof fetch, onSend: (threadId: string) => void): typeof fetch {
  return (async (input: Request | string, init?: RequestInit) => {
    const req = input instanceof Request ? input : new Request(new URL(String(input), "http://localhost"), init);
    const m = /^\/v1\/agents\/[^/]+\/threads\/([^/]+)\/messages$/.exec(new URL(req.url).pathname);
    const res = await inner(req);
    if (req.method !== "POST" || !m || res.status !== 404) return res;
    onSend(decodeURIComponent(m[1]!));
    return new Response(JSON.stringify({ data: null }), { status: 202, headers: { "Content-Type": "application/json" } });
  }) as typeof fetch;
}
