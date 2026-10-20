/* The OAuth Connect consent page: an app asking to read only, and an app expecting another account (login_hint) than the one signed in. */
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import { redirectTo } from "../pages/connect/consent";
import { oauthConsent } from "./fixtures-oauth";
import { type Handler, mockApi, renderApp, shellRoutes } from "./harness";

const query =
  "response_type=code&client_id=https%3A%2F%2Fchat.example.com%2Foauth%2Fclient-metadata.json&redirect_uri=https%3A%2F%2Fchat.example.com%2Foauth%2Fcallback&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256&login_hint=pat%40example.com&state=123";
const path = `/connect/consent?${query}`;

function openConsent(routes: Record<string, Handler>) {
  window.history.replaceState({}, "", path);
  const calls = mockApi({ ...shellRoutes(), ...routes });
  return { calls, ...renderApp(path) };
}

afterEach(() => {
  vi.restoreAllMocks();
  window.history.replaceState({}, "", "/");
});

describe("OAuth consent page, scopes and accounts", () => {
  it("asks to read only for agents:read, and says the app can't send or change anything", async () => {
    openConsent({ "GET /v1/oauth/consent": () => oauthConsent({ scopes: ["agents:read"] }) });
    expect(await screen.findByRole("heading", { level: 1, name: "Team Chat wants to read your agents as you" })).toBeInTheDocument();
    const can = screen.getByRole("region", { name: "It will be able to" });
    expect(can).toHaveTextContent("See your agents and read your conversations with them");
    expect(can).toHaveTextContent("Never send messages or change anything");
    expect(can).not.toHaveTextContent("Create agents");
  });

  it("never allows another account than the app expects: it offers to sign in as that one, or to cancel", async () => {
    const u = userEvent.setup();
    const go = vi.spyOn(redirectTo, "go").mockImplementation(() => {});
    const { calls, container } = openConsent({
      "GET /v1/oauth/consent": () => oauthConsent({ loginHint: "pat@example.com", accountMismatch: true, remembered: true }),
      "POST /auth/logout": () => ({ ok: true }),
    });
    expect(await screen.findByText("This isn't the account the app expects")).toBeInTheDocument();
    expect(screen.getByText(/asked to connect/)).toHaveTextContent("Team Chat asked to connect pat@example.com, but you're signed in as una@example.com.");
    expect(screen.queryByRole("button", { name: "Allow" })).toBeNull();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();

    await u.click(screen.getByRole("button", { name: "Sign in as pat@example.com" }));
    await waitFor(() => expect(go).toHaveBeenCalledWith(`/oauth/authorize?${query}`));
    expect(calls.some((c) => c.method === "POST" && c.url === "/auth/logout")).toBe(true);
    expect(calls.some((c) => c.method === "POST" && c.url === "/v1/oauth/consent")).toBe(false);
  });

  it("cancels a request for another account as a denial", async () => {
    const u = userEvent.setup();
    const denied = "https://chat.example.com/oauth/callback?error=access_denied&state=123";
    const go = vi.spyOn(redirectTo, "go").mockImplementation(() => {});
    const { calls } = openConsent({
      "GET /v1/oauth/consent": () => oauthConsent({ loginHint: "pat@example.com", accountMismatch: true }),
      "POST /v1/oauth/consent": () => ({ redirectUrl: denied }),
    });
    await u.click(await screen.findByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(go).toHaveBeenCalledWith(denied));
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ query, decision: "deny" });
  });
});
