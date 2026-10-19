/* The OAuth Connect consent page: the app (verified, trusted or not), what it can do, where the browser goes back to, Allow and Deny, invalid requests and signing in first. */
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import { rawQuery, redirectTo, safeRedirect } from "../pages/connect/consent";
import { authConfig } from "./fixtures";
import { oauthClient, oauthConsent, registeredClient } from "./fixtures-oauth";
import { type Handler, Reply, mockApi, renderApp, shellRoutes } from "./harness";

const query =
  "response_type=code&client_id=https%3A%2F%2Fchat.example.com%2Foauth%2Fclient-metadata.json&redirect_uri=https%3A%2F%2Fchat.example.com%2Foauth%2Fcallback&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256&scope=agents&state=123";
const path = `/connect/consent?${query}`;
const back = "https://chat.example.com/oauth/callback?code=x&state=123&iss=https%3A%2F%2Fagents.example.com";

function openConsent(routes: Record<string, Handler>) {
  // The page reads the address the browser was sent to, as it arrived.
  window.history.replaceState({}, "", path);
  const calls = mockApi({ ...shellRoutes(), ...routes });
  return { calls, ...renderApp(path) };
}

afterEach(() => {
  vi.restoreAllMocks();
  window.history.replaceState({}, "", "/");
});

describe("OAuth consent page", () => {
  it("names a verified app, its host, what it can do and where the browser goes back to, without the app's shell", async () => {
    const go = vi.spyOn(redirectTo, "go").mockImplementation(() => {});
    const { calls, container } = openConsent({ "GET /v1/oauth/consent": () => oauthConsent() });
    const title = "Team Chat wants to use your agents as you";
    expect(await screen.findByRole("heading", { level: 1, name: title })).toBeInTheDocument();
    expect(screen.getByRole("main", { name: title })).toBeInTheDocument();
    expect(screen.queryByText("Unverified")).toBeNull();
    expect(screen.queryByText("Trusted")).toBeNull();
    const can = screen.getByRole("region", { name: "It will be able to" });
    expect(can).toHaveTextContent("See your agents and chat with them");
    expect(can).toHaveTextContent("Create agents in your personal workspace (Personal) and in teams where you can");
    expect(can).toHaveTextContent("Never with admin powers");
    expect(screen.getByText(/You'll be sent back to/)).toHaveTextContent("You'll be sent back to chat.example.com.");
    expect(screen.getByText("una@example.com")).toBeInTheDocument();
    // No sidebar to wander off to.
    expect(screen.queryByRole("navigation", { name: "Main" })).toBeNull();
    await waitFor(() => expect(document.title).toBe("Connect Team Chat · Pi Durable Platform"));
    // The query goes to the server exactly as received.
    const get = calls.find((c) => c.method === "GET" && c.url === "/v1/oauth/consent")!;
    expect(get.search.toString()).toBe(query);
    expect(go).not.toHaveBeenCalled();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("badges a trusted app and leaves out the personal workspace when there is none", async () => {
    openConsent({ "GET /v1/oauth/consent": () => oauthConsent({ client: oauthClient({ trusted: true }), personalWorkspace: undefined }) });
    await screen.findByRole("heading", { level: 1 });
    expect(screen.getByText("Trusted")).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "It will be able to" })).toHaveTextContent("Create agents in teams where you can");
  });

  it("marks an app that registered itself as Unverified and says the platform can't verify who made it", async () => {
    const { container } = openConsent({ "GET /v1/oauth/consent": () => oauthConsent({ client: registeredClient() }) });
    expect(await screen.findByRole("heading", { level: 1, name: "Handy Helper wants to use your agents as you" })).toBeInTheDocument();
    expect(screen.getByText("Unverified")).toBeInTheDocument();
    expect(screen.getByText("It registered itself and says it's from helper.example.net")).toBeInTheDocument();
    expect(screen.getByText(/can't verify who made it/)).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("loads an app's logo through the platform, never from the app's own address", async () => {
    const { unmount, container } = openConsent({ "GET /v1/oauth/consent": () => oauthConsent({ client: oauthClient({ logoUrl: "https://chat.example.com/logo.png" }) }) });
    await screen.findByRole("heading", { level: 1 });
    expect(container.querySelector("main img")).toHaveAttribute(
      "src",
      "/v1/oauth/client-logo?client_id=https%3A%2F%2Fchat.example.com%2Foauth%2Fclient-metadata.json",
    );
    unmount();
    const other = openConsent({ "GET /v1/oauth/consent": () => oauthConsent({ client: oauthClient({ logoUrl: "http://tracker.example.net/pixel.png" }) }) });
    await screen.findByRole("heading", { level: 1 });
    expect(other.container.querySelector("main img")).toBeNull();
  });

  it("falls back to the app's initials when its logo can't be loaded", async () => {
    const { container } = openConsent({ "GET /v1/oauth/consent": () => oauthConsent({ client: registeredClient({ logoUrl: "https://helper.example.net/logo.png" }) }) });
    await screen.findByRole("heading", { level: 1 });
    const img = container.querySelector("main img")!;
    expect(img).toHaveAttribute("src", "/v1/oauth/client-logo?client_id=pdpcl_3f9a1c");
    fireEvent.error(img);
    await waitFor(() => expect(container.querySelector("main img")).toBeNull());
    expect(container.querySelector("main")).toHaveTextContent("HH");
  });

  it("allows: posts the query string as received, with the CSRF token, then sends the browser to the app", async () => {
    const u = userEvent.setup();
    const go = vi.spyOn(redirectTo, "go").mockImplementation(() => {});
    const { calls } = openConsent({ "GET /v1/oauth/consent": () => oauthConsent(), "POST /v1/oauth/consent": () => ({ redirectUrl: back }) });
    await u.click(await screen.findByRole("button", { name: "Allow" }));
    await waitFor(() => expect(go).toHaveBeenCalledWith(back));
    const post = calls.find((c) => c.method === "POST" && c.url === "/v1/oauth/consent")!;
    expect(post.body).toEqual({ query, decision: "allow" });
    expect(post.headers.get("X-CSRF-Token")).toBe("csrf-123");
    expect(screen.getByRole("status")).toHaveTextContent("Taking you back to chat.example.com…");
    // Answered: neither button can send a second answer.
    expect(screen.getByRole("button", { name: "Allow" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Deny" })).toBeDisabled();
  });

  it("denies: posts deny and sends the browser back with the app's answer", async () => {
    const u = userEvent.setup();
    const denied = "https://chat.example.com/oauth/callback?error=access_denied&state=123";
    const go = vi.spyOn(redirectTo, "go").mockImplementation(() => {});
    const { calls } = openConsent({ "GET /v1/oauth/consent": () => oauthConsent(), "POST /v1/oauth/consent": () => ({ redirectUrl: denied }) });
    await u.click(await screen.findByRole("button", { name: "Deny" }));
    await waitFor(() => expect(go).toHaveBeenCalledWith(denied));
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ query, decision: "deny" });
  });

  it("explains an invalid request and never redirects (400 oauth_*)", async () => {
    const go = vi.spyOn(redirectTo, "go").mockImplementation(() => {});
    const { calls, container } = openConsent({
      "GET /v1/oauth/consent": () => Reply.error(400, "oauth_invalid_request", "The redirect_uri isn't one the client registered."),
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Can't connect this app" })).toBeInTheDocument();
    expect(screen.getByText("The app's request isn't valid, so it can't be answered.")).toBeInTheDocument();
    expect(screen.getByText("The redirect_uri isn't one the client registered.")).toBeInTheDocument();
    expect(screen.getByText(/Nothing was shared with the app, and this page won't send you anywhere/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Allow" })).toBeNull();
    await waitFor(() => expect(document.title).toBe("Can't connect this app · Pi Durable Platform"));
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    expect(go).not.toHaveBeenCalled();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("says when the request isn't found (404), and never redirects", async () => {
    const go = vi.spyOn(redirectTo, "go").mockImplementation(() => {});
    openConsent({ "GET /v1/oauth/consent": () => Reply.error(404, "not_found", "Not found") });
    expect(await screen.findByText(/This connection request wasn't found/)).toBeInTheDocument();
    expect(go).not.toHaveBeenCalled();
  });

  it("refuses an answer that would send the browser anywhere but http(s), and keeps the person here", async () => {
    const u = userEvent.setup();
    const go = vi.spyOn(redirectTo, "go").mockImplementation(() => {});
    openConsent({ "GET /v1/oauth/consent": () => oauthConsent(), "POST /v1/oauth/consent": () => ({ redirectUrl: "javascript:alert(1)" }) });
    await u.click(await screen.findByRole("button", { name: "Allow" }));
    expect(await screen.findByText("Couldn't send your answer")).toBeInTheDocument();
    expect(go).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Allow" })).toBeEnabled();
  });

  it("says when the person allowed this app before", async () => {
    openConsent({ "GET /v1/oauth/consent": () => oauthConsent({ remembered: true }) });
    expect(await screen.findByText(/You allowed this app before/)).toBeInTheDocument();
  });

  it("signed out, shows the sign-in page, which returns here with the whole request", async () => {
    window.history.replaceState({}, "", path);
    const calls = mockApi({
      ...shellRoutes(),
      "GET /v1/me": () => Reply.error(401, "unauthorized"),
      "GET /v1/auth/config": () => authConfig({ oidcEnabled: true }),
    });
    renderApp(path);
    const signIn = await screen.findByRole("link", { name: /Sign in with single sign-on/ });
    expect(signIn).toHaveAttribute("href", "/auth/login?next=" + encodeURIComponent(path));
    // Nothing about the request is asked for before the person signs in.
    expect(calls.some((c) => c.url === "/v1/oauth/consent")).toBe(false);
  });
});

describe("consent helpers", () => {
  it("keeps the raw query without its ?", () => {
    expect(rawQuery(`?${query}`)).toBe(query);
    expect(rawQuery("client_id=x")).toBe("client_id=x");
  });

  it("allows only http(s) addresses to go back to", () => {
    expect(safeRedirect(back)).toBe(back);
    expect(safeRedirect("http://127.0.0.1:53211/callback?code=x")).toBeDefined();
    expect(safeRedirect("javascript:alert(1)")).toBeUndefined();
    expect(safeRedirect("data:text/html,hi")).toBeUndefined();
    expect(safeRedirect("not a url")).toBeUndefined();
  });
});
